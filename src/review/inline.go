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
		"(?:\\*\\*)?:(?:\\*\\*)?\\s*`?([^\\s:`]+):(\\d+)(?:[-–](\\d+))?`?\\s*(?:—|–|-|:)?\\s*(.*)$")
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
	var kept []string
	var inline []InlineComment
	eachFinding(review, func(l string) { kept = append(kept, l) }, func(f postedFinding) {
		if f.line > 0 && lines[f.path][f.line] {
			inline = append(inline, InlineComment{Path: f.path, Line: f.line, Body: f.text})
			return
		}
		// Off the diff it stays in the body, named by its place the way a person would.
		kept = append(kept, f.named(), "")
	})
	return tidy(kept), inline
}

// FileComment is one file's findings as one plain PR comment.
type FileComment struct {
	Path string
	Body string
}

// FileComments takes every finding out of review and gathers them into one comment per file, in the order the files
// first appear, each finding named by its place; rest is what is left, the final comment. It is the review as plain PR
// comments: no comment can sit on a line, but each file is still commented on by itself.
func FileComments(review string) (files []FileComment, rest string) {
	var kept []string
	byFile := map[string][]string{}
	var order []string
	eachFinding(review, func(l string) { kept = append(kept, l) }, func(f postedFinding) {
		if _, seen := byFile[f.path]; !seen {
			order = append(order, f.path)
		}
		byFile[f.path] = append(byFile[f.path], f.named())
	})
	for _, p := range order {
		files = append(files, FileComment{Path: p, Body: strings.Join(byFile[p], "\n\n")})
	}
	return files, tidy(kept)
}

// postedFinding is one finding as it is posted: its text without the label, and its place.
type postedFinding struct {
	path, place, text string
	line              int // 0 when the place has no usable line number
}

// named is the finding as a comment that does not sit on its line: its place first, then its text.
func (f postedFinding) named() string { return "`" + f.place + "`: " + f.text }

// eachFinding walks review, handing every line that is no finding to other and every finding to found.
func eachFinding(review string, other func(string), found func(postedFinding)) {
	src := strings.Split(review, "\n")
	for i := 0; i < len(src); i++ {
		if fenceOpen(src[i]) != "" {
			// A code block outside any finding is the reviewer's text, whatever its lines look like.
			end := fenceEnd(src, i)
			for _, l := range src[i:end] {
				other(l)
			}
			i = end - 1
			continue
		}
		m := findingStart.FindStringSubmatch(src[i])
		if m == nil {
			other(src[i])
			continue
		}
		end := findingEnd(src, i)
		n, err := strconv.Atoi(m[3])
		if err != nil {
			n = 0 // no diff line has it, so the finding is named by its place
		}
		// The label sorts the finding for codeforge and the user's view; posted, a comment reads as the person wrote it,
		// with no "suggestion:" in front.
		body := []string{strings.TrimSpace(m[5])}
		for _, c := range src[i+1 : end] {
			body = append(body, strings.TrimRight(c, " \t"))
		}
		f := postedFinding{path: strings.TrimPrefix(m[2], "./"), line: n, text: strings.TrimSpace(dedent(body))}
		f.place = f.path + ":" + m[3]
		if m[4] != "" {
			f.place += "-" + m[4]
		}
		found(f)
		i = end - 1
	}
}

// Labeled is a Conventional Comment that names its place, as parsed from one line of a review.
type Labeled struct {
	Label string // "suggestion", "issue (blocking)", ...
	Path  string
	Line  int
	// End is the last line of a range ("a.go:192-195"), or Line for one line.
	End  int
	Text string // what follows the place on that line
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
	end := n
	if e, err := strconv.Atoi(m[4]); err == nil && e >= n {
		end = e
	}
	return Labeled{Label: strings.TrimSpace(m[1]), Path: strings.TrimPrefix(m[2], "./"), Line: n, End: end, Text: strings.TrimSpace(m[5])}, true
}

// LabeledEnd returns the index just past the finding whose first line is src[i]; see findingEnd.
func LabeledEnd(src []string, i int) int { return findingEnd(src, i) }

// findingEnd returns the index just past the finding that starts at i: it runs until the next finding, a heading, or a
// blank line followed by neither an indented continuation nor a fenced code block. A code block is the finding's
// example, so it stays with the finding even after a blank line, and nothing inside it (a `# comment`, a line that
// looks like a finding) ends the finding.
func findingEnd(src []string, i int) int {
	j := i + 1
	for j < len(src) {
		l := src[j]
		if fenceOpen(l) != "" {
			j = fenceEnd(src, j)
			continue
		}
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
		if k < len(src) && fenceOpen(src[k]) != "" {
			j = k
			continue
		}
		indented := k < len(src) && (strings.HasPrefix(src[k], " ") || strings.HasPrefix(src[k], "\t"))
		if !indented || findingStart.MatchString(src[k]) {
			return j
		}
		j = k
	}
	return j
}

// minFence is the shortest run of backticks or tildes that opens a Markdown code block.
const minFence = 3

// fenceOpen returns the fence a line opens a Markdown code block with (three or more backticks or tildes, after any
// indent), or "" for any other line. As in CommonMark, a backtick run with another backtick later on the line is
// inline code ("```x``` is unused"), not a fence.
func fenceOpen(l string) string {
	t := strings.TrimLeft(l, " \t")
	for _, c := range []string{"`", "~"} {
		n := len(t) - len(strings.TrimLeft(t, c))
		if n < minFence || (c == "`" && strings.Contains(t[n:], "`")) {
			continue
		}
		return strings.Repeat(c, n)
	}
	return ""
}

// fenceEnd returns the index just past the code block that opens at src[j]: past its closing fence, a line of at least
// the same run of the same character and nothing else, or the end of src when the block is never closed.
func fenceEnd(src []string, j int) int {
	end, _ := fenceBlock(src, j)
	return end
}

// fenceBlock is fenceEnd that also reports whether the block has its closing fence.
func fenceBlock(src []string, j int) (end int, closed bool) {
	open := fenceOpen(src[j])
	for k := j + 1; k < len(src); k++ {
		t := strings.TrimSpace(src[k])
		if strings.HasPrefix(t, open) && strings.Trim(t, open[:1]) == "" {
			return k + 1, true
		}
	}
	return len(src), false
}

// dedent removes the indentation the continuation lines share, so a nested list keeps its shape on its own. A code
// block is dedented by its own fence's indent and leaves the rest alone, so a block at the margin under an indented
// list moves neither.
func dedent(body []string) string {
	var prose []int
	for i := 1; i < len(body); i++ {
		if fenceOpen(body[i]) == "" {
			prose = append(prose, i)
			continue
		}
		end := min(fenceEnd(body, i), len(body))
		block := make([]int, 0, end-i)
		for k := i; k < end; k++ {
			block = append(block, k)
		}
		stripIndent(body, block, indentOf(body[i]))
		i = end - 1
	}
	shared := -1
	for _, i := range prose {
		if strings.TrimSpace(body[i]) != "" && (shared < 0 || indentOf(body[i]) < shared) {
			shared = indentOf(body[i])
		}
	}
	stripIndent(body, prose, shared)
	return strings.Join(body, "\n")
}

// indentOf counts the spaces and tabs a line starts with.
func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " \t")) }

// stripIndent removes up to n leading spaces and tabs from each of the lines of body at the given indexes.
func stripIndent(body []string, at []int, n int) {
	for _, i := range at {
		body[i] = body[i][min(max(n, 0), indentOf(body[i])):]
	}
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

// tidy drops headings that no longer have anything under them and collapses the blank lines left behind. Code blocks
// are left as they are: a `# comment` in one is code, and so are its blank lines.
func tidy(lines []string) string {
	for changed := true; changed; {
		changed = false
		code := inCode(lines)
		var out []string
		for i, l := range lines {
			if !code[i] && headingLine.MatchString(l) && sectionEmpty(headingLevel(l), lines[i+1:], code[i+1:]) {
				changed = true
				continue
			}
			out = append(out, l)
		}
		lines = out
	}
	code := inCode(lines)
	var out []string
	for i, l := range lines {
		if !code[i] && strings.TrimSpace(l) == "" && (len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == "") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// inCode reports, for each line, whether it belongs to a fenced code block, its fences included.
func inCode(lines []string) []bool {
	code := make([]bool, len(lines))
	for i := 0; i < len(lines); i++ {
		if fenceOpen(lines[i]) == "" {
			continue
		}
		end := fenceEnd(lines, i)
		for k := i; k < end; k++ {
			code[k] = true
		}
		i = end - 1
	}
	return code
}

// sectionEmpty reports whether a heading of level has nothing under it before the next heading of the same or a
// higher level. A line of code is something under it.
func sectionEmpty(level int, rest []string, code []bool) bool {
	for i, l := range rest {
		if code[i] {
			return false
		}
		if headingLine.MatchString(l) && headingLevel(l) <= level {
			return true
		}
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}
