package review

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// InlineComment is one finding posted on its line of the pull request's diff. A finding about a range of lines
// (path:192-195) sits on all of them: Line is the last and StartLine the first, so GitHub shows the whole range and a
// ```suggestion block in Body replaces it. StartLine is 0 for one line.
type InlineComment struct {
	Path      string
	Line      int
	StartLine int
	Body      string
}

// Place is where the comment sits, as the review names it: path:line or path:first-last.
func (ic InlineComment) Place() string {
	if ic.StartLine > 0 {
		return fmt.Sprintf("%s:%d-%d", ic.Path, ic.StartLine, ic.Line)
	}
	return fmt.Sprintf("%s:%d", ic.Path, ic.Line)
}

// danglingEnd matches the end of a range written after a space ("72- 74 — text"), at the start of the text, when the
// number stands alone: "74foo", "74.5" and "74-75" are no end.
var danglingEnd = regexp.MustCompile("^(\\d+)`?(?::\\s+|\\s+[—–-]\\s+|$)")

// endsCitation matches what may follow the end of a range: the text after a space or a separator, or nothing. Anything
// else ("–75", ",75", ":75") means the citation goes on and its end cannot be trusted.
var endsCitation = regexp.MustCompile("^`?(?:\\s|$|—|[:–-](?:\\D|$))")

// strictCitation matches a place written exactly as the skill asks, from its line number on: "72" or "72-74", then a
// space, a backtick, the end, or a separator before the text. Only such a place can carry an applicable suggestion;
// anything looser ("72.5", "72,74", "72- 74") might mean other lines than the ones it would replace.
var strictCitation = regexp.MustCompile("^\\d+(?:[-–]\\d+)?`?(?:$|(?:\\s+(?:[:—–-]\\s*)?|[:—–-]\\s*)(?:[^\\s\\d+:—–-]|$))")

// atLeast reports whether the number s is at least n.
func atLeast(s string, n int) bool {
	v, err := strconv.Atoi(s)
	return err == nil && v >= n
}

// The groups of findingStart.
const (
	groupLabel = iota + 1
	groupPath
	groupLine
	groupEnd // the last line of a range, as written: present, perhaps empty, only after a range delimiter
	groupText
)

var (
	newHunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	// findingStart matches a Conventional Comment that names its place: "suggestion: a/b.go:12 — text", with an optional
	// list marker, bold label, decoration and backticks around the place.
	findingStart = regexp.MustCompile("(?i)^\\s*(?:[-*]\\s+|\\d+\\.\\s+)?(?:\\*\\*)?" +
		`((?:issue|suggestion|question|nitpick|nit|optional|fyi|praise|todo|chore|note|thought)(?:\s*\([^)]*\))?)` +
		"(?:\\*\\*)?:(?:\\*\\*)?\\s*`?([^\\s:`]+):(\\d+)(?:[-–]([0-9+-][^\\s`:,;—–]*|))?`?\\s*(?:—|–|-|:)?\\s*(.*)$")
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

// preambleWords match a whole sentence written to the user about delivering the review, never one that says anything
// about the change: "The review is below.", "I found no blocking issues." A sentence with evidence in it ("I found that
// retries send payments twice.") does not match.
var preambleWords = regexp.MustCompile(`(?i)^(` +
	// handing the review over: nothing in it is about the change
	`((here is|here's|below is) (the|my) review|(the|my) review (is below|follows))` +
	`(,? (written )?in your voice|,? with the inline comments first|,? with the findings first)*|` +
	`details (are|follow) below|` +
	// a bare count of what the review found, with nothing said about any of it
	`i found (no|one|two|three|four|five|\d+|several|a few) (blocking )?(issues?|problems?|findings?|blockers?)( (to report|in this pr|in this change))?` +
	`)[.!]?$`)

// sentenceEnd splits a paragraph into sentences.
var sentenceEnd = regexp.MustCompile(`[.!?]\s+`)

// StripPreamble drops a note to the user that opens the review ("I found no blocking issues. The review is below…").
// The review starts with its findings, so a paragraph before the first finding in which every sentence talks about
// delivering the review is for the user, never for the PR. A paragraph with anything else in it is kept whole: it may
// be the reviewer's own words to the author ("I checked the rollout: old clients cannot read the new records. Deploy
// the reader first.").
func StripPreamble(review string) string {
	text := strings.TrimSpace(review)
	first, rest, found := strings.Cut(text, "\n\n")
	if !found || findingStart.MatchString(first) {
		return text
	}
	for _, s := range sentenceEnd.Split(strings.TrimSpace(first), -1) {
		if !preambleWords.MatchString(strings.TrimSpace(s)) {
			return text
		}
	}
	lines := strings.Split(rest, "\n")
	code := inCode(lines)
	for k, l := range lines {
		if !code[k] && findingStart.MatchString(l) {
			return strings.TrimSpace(rest)
		}
	}
	return text
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
	summary, inline, _ := splitReview(review, diff, false)
	return summary, inline
}

// SplitFindingsByFile is SplitFindings, with a finding off the diff in a file the pull request changes taken out as a
// comment on that file (onFile) rather than left in the summary: it is about code the change touches, so it goes on the
// file, not into the final comment. A finding in a file the change does not touch stays in the summary.
func SplitFindingsByFile(review, diff string) (summary string, inline, onFile []InlineComment) {
	return splitReview(review, diff, true)
}

func splitReview(review, diff string, byFile bool) (summary string, inline, onFile []InlineComment) {
	lines := diffLines(diff)
	var kept []string
	eachFinding(review, func(l string) { kept = append(kept, l) }, func(f postedFinding) {
		shown := lines[f.path]
		switch {
		case f.line <= 0 || !shown[f.line]:
		case hasSuggestion(f.text) && (!f.strict || !exactSuggestions(f.text) || openFence(strings.Split(f.text, "\n")) != ""):
			// A loosely written place ("72.5", "72,74", "72- 74") cannot be trusted with a one-click replacement, nor
			// can a suggestion never closed: GitHub would read it to the end of the comment, footer and all.
		case f.end > f.line && allShown(shown, f.line, f.end):
			inline = append(inline, InlineComment{Path: f.path, StartLine: f.line, Line: f.end, Body: f.text})
			return
		case !f.ranged || f.end == f.line || !hasSuggestion(f.text):
			// One line, or a range whose comment can sit on its first line: nothing in it rewrites the other lines.
			inline = append(inline, InlineComment{Path: f.path, Line: f.line, Body: f.text})
			return
		}
		if _, changed := lines[f.path]; byFile && changed && f.line > 0 {
			// A file comment says which line it means; a suggestion block there would apply to nothing, so it is code.
			onFile = append(onFile, InlineComment{Path: f.path, Line: f.line, Body: plainCode(f.text)})
			return
		}
		// Off the diff, or a suggestion for a range the diff does not show whole (on one line it would replace that
		// line alone), it stays in the body, named by its place the way a person would.
		kept = append(kept, Named(f.place, f.text), "")
	})
	return tidy(kept), inline, onFile
}

// suggestionFence matches the opening line of a GitHub suggestion block.
var suggestionFence = regexp.MustCompile("(?mi)^([ \\t]*(?:```+|~~~+))[ \\t]*suggestion(?::[-+0-9]*)?[ \\t]*$")

// plainSuggestion matches a bare suggestion fence, which replaces exactly the lines the comment sits on. One with an
// offset (```suggestion:-1+1) reaches lines the finding did not cite.
var plainSuggestion = regexp.MustCompile("(?i)^[ \\t]*(?:```+|~~~+)[ \\t]*suggestion[ \\t]*$")

// suggestionOpeners lists the lines of text that open a GitHub suggestion block: a fence that opens a block at the top
// level, not inline code ("```suggestion``` is…") and not a line inside another block, such as a quoted example.
func suggestionOpeners(lines []string) []int {
	var at []int
	for i := 0; i < len(lines); i++ {
		if fenceOpen(lines[i]) == "" {
			continue
		}
		if suggestionFence.MatchString(lines[i]) {
			at = append(at, i)
		}
		i = fenceEnd(lines, i) - 1
	}
	return at
}

// hasSuggestion reports whether text holds a GitHub suggestion block.
func hasSuggestion(text string) bool { return len(suggestionOpeners(strings.Split(text, "\n"))) > 0 }

// exactSuggestions reports whether every suggestion block in text is a bare one.
func exactSuggestions(text string) bool {
	lines := strings.Split(text, "\n")
	for _, i := range suggestionOpeners(lines) {
		if !plainSuggestion.MatchString(lines[i]) {
			return false
		}
	}
	return true
}

// plainCode turns every suggestion block in text into a plain code block, leaving every other line as it is.
func plainCode(text string) string {
	lines := strings.Split(text, "\n")
	for _, i := range suggestionOpeners(lines) {
		lines[i] = suggestionFence.ReplaceAllString(lines[i], "$1")
	}
	return strings.Join(lines, "\n")
}

// Named is a finding as a comment that does not sit on its line: its place first, then its text. A suggestion block in
// it becomes a plain code block, since only a comment on the lines can be applied.
func Named(place, text string) string {
	return "`" + place + "`: " + plainCode(text)
}

// allShown reports whether the diff shows every line from first to last, so one comment can cover them: GitHub refuses
// a range that leaves the diff.
func allShown(shown map[int]bool, first, last int) bool {
	for n := first; n <= last; n++ {
		if !shown[n] {
			return false
		}
	}
	return true
}

// postedFinding is one finding as it is posted: its text without the label, and its place.
type postedFinding struct {
	path, place, text string
	line              int  // 0 when the place has no usable line number
	end               int  // the last line of a range (path:192-195), or 0 when it has none or it is not a number
	ranged            bool // whether the place names a range, valid or not
	strict            bool // whether the place is written exactly, so a suggestion on it can be applied
}

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
		loc := findingStart.FindStringSubmatchIndex(src[i])
		if loc == nil {
			other(src[i])
			continue
		}
		group := func(k int) string {
			if loc[2*k] < 0 {
				return ""
			}
			return src[i][loc[2*k]:loc[2*k+1]]
		}
		end := findingEnd(src, i)
		n, err := strconv.Atoi(group(groupLine))
		if err != nil {
			n = 0 // no diff line has it, so the finding is named by its place
		}
		// The label sorts the finding for codeforge and the user's view; posted, a comment reads as the person wrote it,
		// with no "suggestion:" in front. Code lines are kept as written: a suggestion applies them verbatim.
		rest := src[i+1 : end]
		code := inCode(rest)
		body := []string{strings.TrimSpace(group(groupText))}
		for k, c := range rest {
			if !code[k] {
				c = strings.TrimRight(c, " \t")
			}
			body = append(body, c)
		}
		f := postedFinding{path: strings.TrimPrefix(group(groupPath), "./"), line: n, text: strings.TrimSpace(dedent(body)),
			strict: strictCitation.MatchString(src[i][loc[2*groupLine]:])}
		f.place = f.path + ":" + group(groupLine)
		if last, at := group(groupEnd), loc[2*groupEnd]; at >= 0 {
			valid := last != "" && endsCitation.MatchString(src[i][loc[2*groupEnd+1]:])
			switch m := danglingEnd.FindStringSubmatch(f.text); {
			case last != "":
				// "72-74", or a malformed end ("72--1", "72-74foo", "72-74,75"): a range either way, never one line.
				f.ranged = true
			case m != nil && atLeast(m[1], n):
				// "72- 74 — text": the end is the number after the space, not the start of the text ("72- 3 callers
				// break" keeps its 3).
				f.ranged, valid = true, true
				last, f.text = m[1], strings.TrimPrefix(f.text, m[0])
			case strings.IndexFunc(f.text, unicode.IsDigit) == 0:
				// "72- 74foo": a range whose end cannot be read.
				f.ranged = true
			}
			// Otherwise the dash separates the place from the text ("72–Close the body", "72- Close the body").
			if f.ranged {
				if last != "" {
					f.place += "-" + last
				}
				if e, err := strconv.Atoi(last); valid && err == nil && e > 0 && !strings.ContainsAny(last, "+-") {
					f.end = e
				}
			}
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
	if e, err := strconv.Atoi(m[4]); err == nil && e >= n && !strings.ContainsAny(m[4], "+-") {
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

// maxFenceIndent is how far past its opener a closing fence may be indented and still close the block.
const maxFenceIndent = 3

// tabColumns is how many columns a leading tab takes, as CommonMark counts it.
const tabColumns = 4

// columns is the width of a line's leading spaces and tabs.
func columns(l string) int {
	n := 0
	for _, r := range l {
		switch r {
		case ' ':
			n++
		case '\t':
			n += tabColumns - n%tabColumns
		default:
			return n
		}
	}
	return n
}

// fenceBlock is fenceEnd that also reports whether the block has its closing fence.
func fenceBlock(src []string, j int) (end int, closed bool) {
	open := fenceOpen(src[j])
	for k := j + 1; k < len(src); k++ {
		// As in CommonMark, a closing fence has only spaces and tabs around it, and one indented four columns or more
		// past its opener is code, not a close. Other whitespace (U+00A0, U+2003) leaves the block open.
		t := strings.Trim(src[k], " \t")
		if strings.HasPrefix(t, open) && strings.Trim(t, open[:1]) == "" && columns(src[k])-columns(src[j]) <= maxFenceIndent {
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
