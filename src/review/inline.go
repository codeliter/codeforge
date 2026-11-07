package review

import (
	"regexp"
	"strconv"
	"strings"
)

// InlineComment is one finding posted on its line of the pull request's diff.
type InlineComment struct {
	Path string
	Line int
	Body string
}

var (
	newHunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	// findingStart matches a Conventional Comment that names its place: "suggestion: a/b.go:12 — text", with an optional
	// list marker, bold label, decoration and backticks around the place.
	findingStart = regexp.MustCompile("(?i)^\\s*(?:[-*]\\s+|\\d+\\.\\s+)?(?:\\*\\*)?" +
		`((?:issue|suggestion|question|nitpick|nit|optional|fyi|praise|todo|chore|note|thought)(?:\s*\([^)]*\))?)` +
		"(?:\\*\\*)?:(?:\\*\\*)?\\s*`?([^\\s:`]+):(\\d+)(?:-\\d+)?`?\\s*(?:—|–|-|:)?\\s*(.*)$")
	verdictLead = regexp.MustCompile(`(?i)^\s*(?:\d\.\s*)?(?:\*\*)?verdict(?:\*\*)?\s*:?\s*(?:\*\*)?\s*(?:approve|comment|request changes)\b\.?(?:\*\*)?\s*[:.—–-]?\s*`)
	headingLine = regexp.MustCompile("^\\s*(?:#{1,6}\\s+.*|\\*\\*[^*]+\\*\\*:?|`?[\\w.@+-]+(?:/[\\w.@+-]+)*\\.\\w+`?:?)\\s*$")
)

// diffLines lists, per file, the lines of the new version that the diff shows: the only lines GitHub accepts an inline
// comment on.
func diffLines(diff string) map[string]map[int]bool {
	out := map[string]map[int]bool{}
	file, line, inHunk := "", 0, false
	for _, l := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			file, inHunk = "", false
			continue
		case !inHunk && strings.HasPrefix(l, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			if file == "/dev/null" {
				file = ""
			}
			continue
		case strings.HasPrefix(l, "@@ "):
			m := newHunkHeader.FindStringSubmatch(l)
			start, err := 0, error(nil)
			if m != nil {
				start, err = strconv.Atoi(m[1])
			}
			line, inHunk = start, m != nil && err == nil
			continue
		}
		if !inHunk || file == "" {
			continue
		}
		switch {
		case l == "" || l[0] == ' ' || l[0] == '+':
			if out[file] == nil {
				out[file] = map[int]bool{}
			}
			out[file][line] = true
			line++
		case l[0] == '-' || l[0] == '\\':
		default:
			inHunk = false
		}
	}
	return out
}

// StripVerdict drops a leading "Verdict: Comment." label: the review states its findings, and posting it is never an
// approval, so the label only repeats what the findings say.
func StripVerdict(review string) string {
	lines := strings.Split(strings.TrimSpace(review), "\n")
	if !verdictLead.MatchString(lines[0]) {
		return strings.TrimSpace(review)
	}
	lines[0] = verdictLead.ReplaceAllString(lines[0], "")
	if strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// SplitFindings takes out of review every finding whose path:line is on the diff and returns them as inline comments,
// with the rest of the review as the summary. A finding off the diff stays in the summary, so nothing is lost when a
// line cannot take a comment. Headings left with nothing under them are dropped.
func SplitFindings(review, diff string) (string, []InlineComment) {
	lines := diffLines(diff)
	src := strings.Split(review, "\n")
	var kept []string
	var inline []InlineComment
	for i := 0; i < len(src); i++ {
		m := findingStart.FindStringSubmatch(src[i])
		if m == nil {
			kept = append(kept, src[i])
			continue
		}
		end := findingEnd(src, i)
		n, err := strconv.Atoi(m[3])
		path := strings.TrimPrefix(m[2], "./")
		if err != nil || !lines[path][n] {
			kept = append(kept, src[i:end]...)
			i = end - 1
			continue
		}
		body := []string{strings.TrimSpace(m[1]) + ": " + strings.TrimSpace(m[4])}
		for _, c := range src[i+1 : end] {
			body = append(body, strings.TrimRight(c, " \t"))
		}
		inline = append(inline, InlineComment{Path: path, Line: n, Body: strings.TrimSpace(dedent(body))})
		i = end - 1
	}
	return tidy(kept), inline
}

// Labeled is a Conventional Comment that names its place, as parsed from one line of a review.
type Labeled struct {
	Label string // "suggestion", "issue (blocking)", ...
	Path  string
	Line  int
	Text  string // what follows the place on that line
}

// ParseLabeled reads a finding from the first line of one ("suggestion: a.go:12 — text", with the same list markers,
// bold and backticks SplitFindings accepts).
func ParseLabeled(line string) (Labeled, bool) {
	m := findingStart.FindStringSubmatch(line)
	if m == nil {
		return Labeled{}, false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return Labeled{}, false
	}
	return Labeled{Label: strings.TrimSpace(m[1]), Path: strings.TrimPrefix(m[2], "./"), Line: n, Text: strings.TrimSpace(m[4])}, true
}

// LabeledEnd returns the index just past the finding whose first line is src[i]; see findingEnd.
func LabeledEnd(src []string, i int) int { return findingEnd(src, i) }

// findingEnd returns the index just past the finding that starts at i: it runs until the next finding, a heading, or a
// blank line that is not followed by an indented continuation.
func findingEnd(src []string, i int) int {
	j := i + 1
	for j < len(src) {
		l := src[j]
		if findingStart.MatchString(l) || headingLine.MatchString(l) {
			return j
		}
		if strings.TrimSpace(l) != "" {
			j++
			continue
		}
		k := j
		for k < len(src) && strings.TrimSpace(src[k]) == "" {
			k++
		}
		indented := k < len(src) && (strings.HasPrefix(src[k], " ") || strings.HasPrefix(src[k], "\t"))
		if !indented || findingStart.MatchString(src[k]) {
			return j
		}
		j = k
	}
	return j
}

// dedent removes the indentation the continuation lines share, so a nested list keeps its shape on its own.
func dedent(body []string) string {
	indent := -1
	for _, l := range body[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	for i := 1; i < len(body) && indent > 0; i++ {
		if len(body[i]) >= indent {
			body[i] = body[i][indent:]
		}
	}
	return strings.Join(body, "\n")
}

// plainHeading ranks a bold line or a bare file path used as a heading below every Markdown heading level.
const plainHeading = 7

// headingLevel is a Markdown heading's level, or plainHeading for a bold line or a bare file path.
func headingLevel(l string) int {
	t := strings.TrimSpace(l)
	if n := len(t) - len(strings.TrimLeft(t, "#")); n > 0 {
		return n
	}
	return plainHeading
}

// tidy drops headings that no longer have anything under them and collapses the blank lines left behind.
func tidy(lines []string) string {
	for changed := true; changed; {
		changed = false
		var out []string
		for i, l := range lines {
			if headingLine.MatchString(l) && sectionEmpty(headingLevel(l), lines[i+1:]) {
				changed = true
				continue
			}
			out = append(out, l)
		}
		lines = out
	}
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" && (len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == "") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// sectionEmpty reports whether a heading of level has nothing under it before the next heading of the same or a
// higher level.
func sectionEmpty(level int, rest []string) bool {
	for _, l := range rest {
		if headingLine.MatchString(l) && headingLevel(l) <= level {
			return true
		}
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}
