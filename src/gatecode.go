package src

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// Around a finding the report shows contextBefore lines before it and contextAfter after, plus the header of the
// declaration it sits in when that starts within headerSearch lines; a header no more than headerJoin lines above the
// window is joined to it. Without a terminal to measure, a line is cut at maxCodeLine bytes; a terminal narrower than
// minCodeWidth still gets that many. A code card is at most maxCardWidth columns wide.
const (
	contextBefore = 6
	contextAfter  = 4
	headerSearch  = 80
	headerJoin    = 4
	maxCodeLine   = 200
	minCodeWidth  = 20
	maxCardWidth  = 120
	tabWidth      = 4
	// gapRow stands for the left-out lines between a declaration's header and the window; headerRows is both.
	gapRow     = 0
	headerRows = 2
	// How the code a comment names marks lines: within hintReach lines of the cited ones, names of at least minHintLen
	// characters found on at most hintMax lines, and never more than maxMarkedSpan lines apart.
	hintReach     = 12
	minHintLen    = 4
	hintMax       = 3
	maxMarkedSpan = 16
	// maxContextLines is the most lines a snippet grows to so it shows the whole construct a finding sits in; a longer
	// construct is shown as the window around the finding with its header on top.
	maxContextLines = 40
)

// codeStyle is how the report is drawn: the Codeforge Steel palette and highlighting only on a terminal that wants
// colour, the width to keep code lines within, and whether findings wrap at that width (only on a terminal: piped
// output keeps each finding on one line for whatever reads it).
type codeStyle struct {
	color bool
	wrap  bool
	width int
	pal   palette
}

func codeStyleFor(ctx context.Context, w io.Writer) codeStyle {
	st := codeStyle{width: maxCodeLine}
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return st
	}
	if cols, _, err := term.GetSize(int(f.Fd())); err == nil && cols > 0 {
		st.width, st.wrap = cols, true
	}
	if resumeHintColors(w) {
		st.color, st.pal = true, paletteFor(appearanceProbe(ctx))
	}
	return st
}

// paint colours s with one of the palette's escapes (st.pal.accent, st.pal.dim, st.pal.bold), on a colour terminal only.
func (st codeStyle) paint(code, s string) string {
	if !st.color {
		return s
	}
	return st.pal.paint(code, s)
}

// codeAround renders the lines of file around line from the working tree: numbered, the finding's line marked, the
// indentation they share removed and every line kept within the terminal's width. On a terminal the code is
// highlighted for its language. It is empty when the path leaves the repository, the file cannot be read or the line
// is not in it: the finding is still shown, only without code.
func codeAround(repo, file string, line int, st codeStyle) string {
	return codeAroundFocus(repo, file, focus{Line: line}, st)
}

// codeAroundFocus is codeAround for a finding with a focus; see codeSnippetFocus.
func codeAroundFocus(repo, file string, fc focus, st codeStyle) string {
	if fc.Line < 1 {
		return ""
	}
	// os.Root refuses a path, or a symlink, that leads out of the repository.
	root, err := os.OpenRoot(repo)
	if err != nil {
		return ""
	}
	defer root.Close()
	data, err := root.ReadFile(file)
	if err != nil {
		return ""
	}
	return codeSnippetFocus(string(data), file, fc, st)
}

// focus is what a snippet is about: the lines a finding cites (Line to End) and the code it names in backticks
// (Hints), which marks the lines nearby that contain it. A model often cites the first line of a statement while the
// comment is about a call three lines down; the hints find that call.
type focus struct {
	Line, End int
	Hints     []string
}

// codeSnippetFocus renders the lines of content, the text of file, around a finding, as codeAround describes. Every
// line the finding is about is marked: the cited ones and those near them that contain code the comment names. The
// window runs from contextBefore lines above the first marked line to contextAfter below the last; when the
// declaration the code sits in (a function, method, class or block header) starts above the window, its first line is
// shown at the top, with a gap marker when lines are left out, so the reader knows where they are.
func codeSnippetFocus(content, file string, fc focus, st codeStyle) string {
	line := fc.Line
	if line < 1 {
		return ""
	}
	all := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if line > len(all) {
		return ""
	}
	for i, l := range all {
		all[i] = strings.ReplaceAll(strings.TrimRight(l, "\r"), "\t", strings.Repeat(" ", tabWidth))
	}
	marked := markedLines(all, fc)
	lo, hi := line, line
	for n := range marked {
		lo, hi = min(lo, n), max(hi, n)
	}
	first, last := max(1, lo-contextBefore), min(len(all), hi+contextAfter)
	// The window grows to the whole construct the finding sits in, a short function or the loop or branch within a long
	// one, so the reader sees where it starts and ends instead of a fixed slice cut mid-way.
	if from, to := enclosingSpan(all, lo, hi); from > 0 {
		first, last = min(first, from), max(last, to)
	}
	header := enclosingHeader(all, lo)
	switch {
	case header >= first:
		header = 0 // already in the window
	case header > 0 && first-header <= headerJoin:
		first, header = header, 0 // close enough: show every line from the header down
	}
	nums := make([]int, 0, last-first+headerRows)
	if header > 0 {
		nums = append(nums, header, gapRow)
	}
	for n := first; n <= last; n++ {
		nums = append(nums, n)
	}
	code := make([]string, len(nums))
	for i, n := range nums {
		if n > 0 {
			code[i] = all[n-1]
		}
	}
	code = trimSharedIndent(code)
	numWidth := len(strconv.Itoa(last))
	place := fmt.Sprintf("%s:%d", file, line)
	if fc.End > line {
		place += fmt.Sprintf("-%d", fc.End)
	}
	if st.color {
		return codeCard(place, file, marked, nums, code, numWidth, st)
	}
	// "  > 12 | " before the code.
	avail := max(minCodeWidth, st.width-(numWidth+len("  >  | ")))
	var s strings.Builder
	for i, n := range nums {
		switch {
		case n == gapRow:
			fmt.Fprintf(&s, "    %*s ⋮\n", numWidth, "")
		case marked[n]:
			fmt.Fprintf(&s, "  > %*d | %s\n", numWidth, n, cutColumns(code[i], avail))
		default:
			fmt.Fprintf(&s, "    %*d | %s\n", numWidth, n, cutColumns(code[i], avail))
		}
	}
	return s.String()
}

// markedLines returns the lines a finding is about: the cited range, plus the lines within hintReach of it that
// contain code its comment names. A name found on more than hintMax lines says nothing about where to look and is
// ignored, and marked lines never span more than maxMarkedSpan lines.
func markedLines(all []string, fc focus) map[int]bool {
	end := min(len(all), max(fc.End, fc.Line))
	marked := map[int]bool{}
	for n := fc.Line; n <= end && n-fc.Line < maxMarkedSpan; n++ {
		marked[n] = true
	}
	lo, hi := max(1, fc.Line-hintReach), min(len(all), end+hintReach)
	for _, h := range fc.Hints {
		var found []int
		for _, needle := range hintNeedles(h) {
			for n := lo; n <= hi; n++ {
				if strings.Contains(all[n-1], needle) {
					found = append(found, n)
				}
			}
			if len(found) > 0 {
				break // the full name matched; its shorter form would only add noise
			}
		}
		if len(found) == 0 || len(found) > hintMax {
			continue
		}
		for _, n := range found {
			if abs(n-fc.Line) < maxMarkedSpan && abs(n-end) < maxMarkedSpan {
				marked[n] = true
			}
		}
	}
	return marked
}

// hintNeedles turns a name from the comment into what to look for in the code: as written, then without a trailing
// call ("sdktrace.AlwaysSample()" → "sdktrace.AlwaysSample"). Names too short or with no letters say nothing.
func hintNeedles(h string) []string {
	h = strings.TrimSpace(h)
	if len(h) < minHintLen || !strings.ContainsFunc(h, unicode.IsLetter) || strings.ContainsAny(h, "\n") {
		return nil
	}
	out := []string{h}
	if i := strings.Index(h, "("); i >= minHintLen {
		out = append(out, h[:i])
	}
	return out
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// codeCard draws the snippet as a card that stands out from the review's prose: a title row with the place, then the
// code on a raised background, the lines the finding is about on a brighter one with an accent bar, and gaps as a dim
// marker.
func codeCard(place, file string, marked map[int]bool, nums []int, code []string, numWidth int, st codeStyle) string {
	const margin, pad, beforeCode = "  ", 1, "  "
	width := min(maxCardWidth, max(minCodeWidth, st.width-len(margin)))
	gutter := len("▌") + numWidth + len(beforeCode)
	avail := max(minCodeWidth, width-gutter-pad)
	for i := range code {
		code[i] = cutColumns(code[i], avail)
	}
	shown := highlight(file, code, st.pal.code)
	row := func(bg, text string) string {
		// Every reset inside the highlighted text would also end the background; carry it across them.
		text = strings.ReplaceAll(text, ansiReset, ansiReset+bg)
		fill := max(0, width-ansi.StringWidth(text))
		return margin + bg + text + strings.Repeat(" ", fill) + ansiReset + "\n"
	}
	var s strings.Builder
	s.WriteString(row(st.pal.cardTitle, st.pal.accent+cutColumns(" "+place, width)+ansiReset))
	for i, n := range nums {
		switch {
		case n == gapRow:
			s.WriteString(row(st.pal.card, st.pal.dim+fmt.Sprintf(" %*s ⋮", numWidth, "")+ansiReset))
		case marked[n]:
			s.WriteString(row(st.pal.cardLine, st.pal.accent+"▌"+fmt.Sprintf("%*d", numWidth, n)+ansiReset+beforeCode+shown[i]))
		default:
			s.WriteString(row(st.pal.card, st.pal.dim+" "+fmt.Sprintf("%*d", numWidth, n)+ansiReset+beforeCode+shown[i]))
		}
	}
	return s.String()
}

// declLine matches the first line of a declaration or block in common languages: Go, Python, JS/TS, Rust, Java,
// Ruby, shell, Terraform and YAML keys.
var declLine = regexp.MustCompile(`^\s*(func|def|class|function|fn|pub|impl|trait|interface|struct|enum|type|module|resource|data|locals|variable|describe|it|test|public|private|protected|static|async|export|const\s+\w+\s*=\s*(async\s*)?\(|[\w.]+\s*\(\)\s*\{|[\w.-]+:\s*$)`)

// enclosingHeader returns the line number of the declaration (function, method, class, resource) that contains line,
// found by walking up through ever less indented lines within headerSearch lines. Without one, the nearest enclosing
// block opener ("if x {") is used; without that, 0.
func enclosingHeader(all []string, line int) int {
	target := indentOf(all[line-1])
	if strings.TrimSpace(all[line-1]) == "" {
		target = math.MaxInt
	}
	block := 0
	for n := line - 1; n >= 1 && n >= line-headerSearch && target > 0; n-- {
		l := all[n-1]
		if strings.TrimSpace(l) == "" || indentOf(l) >= target {
			continue
		}
		if declLine.MatchString(l) {
			return n
		}
		if block == 0 && strings.HasSuffix(strings.TrimSpace(l), "{") {
			block = n
		}
		target = indentOf(l) // an outer statement; keep looking for what contains it
	}
	return block
}

// enclosingSpan returns the first and last line of the largest construct around lines lo to hi that has at most
// maxContextLines lines: walking out from the innermost block (an if, a loop) to the declaration that holds it, and
// stopping at that declaration or at the first construct too long to show whole. It returns 0, 0 when even the innermost
// one is too long, or when the lines sit at the top level.
func enclosingSpan(all []string, lo, hi int) (from, to int) {
	target := indentOf(all[lo-1])
	if strings.TrimSpace(all[lo-1]) == "" {
		target = math.MaxInt
	}
	for n := lo - 1; n >= 1 && n >= lo-headerSearch && target > 0; n-- {
		l := all[n-1]
		if strings.TrimSpace(l) == "" || indentOf(l) >= target {
			continue
		}
		target = indentOf(l)
		end := blockEnd(all, n)
		if end < hi {
			continue // a statement that ends above the finding, such as a multi-line call's closing line; look further out
		}
		if end-n+1 > maxContextLines {
			break
		}
		from, to = n, end
		if declLine.MatchString(l) {
			break
		}
	}
	return from, to
}

// blockEnd is the last line of the block that opens at line start: the last line indented deeper than it, or the
// closing line at its own indentation (a brace, bracket, end, fi or done) that follows them.
func blockEnd(all []string, start int) int {
	indent, last := indentOf(all[start-1]), start
	for n := start + 1; n <= len(all); n++ {
		t := strings.TrimSpace(all[n-1])
		if t == "" {
			continue
		}
		if indentOf(all[n-1]) <= indent {
			if closesBlock(t) {
				return n
			}
			return last
		}
		last = n
	}
	return last
}

func closesBlock(t string) bool {
	if strings.HasPrefix(t, "}") || strings.HasPrefix(t, ")") || strings.HasPrefix(t, "]") || strings.HasPrefix(t, "</") {
		return true
	}
	word, _, _ := strings.Cut(t, " ")
	switch strings.TrimRight(word, ";") {
	case "end", "fi", "done", "esac":
		return true
	}
	return false
}

// cutColumns keeps s within width characters, ending a cut line with "…". It counts characters, not bytes, so a line
// of accented or other multi-byte text is not cut short.
func cutColumns(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	return string([]rune(s)[:max(0, width-1)]) + "…"
}

// span is a run of text in one colour (code is a palette escape, "" for none).
// glue joins its first word to the previous span's last one, as in "`codeforge respond`;" where no space separates them.
type span struct {
	text, code string
	glue       bool
}

// wrapFinding lays out "- " and the spans word by word within width, continuing on lines indented under the text, so a
// long finding never runs past the terminal's edge. A width of 0 keeps it on one line.
func wrapFinding(st codeStyle, width int, spans ...span) string {
	return wrapSpans(st, width, "- ", "  ", spans...)
}

// wrapSpans lays out lead and the spans word by word within width, continuing on lines that start with hang. A width of
// 0 keeps everything on one line.
func wrapSpans(st codeStyle, width int, lead, hang string, spans ...span) string {
	var b strings.Builder
	b.WriteString(lead)
	col, first := utf8.RuneCountInString(lead), true
	for _, sp := range spans {
		for i, word := range strings.Fields(sp.text) {
			n := utf8.RuneCountInString(word)
			switch {
			case first, i == 0 && sp.glue:
			case width > 0 && col+1+n > width:
				b.WriteString("\n" + hang)
				col = utf8.RuneCountInString(hang)
			default:
				b.WriteString(" ")
				col++
			}
			if sp.code != "" {
				word = st.paint(sp.code, word)
			}
			b.WriteString(word)
			col += n
			first = false
		}
	}
	b.WriteString("\n")
	return b.String()
}

// trimSharedIndent removes the leading spaces every non-blank line has, so deeply nested code starts at the gutter.
func trimSharedIndent(lines []string) []string {
	shared := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " ")); shared < 0 || n < shared {
			shared = n
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= shared && shared > 0 {
			l = l[shared:]
		}
		out[i] = strings.TrimRight(l, " ")
	}
	return out
}

// highlight colours lines as the language of file in style, falling back to the plain lines without a style or
// whenever the lexer cannot keep them one for one.
func highlight(file string, lines []string, style *chroma.Style) []string {
	return highlightWith(lexers.Match(file), lines, style)
}

// highlightLang is highlight for a fenced block whose info string names lang ("go", "yaml", ...), or a file name.
func highlightLang(lang string, lines []string, style *chroma.Style) []string {
	lexer := lexers.Get(lang)
	if lexer == nil && lang != "" {
		lexer = lexers.Match("x." + lang)
	}
	return highlightWith(lexer, lines, style)
}

func highlightWith(lexer chroma.Lexer, lines []string, style *chroma.Style) []string {
	if style == nil {
		return lines
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, strings.Join(lines, "\n")+"\n")
	if err != nil {
		return lines
	}
	split := chroma.SplitTokensIntoLines(it.Tokens())
	if len(split) < len(lines) {
		return lines
	}
	out := make([]string, len(lines))
	for i := range lines {
		var b strings.Builder
		if err := formatters.TTY16m.Format(&b, style, chroma.Literator(split[i]...)); err != nil {
			return lines
		}
		out[i] = strings.TrimRight(b.String(), "\n")
	}
	return out
}
