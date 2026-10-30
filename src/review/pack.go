package review

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	packFileBudget   = 60_000
	maxFileBytes     = 20_000
	maxSymbols       = 12
	maxCallersPerSym = 8
)

// changedSymbol matches exported declarations on added or removed lines in Go, TS/JS and Python.
var changedSymbol = regexp.MustCompile(`(?m)^[+-]\s*(?:func\s+(?:\([^)]*\)\s*)?([A-Z]\w*)|type\s+([A-Z]\w*)|export\s+(?:default\s+)?(?:async\s+)?(?:function|class|const|interface|type)\s+(\w+)|def\s+(\w+))`)

// Pack front-loads what a juror would otherwise fetch with many tool calls: whole changed files and their callers.
func Pack(ctx context.Context, r Runner, repo string, l Local) string {
	var s strings.Builder

	budget := packFileBudget
	s.WriteString("\n<changed_files>\n")
	for _, f := range l.Files {
		if secretName.MatchString(f) {
			continue
		}
		data, err := readInRepo(repo, f)
		if err != nil || len(data) > maxFileBytes || len(data) > budget || !isText(data) {
			continue
		}
		budget -= len(data)
		fmt.Fprintf(&s, "=== %s ===\n%s\n", f, numbered(string(data)))
	}
	s.WriteString("</changed_files>\n")

	callers := Callers(ctx, r, repo, l.Diff, l.Files)
	if callers != "" {
		fmt.Fprintf(&s, "\n<callers>\n%s</callers>\n", callers)
	}
	return s.String()
}

// Callers lists where changed exported symbols are used outside the changed files.
func Callers(ctx context.Context, r Runner, repo, diff string, changed []string) string {
	var s strings.Builder
	for _, sym := range symbols(diff) {
		out, err := r.Run(ctx, "git", append([]string{"-C", repo, "grep", "-n", "-w", "-e", sym, "--"}, secretExcludes...)...)
		if err != nil {
			continue
		}

		var hits []string
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
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
