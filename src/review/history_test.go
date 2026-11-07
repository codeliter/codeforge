//nolint:err113 // dynamic error messages are okay in tests
package review

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Mining a repository's own history for review cases with real ground truth. A "fix" commit changes lines that an
// earlier commit introduced; those lines are where the earlier change went wrong. Reviewing that earlier change and
// asking whether the review points at the lines the fix later changed measures recall on bugs nobody injected.
//
// The bug lines are labelled by git blame at the fix's parent: each line the fix removed or changed is traced to the
// commit that wrote it, and to its line number in that commit's version of the file.

// minedCase is a change that a later fix commit repaired.
type minedCase struct {
	Intro, Parent, Fix, Subject string
	DiffBytes                   int
	Defects                     []Defect
}

// mineOptions bound what is mined: how many recent commits to scan for fixes, and the sizes of change worth reviewing.
type mineOptions struct {
	Scan               int
	MinBytes, MaxBytes int
	MaxDefects         int
	// MaxFixLines skips a fix that rewrote more old lines than this: a broad rewrite is a refactor or a redesign, and its
	// lines say little about where a bug was. Zero means no limit.
	MaxFixLines int
}

var (
	fixSubject = regexp.MustCompile(`(?i)^fix\b`)
	hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+`)
	blameHead  = regexp.MustCompile(`^([0-9a-f]{40}) (\d+) (\d+)`)
)

// atoi reads a number the patterns above already matched as digits; it is 0 for anything else.
func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// changedRange is a run of lines a fix removed or rewrote, in the fix's parent.
type changedRange struct {
	path          string
	start, length int
}

// parseOldRanges reads the old-side line ranges of the Go source files a -U0 diff changes. A hunk that only inserts
// lines has no old side and is skipped: there is nothing to trace to the change that wrote it.
func parseOldRanges(diff string) []changedRange {
	var out []changedRange
	path := ""
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "--- a/"):
			path = strings.TrimPrefix(line, "--- a/")
		case strings.HasPrefix(line, "--- "):
			path = ""
		default:
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil || path == "" {
				continue
			}
			start := atoi(m[1])
			length := 1
			if m[2] != "" {
				length = atoi(m[2])
			}
			if length > 0 {
				out = append(out, changedRange{path, start, length})
			}
		}
	}
	return out
}

// blamedLine is a line traced to the commit that wrote it.
type blamedLine struct {
	commit, path string
	line         int
	text         string
}

// parseBlame reads git blame --porcelain output: for each line the commit and the line's number in that commit's file.
func parseBlame(out string) []blamedLine {
	var lines []blamedLine
	files := map[string]string{}
	var cur blamedLine
	for line := range strings.SplitSeq(out, "\n") {
		if m := blameHead.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, "\t") {
			cur = blamedLine{commit: m[1]}
			cur.line = atoi(m[2])
			continue
		}
		if name, ok := strings.CutPrefix(line, "filename "); ok {
			files[cur.commit] = name
			continue
		}
		if strings.HasPrefix(line, "\t") && cur.commit != "" {
			cur.path = files[cur.commit]
			cur.text = strings.TrimSpace(strings.TrimPrefix(line, "\t"))
			lines = append(lines, cur)
		}
	}
	return lines
}

// oldLines is how many old lines the ranges cover.
func oldLines(ranges []changedRange) int {
	n := 0
	for _, r := range ranges {
		n += r.length
	}
	return n
}

// codeLine reports whether a line of Go source can hold a bug: a comment, a blank line or a lone brace cannot, and a fix
// that rewrote one changed the prose or the layout, not the behaviour.
func codeLine(text string) bool {
	switch {
	case text == "", strings.HasPrefix(text, "//"), strings.HasPrefix(text, "/*"), strings.HasPrefix(text, "*"):
		return false
	case strings.Trim(text, "{}()[], ") == "":
		return false
	}
	return true
}

// mergeLines turns line numbers into ranges, joining those no more than gap lines apart.
func mergeLines(lines []int, gap int) [][2]int {
	slices.Sort(lines)
	lines = slices.Compact(lines)
	var out [][2]int
	for _, n := range lines {
		if last := len(out) - 1; last >= 0 && n-out[last][1] <= gap {
			out[last][1] = n
			continue
		}
		out = append(out, [2]int{n, n})
	}
	return out
}

// mineHistory finds fix commits among the last opts.Scan commits and returns, for each change they repaired, the
// repaired lines. Merge commits, tests-only fixes and changes outside opts' size bounds are left out.
func mineHistory(ctx context.Context, dir string, opts mineOptions) ([]minedCase, error) {
	log, err := gitOut(ctx, dir, "log", "--no-merges", "-n", strconv.Itoa(opts.Scan), "--format=%H%x09%s")
	if err != nil {
		return nil, err
	}
	byIntro := map[string]*minedCase{}
	var order []string
	for entry := range strings.SplitSeq(strings.TrimSpace(log), "\n") {
		fix, subject, ok := strings.Cut(entry, "\t")
		if !ok || !fixSubject.MatchString(subject) {
			continue
		}
		diff, err := gitOut(ctx, dir, "diff", "--no-renames", "-U0", fix+"^", fix, "--", "*.go", ":(exclude)*_test.go")
		if err != nil {
			continue
		}
		perIntro := map[string]map[string][]int{}
		ranges := parseOldRanges(diff)
		if opts.MaxFixLines > 0 && oldLines(ranges) > opts.MaxFixLines {
			continue
		}
		for _, r := range ranges {
			out, err := gitOut(ctx, dir, "blame", "--porcelain", "-w", "-L", fmt.Sprintf("%d,%d", r.start, r.start+r.length-1), fix+"^", "--", r.path)
			if err != nil {
				continue
			}
			for _, b := range parseBlame(out) {
				if !codeLine(b.text) {
					continue
				}
				if perIntro[b.commit] == nil {
					perIntro[b.commit] = map[string][]int{}
				}
				perIntro[b.commit][b.path] = append(perIntro[b.commit][b.path], b.line)
			}
		}
		for intro, files := range perIntro {
			parents, err := gitOut(ctx, dir, "rev-list", "--parents", "-n", "1", intro)
			if err != nil || len(strings.Fields(parents)) != 2 {
				continue // a root or merge commit has no single change to review
			}
			mc := byIntro[intro]
			if mc == nil {
				mc = &minedCase{Intro: intro, Parent: intro + "^", Fix: fix, Subject: subject}
				byIntro[intro] = mc
				order = append(order, intro)
			}
			for path, lines := range files {
				for _, rg := range mergeLines(lines, lineSlack) {
					mc.Defects = append(mc.Defects, Defect{Name: fmt.Sprintf("%s:%d-%d fixed by %.7s", path, rg[0], rg[1], fix), File: path, Lo: rg[0], Hi: rg[1]})
				}
			}
		}
	}
	var cases []minedCase
	for _, intro := range order {
		mc := byIntro[intro]
		size, err := gitOut(ctx, dir, "diff", "--no-renames", intro+"^", intro)
		if err != nil || len(size) < opts.MinBytes || len(size) > opts.MaxBytes || len(mc.Defects) == 0 {
			continue
		}
		mc.DiffBytes = len(size)
		slices.SortFunc(mc.Defects, func(a, b Defect) int { return strings.Compare(a.Name, b.Name) })
		if opts.MaxDefects > 0 && len(mc.Defects) > opts.MaxDefects {
			mc.Defects = mc.Defects[:opts.MaxDefects]
		}
		cases = append(cases, *mc)
	}
	return cases, nil
}

func histGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "user.email=e@e", "-c", "user.name=e"}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func histCommit(t *testing.T, dir, file, body, message string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600))
	histGit(t, dir, "add", "-A")
	histGit(t, dir, "commit", "-q", "-m", message)
	return histGit(t, dir, "rev-parse", "HEAD")
}

func TestParseOldRanges(t *testing.T) {
	diff := "diff --git a/p/a.go b/p/a.go\n--- a/p/a.go\n+++ b/p/a.go\n@@ -5,2 +5,3 @@ func x() {\n-a\n-b\n+c\n@@ -20 +21 @@\n-d\n+e\n@@ -30,0 +32,2 @@\n+only added\n" +
		"diff --git a/new.go b/new.go\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,3 @@\n+x\n"
	require.Equal(t, []changedRange{{"p/a.go", 5, 2}, {"p/a.go", 20, 1}}, parseOldRanges(diff))
}

func TestParseBlame(t *testing.T) {
	sha1, sha2 := strings.Repeat("a", 40), strings.Repeat("b", 40)
	out := sha1 + " 7 10 2\nauthor x\nfilename p/a.go\n\tline one\n" + sha1 + " 8 11\n\tline two\n" + sha2 + " 3 12 1\nfilename p/b.go\n\tline three\n"
	require.Equal(t, []blamedLine{{sha1, "p/a.go", 7, "line one"}, {sha1, "p/a.go", 8, "line two"}, {sha2, "p/b.go", 3, "line three"}}, parseBlame(out))
}

func TestMergeLines(t *testing.T) {
	require.Equal(t, [][2]int{{3, 4}, {10, 10}, {20, 21}}, mergeLines([]int{21, 3, 4, 10, 20, 4}, 3))
	require.Equal(t, [][2]int{{3, 10}}, mergeLines([]int{3, 6, 10}, 4))
	require.Empty(t, mergeLines(nil, 3))
}

func TestMineHistory_FindsTheChangeAFixRepaired(t *testing.T) {
	dir := t.TempDir()
	histGit(t, dir, "init", "-q", "-b", "main")
	histCommit(t, dir, "base.go", "package p\n\nfunc Base() int { return 1 }\n", "base")
	body := "package p\n\n// Page returns n.\nfunc Page(n int) int {\n\tif n > 10 {\n\t\treturn 10\n\t}\n\tpadding := 1\n\t_ = padding\n\treturn n + 1\n}\n"
	intro := histCommit(t, dir, "page/page.go", body, "Add paging")
	histCommit(t, dir, "other.go", "package p\n\nfunc Other() {}\n", "Add other")
	fixed := strings.Replace(body, "return n + 1", "return n", 1)
	fix := histCommit(t, dir, "page/page.go", fixed, "fix(page): the last page was one too high")
	histCommit(t, dir, "docs.go", "package p\n", "Tidy")

	cases, err := mineHistory(t.Context(), dir, mineOptions{Scan: 20, MinBytes: 1, MaxBytes: 100_000})
	require.NoError(t, err)
	require.Len(t, cases, 1)
	require.Equal(t, intro, cases[0].Intro)
	require.Equal(t, fix, cases[0].Fix)
	require.Equal(t, []Defect{{Name: "page/page.go:10-10 fixed by " + fix[:7], File: "page/page.go", Lo: 10, Hi: 10}}, cases[0].Defects)

	small, err := mineHistory(t.Context(), dir, mineOptions{Scan: 20, MinBytes: 100_000, MaxBytes: 200_000})
	require.NoError(t, err)
	require.Empty(t, small, "a change below the size bound is not worth a case")

	narrow, err := mineHistory(t.Context(), dir, mineOptions{Scan: 1, MinBytes: 1, MaxBytes: 100_000})
	require.NoError(t, err)
	require.Empty(t, narrow, "the fix is outside the scanned commits")
}

func TestMineHistory_IgnoresNonFixesAndTestOnlyFixes(t *testing.T) {
	dir := t.TempDir()
	histGit(t, dir, "init", "-q", "-b", "main")
	histCommit(t, dir, "a.go", "package p\n\nfunc A() int { return 1 }\n", "base")
	histCommit(t, dir, "a_test.go", "package p\n\nfunc TestA() {}\n", "Add test")
	histCommit(t, dir, "a.go", "package p\n\nfunc A() int { return 2 }\n", "Change A")
	histCommit(t, dir, "a_test.go", "package p\n\nfunc TestA() { _ = 1 }\n", "fix: the test")
	cases, err := mineHistory(t.Context(), dir, mineOptions{Scan: 20, MinBytes: 1, MaxBytes: 100_000})
	require.NoError(t, err)
	require.Empty(t, cases, "a change to a non-fix subject, or to tests only, is not mined")
}

func TestCodeLine(t *testing.T) {
	for _, code := range []string{"return n + 1", "if x > 0 {", "} else {", "const limit = 5", "x := y // trailing note"} {
		require.True(t, codeLine(code), code)
	}
	for _, other := range []string{"", "// A comment.", "/* block */", "* continued", "}", "})", "{", ") {"} {
		require.False(t, codeLine(other), other)
	}
}

func TestMineHistory_IgnoresFixesToCommentsAndBroadRewrites(t *testing.T) {
	dir := t.TempDir()
	histGit(t, dir, "init", "-q", "-b", "main")
	histCommit(t, dir, "base.go", "package p\n", "base")
	body := "package p\n\n// Old wording of the comment.\nfunc A() int {\n\treturn 1\n}\n"
	histCommit(t, dir, "a.go", body, "Add A")
	histCommit(t, dir, "a.go", strings.Replace(body, "Old wording", "New wording", 1), "fix: a comment")
	cases, err := mineHistory(t.Context(), dir, mineOptions{Scan: 20, MinBytes: 1, MaxBytes: 100_000})
	require.NoError(t, err)
	require.Empty(t, cases, "a fix that only rewrote prose repaired no bug")

	var list strings.Builder
	list.WriteString("package p\n")
	for i := range 30 {
		fmt.Fprintf(&list, "func F%d() int { return %d }\n", i, i)
	}
	many := list.String()
	histCommit(t, dir, "many.go", many, "Add many")
	histCommit(t, dir, "many.go", strings.ReplaceAll(many, "return", "return 0 +"), "fix: everything")
	broad, err := mineHistory(t.Context(), dir, mineOptions{Scan: 20, MinBytes: 1, MaxBytes: 100_000, MaxFixLines: 10})
	require.NoError(t, err)
	require.Empty(t, broad, "a fix that rewrote thirty lines is a rewrite, not a repair")
	all, err := mineHistory(t.Context(), dir, mineOptions{Scan: 20, MinBytes: 1, MaxBytes: 100_000})
	require.NoError(t, err)
	require.Len(t, all, 1, "without the limit the same fix is mined")
}
