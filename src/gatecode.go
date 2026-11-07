package src

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"golang.org/x/term"
)

// Around a finding the report shows contextLines lines on each side. Without a terminal to measure, a line is cut at
// maxCodeLine bytes; a terminal narrower than minCodeWidth still gets that many.
const (
	contextLines = 2
	maxCodeLine  = 200
	minCodeWidth = 20
	tabWidth     = 4
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
	if line < 1 {
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
	all := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if line > len(all) {
		return ""
	}
	first, last := max(1, line-contextLines), min(len(all), line+contextLines)
	numWidth := len(strconv.Itoa(last))
	// "  > 12 | " before the code.
	avail := max(minCodeWidth, st.width-(numWidth+len("  >  | ")))
	code := make([]string, 0, last-first+1)
	for _, l := range all[first-1 : last] {
		code = append(code, strings.ReplaceAll(strings.TrimRight(l, "\r"), "\t", strings.Repeat(" ", tabWidth)))
	}
	code = trimSharedIndent(code)
	for i, l := range code {
		code[i] = cutColumns(l, avail)
	}
	shown := code
	if st.color {
		shown = highlight(file, code, st.pal.code)
	}
	var s strings.Builder
	for i, l := range shown {
		n := first + i
		switch {
		case !st.color && n == line:
			fmt.Fprintf(&s, "  > %*d | %s\n", numWidth, n, l)
		case !st.color:
			fmt.Fprintf(&s, "    %*d | %s\n", numWidth, n, l)
		case n == line:
			fmt.Fprintf(&s, "  %s %s %s\n", st.paint(st.pal.accent, fmt.Sprintf("▶ %*d", numWidth, n)), st.paint(st.pal.dim, "│"), l)
		default:
			fmt.Fprintf(&s, "  %s %s\n", st.paint(st.pal.dim, fmt.Sprintf("  %*d │", numWidth, n)), l)
		}
	}
	return s.String()
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
type span struct{ text, code string }

// wrapFinding lays out "- " and the spans word by word within width, continuing on lines indented under the text, so a
// long finding never runs past the terminal's edge. A width of 0 keeps it on one line.
func wrapFinding(st codeStyle, width int, spans ...span) string {
	const lead, hang = "- ", "  "
	var b strings.Builder
	b.WriteString(lead)
	col, first := len(lead), true
	for _, sp := range spans {
		for _, word := range strings.Fields(sp.text) {
			n := utf8.RuneCountInString(word)
			switch {
			case first:
			case width > 0 && col+1+n > width:
				b.WriteString("\n" + hang)
				col = len(hang)
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
	if style == nil {
		return lines
	}
	lexer := lexers.Match(file)
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
