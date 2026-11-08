package review

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	maxFileBytes     = 20_000
	maxSymbols       = 12
	maxCallersPerSym = 8
)

// changedSymbol matches exported declarations on added or removed lines in Go, TS/JS and Python.
var changedSymbol = regexp.MustCompile(`(?m)^[+-]\s*(?:func\s+(?:\([^)]*\)\s*)?([A-Z]\w*)|type\s+([A-Z]\w*)|export\s+(?:default\s+)?(?:async\s+)?(?:function|class|const|interface|type)\s+(\w+)|def\s+(\w+))`)

const (
	docsPackBytes     = 10_000
	smallPackBytes    = 20_000
	standardPackBytes = 40_000
	riskyPackBytes    = 60_000
)

// packBudget bounds the whole files sent with the brief: every byte is re-billed on each juror step, so a
// small change gets a small pack and only risky ones get the full one. Docs-only changes get a little, because
// a reviewer with no file in the brief spends its few steps fetching it and can run out before its verdict.
func packBudget(t Tier) int {
	switch t.Name {
	case TierDocs.Name:
		return docsPackBytes
	case TierSmall.Name:
		return smallPackBytes
	case TierStandard.Name:
		return standardPackBytes
	}
	return riskyPackBytes
}

// Pack front-loads what a juror would otherwise fetch with many tool calls: the changed files and their callers, within
// the tier's budget; see packFiles for what goes in first.
func Pack(ctx context.Context, r Runner, repo string, l Local, t Tier) string {
	var s strings.Builder
	s.WriteString(packFiles(l.Files, l.Diff, packBudget(t), func(f string) ([]byte, error) { return readInRepo(repo, f) }))
	callers := Callers(ctx, r, repo, l.Diff, l.Files)
	if callers != "" {
		fmt.Fprintf(&s, "\n<callers>\n%s</callers>\n", callers)
	}
	if withAudit {
		s.WriteString(auditSection(l.Diff))
	}
	return s.String()
}

// PackRef is Pack for a PR whose head is fetched as ref: the changed files are read from that ref, because the working
// tree is not the change, and so are their callers. Without them a reviewer spends its few steps fetching files and
// can run out before its verdict.
func PackRef(ctx context.Context, r Runner, ref string, files []string, diff string, t Tier) string {
	var s strings.Builder
	s.WriteString(packFiles(files, diff, packBudget(t), func(f string) ([]byte, error) {
		out, err := r.Run(ctx, "git", "show", ref+":"+f)
		if err != nil {
			return nil, fmt.Errorf("reading %s at %s: %w", f, ref, err)
		}
		return out, nil
	}))
	if callers := CallersAt(ctx, r, ref, diff, files); callers != "" {
		fmt.Fprintf(&s, "\n<callers>\n%s</callers>\n", callers)
	}
	if withAudit {
		s.WriteString(auditSection(diff))
	}
	return s.String()
}

// excerptContext is how many lines around each changed hunk an excerpt keeps: enough for the function a change sits in.
const excerptContext = 25

// The order files go into the pack in.
const (
	packCode = iota
	packTests
	packDocs
)

// packFiles writes the changed files for the brief within budget bytes. Code goes in first, then tests, then docs, so
// prose about the change never pushes out the code it describes. A whole file goes in when it fits; a file too large
// for that goes in as excerpts around its changed hunks, so every changed code file is in the brief at least where it
// changed. Secrets and binaries are never sent.
//
// It fills the budget in two passes: first every file in its smallest useful form (its excerpts, or the whole file
// when that is smaller), so no changed code is left out while there is room for its changes; then, in the same order,
// excerpts are widened to whole files while the budget lasts.
func packFiles(files []string, diff string, budget int, read func(string) ([]byte, error)) string {
	type entry struct {
		name, whole, part string
		full, in          bool
	}
	hunks := hunkRanges(diff)
	var entries []*entry
	for _, f := range packOrder(files) {
		if secretName.MatchString(f) {
			continue
		}
		data, err := read(f)
		if err != nil || !isText(data) {
			continue
		}
		e := &entry{name: f, part: excerpt(string(data), hunks[f], excerptContext)}
		if len(data) <= maxFileBytes {
			e.whole = numbered(string(data))
		}
		entries = append(entries, e)
	}
	for _, e := range entries {
		switch {
		case e.whole != "" && (e.part == "" || len(e.whole) <= len(e.part)) && len(e.whole) <= budget:
			e.in, e.full, budget = true, true, budget-len(e.whole)
		case e.part != "" && len(e.part) <= budget:
			e.in, budget = true, budget-len(e.part)
		}
	}
	for _, e := range entries {
		if e.in && !e.full && e.whole != "" && len(e.whole)-len(e.part) <= budget {
			e.full, budget = true, budget-(len(e.whole)-len(e.part))
		}
	}
	var s strings.Builder
	s.WriteString("\n<changed_files>\n")
	var left []string
	for _, e := range entries {
		switch {
		case !e.in:
			left = append(left, e.name)
		case e.full:
			fmt.Fprintf(&s, "=== %s ===\n%s\n", e.name, e.whole)
		default:
			fmt.Fprintf(&s, "=== %s (excerpts around the changes; read the rest only if a finding needs it) ===\n%s\n", e.name, e.part)
		}
	}
	if len(left) > 0 {
		fmt.Fprintf(&s, "Not included, read them if a finding needs them: %s\n", strings.Join(left, ", "))
	}
	s.WriteString("</changed_files>\n")
	return s.String()
}

// packOrder puts code first, then tests, then docs and everything else that is not code, keeping the diff's order
// within each group.
func packOrder(files []string) []string {
	rank := func(f string) int {
		switch {
		case docExts[strings.ToLower(path.Ext(f))] || strings.HasPrefix(f, "docs/"):
			return packDocs
		case isTestFile(f):
			return packTests
		}
		return packCode
	}
	out := slices.Clone(files)
	slices.SortStableFunc(out, func(a, b string) int { return rank(a) - rank(b) })
	return out
}

var testFile = regexp.MustCompile(`(_test\.go|\.test\.[jt]sx?|\.spec\.[jt]sx?|_test\.py)$|(^|/)(test_[^/]*\.py|tests?/)`)

func isTestFile(f string) bool { return testFile.MatchString(f) }

var hunkStart = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// hunkRanges lists, per file, the new-side line ranges the diff changes.
func hunkRanges(diff string) map[string][][2]int {
	out := map[string][][2]int{}
	file := ""
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
		case strings.HasPrefix(l, "@@ ") && file != "":
			m := hunkStart.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1]) //nolint:errcheck // the pattern only matches digits
			n := 1
			if m[2] != "" {
				n, _ = strconv.Atoi(m[2]) //nolint:errcheck // the pattern only matches digits
			}
			out[file] = append(out[file], [2]int{start, start + max(n, 1) - 1})
		}
	}
	return out
}

// excerpt returns the numbered lines of content within around lines of each range, merged where they meet, with a
// marker where lines are left out. It is empty without ranges.
func excerpt(content string, ranges [][2]int, around int) string {
	if len(ranges) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	keep := make([]bool, len(lines)+1)
	for _, r := range ranges {
		for n := max(1, r[0]-around); n <= min(len(lines), r[1]+around); n++ {
			keep[n] = true
		}
	}
	var s strings.Builder
	gap := false
	for n := 1; n <= len(lines); n++ {
		if !keep[n] {
			gap = true
			continue
		}
		if gap && s.Len() > 0 {
			s.WriteString("   …\n")
		}
		gap = false
		fmt.Fprintf(&s, "%4d  %s\n", n, lines[n-1])
	}
	return s.String()
}

// Callers lists where changed exported symbols are used outside the changed files.
func Callers(ctx context.Context, r Runner, repo, diff string, changed []string) string {
	return grepCallers(ctx, r, diff, changed, func(sym string) []string {
		return append([]string{"-C", repo, "grep", "-n", "-w", "-e", sym, "--"}, secretExcludes...)
	}, "")
}

// CallersAt is Callers for a PR head fetched as ref: the search runs in that tree, not the working tree.
func CallersAt(ctx context.Context, r Runner, ref, diff string, changed []string) string {
	return grepCallers(ctx, r, diff, changed, func(sym string) []string {
		return append([]string{"grep", "-n", "-w", "-e", sym, ref, "--"}, secretExcludes...)
	}, ref+":")
}

// grepCallers runs one git grep per changed symbol; prefix is what git puts before each hit's path, stripped.
func grepCallers(ctx context.Context, r Runner, diff string, changed []string, grep func(string) []string, prefix string) string {
	var s strings.Builder
	for _, sym := range symbols(diff) {
		out, err := r.Run(ctx, "git", grep(sym)...)
		if err != nil {
			continue
		}

		var hits []string
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimPrefix(line, prefix)
			file, _, _ := strings.Cut(line, ":")
			if line == "" || slices.Contains(changed, file) || secretName.MatchString(file) {
				continue
			}
			hits = append(hits, line)
			if len(hits) == maxCallersPerSym {
				break
			}
		}
		if len(hits) > 0 {
			fmt.Fprintf(&s, "%s:\n  %s\n", sym, strings.Join(hits, "\n  "))
		}
	}
	return s.String()
}

func symbols(diff string) []string {
	var out []string
	for _, m := range changedSymbol.FindAllStringSubmatch(diff, -1) {
		for _, name := range m[1:] {
			if name != "" && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
		if len(out) == maxSymbols {
			break
		}
	}
	return out
}

func numbered(src string) string {
	var s strings.Builder
	for i, line := range strings.Split(strings.TrimRight(src, "\n"), "\n") {
		fmt.Fprintf(&s, "%4d  %s\n", i+1, line)
	}
	return s.String()
}

func isText(data []byte) bool {
	const sniff = 8000
	return !slices.Contains(data[:min(len(data), sniff)], 0)
}
