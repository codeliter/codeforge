package src

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
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
	// maxAddedLines is the most lines of a declaration the change adds whole that are shown; a longer one is shown as
	// any other code is.
	maxAddedLines = 150
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
	// Added is the lines of the file that the change under review adds, when that is known: a declaration the change
	// adds whole is shown whole.
	Added map[int]bool
	// Removed is the lines the change removed from the file, each run under the number of the line it stood before:
	// they are shown there, marked as removed.
	Removed map[int][]string
	// Whole asks for every place of the file the change touches, not only the one cited: a reader walked through a
	// change must not take the cited place for all of it. Stops are the lines of the file that other stops of the tour
	// cite; a change shown at a stop of its own is left to it.
	Whole bool
	Stops []int
	// Reach asks for the whole of the changed place the cited lines touch, however far it runs: what a stop that
	// leaves the rest of the file to an earlier one still owes its reader.
	Reach bool
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
	all := snippetLines(content)
	if line > len(all) {
		return ""
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
	// A function, type or block the change adds whole is all new to the reader, so all of it is shown, not a slice.
	if from, to := addedDeclaration(file, all, lo, hi, fc.Added); from > 0 {
		first, last = from, to
	}
	// So is a constant or variable the change adds with a value of several lines (a list, a map, a tuple): it is
	// shown to its last entry, with the lines around the finding.
	if from, to := addedValue(file, all, lo, hi, fc.Added); from > 0 {
		first, last = min(first, from), max(last, to)
	}
	// The header of the declaration the code sits in is shown above the window, and joined to it when close enough.
	shown := []lineRange{{first, last}}
	if header := enclosingHeader(all, lo); header > 0 && header < first {
		shown = append(shown, lineRange{header, header})
	}
	tail := ""
	if fc.Whole || fc.Reach {
		var more []lineRange
		more, tail = otherChanges(all, fc, shown, first, last)
		shown = append(shown, more...)
	}
	shown = joinRanges(shown)
	last = shown[len(shown)-1].to
	nums := make([]int, 0, last-first+headerRows)
	code := make([]string, 0, cap(nums))
	// What the change removed is shown where it stood, before the line that follows it now.
	lost := func(n int) {
		for _, l := range removedRun(fc.Removed[n]) {
			nums, code = append(nums, removedRow), append(code, l)
		}
	}
	for i, r := range shown {
		if i > 0 {
			nums, code = append(nums, gapRow), append(code, "")
		}
		for n := r.from; n <= r.to; n++ {
			lost(n)
			nums, code = append(nums, n), append(code, all[n-1])
		}
	}
	if last == len(all) {
		lost(last + 1)
	}
	code = trimSharedIndent(code)
	numWidth := len(strconv.Itoa(last))
	place := fmt.Sprintf("%s:%d", file, line)
	if fc.End > line {
		place += fmt.Sprintf("-%d", fc.End)
	}
	// With the change known, every line says what the change did to it: "+" added, "-" removed, nothing for the rest.
	signs := fc.signs()
	if st.color {
		return codeCard(place, file, marked, nums, code, numWidth, signs, tail, st)
	}
	// "  > 12 | " before the code, and "+ " when lines carry a sign.
	bar := " | "
	if signs != nil {
		bar += "  "
	}
	avail := max(minCodeWidth, st.width-(numWidth+len("  > ")+len(bar)))
	var s strings.Builder
	for i, n := range nums {
		if n == gapRow {
			fmt.Fprintf(&s, "    %*s ⋮\n", numWidth, "")
			continue
		}
		mark, num, sign := "    ", "", ""
		if marked[n] {
			mark = "  > "
		}
		if n > 0 {
			num = strconv.Itoa(n)
		}
		if signs != nil {
			sign = signs(n) + " "
		}
		// A line too long for the width goes on under itself, with no number: nothing of it is cut off.
		for k, part := range wrapColumns(code[i], avail) {
			if k > 0 {
				mark, num, sign = "    ", "", strings.Repeat(" ", len(sign))
			}
			fmt.Fprintf(&s, "%s%*s | %s%s\n", mark, numWidth, num, sign, part)
		}
	}
	if tail != "" {
		fmt.Fprintf(&s, "    %*s   %s\n", numWidth, "", tail)
	}
	return s.String()
}

// lineRange is a run of lines of a file, first to last.
type lineRange struct{ from, to int }

// joinRanges puts ranges in the file's order and joins those that overlap or have at most headerJoin-1 lines between
// them: a gap that small is shorter to read than to skip.
func joinRanges(ranges []lineRange) []lineRange {
	slices.SortFunc(ranges, func(a, b lineRange) int { return cmp.Or(a.from-b.from, a.to-b.to) })
	if len(ranges) == 0 {
		return nil
	}
	out := ranges[:1]
	for _, r := range ranges[1:] {
		if cur := &out[len(out)-1]; r.from-cur.to <= headerJoin {
			cur.to = max(cur.to, r.to)
			continue
		}
		out = append(out, r)
	}
	return out
}

// Besides the lines a snippet is about, one that shows the whole of a file's change shows every other place the
// change touches: otherContext lines around each, and at most maxOtherLines lines in all; a place is shown in part
// only when at least minOtherLines of it fit.
const (
	otherContext  = 3
	maxOtherLines = 80
	minOtherLines = 8
	// aboveShare is the part of the room, one in so many lines, that a change running both ways from the window gets
	// above it.
	aboveShare = 2
)

// otherChanges lists the places of the file the change touches outside the window, first to last, which shown draws
// with its header: each place with the lines around it and the header of the declaration it sits in. It says in words
// what is left out: a place another stop of the tour is about, which is read there, and what does not fit.
func otherChanges(all []string, fc focus, shown []lineRange, first, last int) (ranges []lineRange, note string) {
	var changed []int
	for n := range fc.Added {
		if n >= 1 && n <= len(all) {
			changed = append(changed, n)
		}
	}
	for n := range fc.Removed {
		if n >= 1 {
			changed = append(changed, min(n, len(all))) // lines removed at the end of the file stand after its last
		}
	}
	slices.Sort(changed)
	var places []lineRange
	for _, n := range changed {
		if k := len(places) - 1; k >= 0 && n-places[k].to <= 2*otherContext {
			places[k].to = n
			continue
		}
		places = append(places, lineRange{n, n})
	}
	// What a place costs is the rows it adds to what is shown, counted as they are drawn: its lines, the removed lines
	// shown among them, the lines between it and a neighbour that joinRanges fills in, and its declaration's header.
	rows := func(rs ...[]lineRange) (n int) {
		for _, r := range joinRanges(slices.Concat(rs...)) {
			for l := r.from; l <= r.to; l++ {
				n += 1 + len(removedRun(fc.Removed[l]))
			}
			if r.to == len(all) {
				n += len(removedRun(fc.Removed[len(all)+1]))
			}
		}
		return n
	}
	// Whether the change shows at line l: it adds the line, or removed lines stand before it (or after the file's last).
	changes := func(l int) bool {
		return fc.Added[l] || len(fc.Removed[l]) > 0 || (l == len(all) && len(fc.Removed[l+1]) > 0)
	}
	base := slices.Clone(shown)
	room, own := maxOtherLines, 0
	var judged []lineRange // the places the reader is owed: what is left out of them is said
	for _, p := range places {
		r := lineRange{max(1, p.from-otherContext), min(len(all), p.to+otherContext)}
		before := rows(base)
		// A place that reaches the window is read with it, as far as there is room, like any other.
		touched := r.to >= first && r.from <= last
		switch {
		case rows(base, []lineRange{r}) == before:
			continue // the window shows all of it
		case !touched && !fc.Whole:
			continue
		case !touched && slices.ContainsFunc(fc.Stops, func(n int) bool { return n >= r.from && n <= r.to }):
			own++
			continue
		}
		if judged = append(judged, p); room < minOtherLines {
			continue
		}
		// A place the window is in is shown around the window, its lines above and those below, each as far as there
		// is room: cut at its far end, not at the window. Any other is shown from its start, under its header.
		parts := []lineRange{r}
		var header []lineRange
		if touched {
			parts = parts[:0]
			if r.from < first {
				parts = append(parts, lineRange{r.from, first - 1})
			}
			if r.to > last {
				parts = append(parts, lineRange{max(r.from, last+1), r.to})
			}
		} else if h := enclosingHeader(all, p.from); h > 0 && h < r.from {
			header = []lineRange{{h, h}}
		}
		for _, part := range parts {
			share, above := room, touched && part.to < first
			if above && len(parts) > 1 {
				share = room / aboveShare
			}
			before = rows(base)
			// A line is a row at least, so no more lines than rows fit: a change of any length is cut to that at once.
			if part.to-part.from+1 > share {
				if above {
					part.from = part.to - share + 1
				} else {
					part.to = part.from + share - 1
				}
			}
			// Then a line at a time until it fits: a line can cost several rows, with what was removed there, and a cut
			// can part the place from a neighbour it was joined to.
			for part.to >= part.from && rows(base, header, []lineRange{part})-before > share {
				if above {
					part.from++
				} else {
					part.to--
				}
			}
			// A fragment of the lines around a change, with none of the change, tells the reader nothing.
			some := false
			for l := part.from; l <= part.to && !some; l++ {
				some = changes(l)
			}
			if !some {
				continue
			}
			add := slices.Concat(header, []lineRange{part})
			ranges, base = append(ranges, add...), append(base, add...)
			room -= rows(base) - before
		}
	}
	// What is left out is read from what is drawn, not kept count of on the way: a place with none of its changed
	// lines on show is not shown, one with some of them is cut short.
	drawn := joinRanges(slices.Clone(base))
	cut, short := 0, false
	for _, p := range judged {
		seen, total := 0, 0
		for l := p.from; l <= p.to; l++ {
			if !changes(l) {
				continue
			}
			total++
			if slices.ContainsFunc(drawn, func(r lineRange) bool { return l >= r.from && l <= r.to }) {
				seen++
			}
		}
		switch {
		case seen == 0:
			cut++
		case seen < total:
			short = true
		}
	}
	var notes []string
	switch {
	case own == 1:
		notes = append(notes, "1 more change at a stop of its own")
	case own > 1:
		notes = append(notes, fmt.Sprintf("%d more changes at stops of their own", own))
	}
	if cut > 0 {
		notes = append(notes, plural(cut, "more change")+" not shown")
	}
	if short {
		notes = append(notes, "a long change is cut short")
	}
	if len(notes) > 0 {
		note = "In this file: " + strings.Join(notes, "; ") + "."
	}
	return ranges, note
}

// wrapWords breaks text at its spaces into lines of at most width characters; a word longer than that is cut by
// wrapColumns.
func wrapWords(text string, width int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(text) {
		switch {
		case cur == "":
			cur = w
		case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) <= width:
			cur += " " + w
		default:
			lines, cur = append(lines, cur), w
		}
		for utf8.RuneCountInString(cur) > width {
			parts := wrapColumns(cur, width)
			lines, cur = append(lines, parts[0]), strings.Join(parts[1:], "")
		}
	}
	return append(lines, cur)
}

// removedRow stands, among a snippet's line numbers, for a line the change removed: it has no number in the file.
const removedRow = -1

// maxRemovedRun is the most lines of one removal that a snippet shows; the rest are counted.
const maxRemovedRun = 12

// removedRun is the removed lines as a snippet shows them: tabs as spaces, and a long run cut to its first lines with
// a count of the rest.
func removedRun(lines []string) []string {
	out := make([]string, 0, min(len(lines), maxRemovedRun+1))
	for i, l := range lines {
		if i == maxRemovedRun {
			// Indented as the run is, so the count does not hold the snippet's shared indentation at none.
			indent := strings.Repeat(" ", indentOf(out[0]))
			return append(out, fmt.Sprintf("%s… %s removed", indent, plural(len(lines)-i, "more line")))
		}
		out = append(out, strings.ReplaceAll(strings.TrimRight(l, "\r"), "\t", strings.Repeat(" ", tabWidth)))
	}
	return out
}

// signs returns what marks a row of the snippet as added ("+"), removed ("-") or neither (" "), by its line number;
// nil when the change is not known, and no row is marked.
func (fc focus) signs() func(n int) string {
	if fc.Added == nil && fc.Removed == nil {
		return nil
	}
	return func(n int) string {
		switch {
		case n == removedRow:
			return "-"
		case fc.Added[n]:
			return "+"
		}
		return " "
	}
}

// snippetLines is the lines of a file as a snippet reads them: without line endings, a tab as tabWidth spaces.
func snippetLines(content string) []string {
	all := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	for i, l := range all {
		all[i] = strings.ReplaceAll(strings.TrimRight(l, "\r"), "\t", strings.Repeat(" ", tabWidth))
	}
	return all
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
//
// When signs is set the change is known: an added line has "+" before it and a removed one "-", each in its own colour
// and on its own background, so the two are told apart at a glance and without colour too.
//
// tail is a line said under the code, of what the card leaves out.
func codeCard(place, file string, marked map[int]bool, nums []int, code []string, numWidth int, signs func(int) string, tail string, st codeStyle) string {
	const margin, pad, beforeCode = "  ", 1, "  "
	width := min(maxCardWidth, max(minCodeWidth, st.width-len(margin)))
	gutter := len("▌") + numWidth + len(beforeCode)
	if signs != nil {
		gutter += len("+ ")
	}
	avail := max(minCodeWidth, width-gutter-pad)
	shown := highlightRows(file, nums, code, st.pal.code)
	row := func(bg, text string) string {
		// Every reset inside the highlighted text would also end the background; carry it across them.
		text = strings.ReplaceAll(text, ansiReset, ansiReset+bg)
		fill := max(0, width-ansi.StringWidth(text))
		return margin + bg + text + strings.Repeat(" ", fill) + ansiReset + "\n"
	}
	var s strings.Builder
	s.WriteString(row(st.pal.cardTitle, st.pal.accent+cutColumns(" "+place, width)+ansiReset))
	for i, n := range nums {
		switch n {
		case gapRow:
			s.WriteString(row(st.pal.card, st.pal.dim+fmt.Sprintf(" %*s ⋮", numWidth, "")+ansiReset))
		default:
			// The finding's lines have an accent bar and number on a brighter background; a line the change added or
			// removed is on the background of its kind instead, and keeps the bar.
			back, lead := st.pal.card, st.pal.dim+" "
			if marked[n] {
				back, lead = st.pal.cardLine, st.pal.accent+"▌"
			}
			num, sign := strings.Repeat(" ", numWidth), ""
			if n > 0 {
				num = fmt.Sprintf("%*d", numWidth, n)
			}
			if signs != nil {
				switch signs(n) {
				case "+":
					back, sign = cmp.Or(st.pal.addedBg, back), st.pal.added+"+"+ansiReset+" "
				case "-":
					back, sign = cmp.Or(st.pal.removedBg, back), st.pal.removed+"-"+ansiReset+" "
				default:
					sign = "  "
				}
			}
			// A line too long for the card goes on under itself, with no number: nothing of it is cut off.
			for k, part := range wrapStyled(shown[i], avail) {
				if k > 0 {
					num = strings.Repeat(" ", numWidth)
					if signs != nil {
						sign = "  "
					}
				}
				s.WriteString(row(back, lead+num+ansiReset+beforeCode+sign+part))
			}
		}
	}
	// What the card leaves out is said whole, over as many rows as it takes: a note cut short hides what it is for.
	if tail != "" {
		for _, part := range wrapWords(tail, max(1, width-1)) {
			s.WriteString(row(st.pal.card, st.pal.dim+" "+part+ansiReset))
		}
	}
	return s.String()
}

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
		if isDeclaration(l) {
			return n
		}
		if block == 0 && strings.HasSuffix(strings.TrimSpace(l), "{") {
			block = n
		}
		target = indentOf(l) // an outer statement; keep looking for what contains it
	}
	return block
}

// isDeclaration reports whether l starts a declaration or block in common languages (Go, Python, JS/TS, Rust, Java,
// Ruby, shell, Terraform and YAML keys): a declaring keyword as a whole word, and not the start of an
// assignment or call that only begins like one ("data := load()", "functionCall()").
func isDeclaration(l string) bool {
	t := strings.TrimSpace(l)
	word := leadingWord.FindString(t)
	// A whole declaration by its shape, whatever its name: "const f = (", "name() {", "key:". A branch of a
	// statement ("else:", "default:") has that shape and is none.
	if declWhole.MatchString(l) {
		return !branchWords[strings.TrimSuffix(t, ":")]
	}
	after, known := declKeywords[word]
	if !known {
		return false
	}
	// The keyword alone is not enough: what follows must be what a declaration has there, a name for most, a quoted
	// label for a Terraform block or a test. "data.foo()", "test()" and "type = 3" are statements.
	return after.MatchString(strings.TrimSpace(t[len(word):]))
}

var (
	branchWords = map[string]bool{"else": true, "try": true, "finally": true, "except": true, "default": true, "do": true}
	leadingWord = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
	declWhole   = regexp.MustCompile(`^\s*(const\s+\w+\s*=\s*(async\s*)?\(|[\w.]+\s*\(\)\s*\{|[\w.-]+:\s*$)`)
	// declKeywords is, for each keyword that starts a declaration, what must follow it.
	declKeywords = func() map[string]*regexp.Regexp {
		named := regexp.MustCompile(`^[\p{L}_*<]`)
		set := map[string]*regexp.Regexp{
			"func":   regexp.MustCompile(`^[\p{L}_(]`), // a name, or a method's receiver
			"locals": regexp.MustCompile(`^\{`),
		}
		// What a module exports is a declaration; "export PATH=/bin" in a shell script is an assignment.
		set["export"] = regexp.MustCompile(`^(default|const|let|var|function|class|async|interface|type|enum|abstract|declare|namespace)\b|^[{*]`)
		for _, w := range strings.Fields("def class function fn pub impl trait interface struct enum type module public private protected static async") {
			set[w] = named
		}
		for _, w := range strings.Fields("resource data variable") {
			set[w] = regexp.MustCompile(`^"`)
		}
		for _, w := range strings.Fields("describe it test") {
			// A test block: a quoted name, and a body that opens on the same line. A plain call has no body.
			set[w] = regexp.MustCompile("^\\(\\s*[\"'`].*(\\{|\\(|=>|\\bdo)$")
		}
		return set
	}()
)

// declarationEnd is the last line of the declaration that starts at line start, and whether that is certain. A body
// in braces ends where its brace closes, however its lines are indented; any other ends by indentation. Its header
// may run over several lines (parameters one a line), which blockEnd alone takes for the end.
func declarationEnd(code []string, literal []bool, start int) (int, bool) {
	if end, ok, braces := braceEnd(code, start); braces {
		return end, ok
	}
	end := indentedEnd(code, start)
	for end < len(code) && literal[end] {
		end++ // a literal the declaration ends with is part of it to its last line
	}
	return end, true
}

// scanLines returns the code of the lines, as codeLines has it, how many brackets are open where each begins, and
// which lines begin inside a comment or string of several lines.
func scanLines(file string, all []string) (code []string, depth []int, literal []bool, ok bool) {
	code, literal, ok = codeLines(file, all)
	depth = make([]int, len(all))
	var scan braceScan
	for i, l := range code {
		depth[i] = scan.depth
		scan.line(l)
	}
	return code, depth, literal, ok
}

// codeLines returns the lines with their comments and strings blanked, as the lexer of the file's language tells
// them apart: what is left is code, whose brackets and indentation say where a declaration ends. literal marks the
// lines that begin inside a comment or string of several lines (a raw string, a here-document, a docstring). ok is false
// for a file in no language the lexers know, which is returned as it is.
func codeLines(file string, all []string) (code []string, literal []bool, ok bool) {
	literal = make([]bool, len(all))
	lexer := lexers.Match(file)
	if lexer == nil {
		return all, literal, false
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, strings.Join(all, "\n")+"\n")
	if err != nil {
		return all, literal, false
	}
	var b strings.Builder
	line := 0
	for _, tok := range it.Tokens() {
		parts := strings.Split(tok.Value, "\n")
		str := tok.Type.InSubCategory(chroma.LiteralString)
		if tok.Type.InCategory(chroma.Comment) || str {
			for j, p := range parts {
				if j == 0 && str {
					// Where a string starts it still stands for a value: a line that holds only a string is not blank.
					parts[j] = strings.Map(func(r rune) rune {
						if unicode.IsSpace(r) {
							return r
						}
						return '"'
					}, p)
					continue
				}
				if j > 0 && line+j < len(all) && (j < len(parts)-1 || p != "") {
					literal[line+j] = true
				}
				parts[j] = strings.Repeat(" ", utf8.RuneCountInString(p))
			}
		}
		b.WriteString(strings.Join(parts, "\n"))
		line += len(parts) - 1
	}
	code = strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(code) != len(all) {
		return all, make([]bool, len(all)), false // the lexer did not keep the lines one for one
	}
	return code, literal, true
}

// unindentedStart is the line of the declaration whose brackets hold line n when n is written at the margin, where
// indentation tells nothing: the line that opens the outermost of them, or the one before a brace on a line of its
// own. It returns 0 when line n is inside no bracket.
func unindentedStart(code []string, depth []int, n int) int {
	if depth[n-1] <= 0 {
		return 0
	}
	for m := n - 1; m >= 1; m-- {
		if depth[m-1] != 0 || strings.TrimSpace(code[m-1]) == "" {
			continue
		}
		for onlyBrace(code[m-1]) && m > 1 {
			m--
		}
		return m
	}
	return 0
}

// braceEnd is the line that closes the body a declaration at line start opens with a brace: at the end of its header
// or on a line of its own after it. braces is false when it has no such body, and ok is false when its brackets do not
// pair up, so where it ends is not known.
func braceEnd(all []string, start int) (end int, ok, braces bool) {
	var scan braceScan
	header := true // still in the header; false once it has ended without a brace
	for n := start; n <= len(all); n++ {
		last := scan.line(all[n-1])
		switch {
		case last == 0 && scan.depth >= 0:
			continue // blank, or all comment
		case scan.depth < 0:
			return 0, false, braces
		case braces:
			if scan.depth == 0 {
				return n, closesDeclaration(all, start, n), true
			}
		case header && scan.outer() == '{':
			braces = true // the body opens in the header, with code after its brace ("f() { step();")
		case last == '{' && (header || scan.code == 1 && indentOf(all[n-1]) == indentOf(all[start-1])):
			if scan.depth == 0 {
				return 0, false, true // the brace closed something the scan did not see open
			}
			braces = true
		case !header:
			return 0, false, false
		case scan.depth == 0:
			header = false // the next line of code says whether a brace on a line of its own follows
		}
	}
	return 0, false, braces
}

// closesDeclaration reports whether line end reads as the close of the declaration at line start: a closing brace at
// its indentation, with nothing deeper straight after it. Where the scan was misled by a literal it does not know (a
// regular expression, a quote of another language), the line it stopped at does not read so.
func closesDeclaration(all []string, start, end int) bool {
	indent := indentOf(all[start-1])
	if !strings.HasPrefix(strings.TrimSpace(all[end-1]), "}") || indentOf(all[end-1]) != indent {
		return false
	}
	for n := end + 1; n <= len(all); n++ {
		if strings.TrimSpace(all[n-1]) != "" {
			return indentOf(all[n-1]) <= indent
		}
	}
	return true
}

// braceScan counts the brackets left open over lines of code.
type braceScan struct {
	depth int
	code  int    // how many characters of code the last line had
	open  []rune // the brackets left open, outermost first
}

// outer is the outermost bracket left open, 0 when none is.
func (b *braceScan) outer() rune {
	if len(b.open) == 0 {
		return 0
	}
	return b.open[0]
}

// line reads one line and returns the last character of its code, 0 when it has none.
func (b *braceScan) line(s string) rune {
	var last rune
	b.code = 0
	for _, c := range s {
		switch {
		case strings.ContainsRune("([{", c):
			b.depth++
			b.open = append(b.open, c)
		case strings.ContainsRune(")]}", c):
			b.depth--
			if len(b.open) > 0 {
				b.open = b.open[:len(b.open)-1]
			}
		}
		if !unicode.IsSpace(c) {
			last = c
			b.code++
		}
	}
	return last
}

// onlyBrace reports whether a line is an opening brace alone, a comment after it aside.
func onlyBrace(l string) bool {
	return strings.TrimSpace(trailingComment.ReplaceAllString(l, "")) == "{"
}

// indentedEnd is the last line of a declaration whose body is told by indentation.
func indentedEnd(all []string, start int) int {
	end := blockEnd(all, start)
	for {
		next := end
		t := strings.TrimSpace(all[end-1])
		switch {
		case end > start && opensBlock(t, "{", ":", "("):
			next = blockEnd(all, end) // the header's closing line opens the body
		default:
			for n := end + 1; n <= len(all); n++ {
				if b := strings.TrimSpace(all[n-1]); b != "" {
					if onlyBrace(b) && indentOf(all[n-1]) == indentOf(all[start-1]) {
						next = blockEnd(all, n) // the brace on its own line
					}
					break
				}
			}
		}
		if next <= end {
			return end
		}
		end = next
	}
}

// addedDeclaration returns the first and last line of the declaration (function, method, type, resource) that lines
// lo to hi sit in, or that starts at lo, when the change adds every line of it and it has at most maxAddedLines
// lines: the outermost such one, so a new method's new class when that fits and the method alone when it does not.
// It returns 0, 0 when there is none, or when what the change adds is not known.
func addedDeclaration(file string, all []string, lo, hi int, added map[int]bool) (from, to int) {
	if len(added) == 0 {
		return 0, 0
	}
	// What a comment or string holds is text, not code: its brackets and indentation say nothing of the code around
	// it. A declaration is still told by its line as written, a quoted name and all.
	src := all
	all, depth, literal, ok := scanLines(file, src)
	if !ok {
		return 0, 0 // in a language unknown a string is not told from code, nor where a declaration ends
	}
	declared := func(n int) bool { return strings.TrimSpace(all[n-1]) != "" && isDeclaration(src[n-1]) }
	var starts []int // innermost first
	label := marginLabel(all, lo)
	if !label && declared(lo) {
		starts = append(starts, lo)
	}
	if n := unindentedStart(all, depth, lo); n > 0 && n < lo && indentOf(all[lo-1]) == 0 && declared(n) {
		starts = append(starts, n)
	}
	target := indentOf(all[lo-1])
	switch t := strings.TrimSpace(all[lo-1]); {
	case t == "":
		target = math.MaxInt
	case closesBlock(t) || onlyBrace(t) || label:
		target++ // a declaration's own closing line, or its brace: it starts at this indentation, further up
	}
	for n := lo - 1; n >= 1 && target > 0; n-- {
		l := all[n-1]
		if strings.TrimSpace(l) == "" || indentOf(l) >= target {
			continue
		}
		target = indentOf(l)
		if t := strings.TrimSpace(l); closesBlock(t) || onlyBrace(t) || marginLabel(all, n) {
			// The closing line of a header written over several lines (") error {"), or a brace on a line of its
			// own: the declaration starts at the same indentation, further up.
			target++
			continue
		}
		if declared(n) {
			starts = append(starts, n)
		}
	}
	for i := len(starts) - 1; i >= 0; i-- {
		start := starts[i]
		end, sure := declarationEnd(all, literal, start)
		if !sure || end < hi || end-start+1 > maxAddedLines {
			continue
		}
		whole := true
		for n := start; n <= end && whole; n++ {
			whole = strings.TrimSpace(src[n-1]) == "" || added[n]
		}
		if whole {
			return start, end
		}
	}
	return 0, 0
}

// addedValue returns the first and last line of the constant or variable that lines lo to hi sit in when its value
// runs over several lines in brackets (a list, array, slice, map, dictionary, tuple or other collection, or a block
// of constants), the change adds every line of it and it has at most maxAddedLines lines: the outermost such one. It
// returns 0, 0 when there is none, or when what the change adds is not known.
func addedValue(file string, all []string, lo, hi int, added map[int]bool) (from, to int) {
	if len(added) == 0 {
		return 0, 0
	}
	code, _, _, ok := scanLines(file, all)
	if !ok {
		return 0, 0
	}
	for n := lo; n >= 1 && hi-n < maxAddedLines; n-- {
		if !valueStart(code[n-1]) {
			continue
		}
		end, sure := valueEnd(code, n)
		if !sure || end < hi || end-n+1 > maxAddedLines {
			continue
		}
		whole := true
		for k := n; k <= end && whole; k++ {
			whole = strings.TrimSpace(all[k-1]) == "" || added[k]
		}
		if whole {
			from, to = n, end
		}
	}
	return from, to
}

// valueBlock matches the opening of a block of constants or variables, as Go writes them.
var valueBlock = regexp.MustCompile(`^(var|const)\s*\($`)

// notValues are the words that start a statement which may hold an assignment and is no declaration of a value.
var notValues = map[string]bool{
	"if": true, "elif": true, "else": true, "for": true, "while": true, "switch": true, "case": true, "return": true,
	"match": true, "when": true, "unless": true, "until": true, "with": true, "yield": true, "await": true,
}

// valueStart reports whether a line of code, its comments and strings blanked, starts a constant or variable whose
// value goes on past the line: it assigns, outside any bracket, and ends with a bracket still open.
func valueStart(code string) bool {
	t := strings.TrimSpace(code)
	if valueBlock.MatchString(t) {
		return true
	}
	if notValues[leadingWord.FindString(t)] {
		return false
	}
	r := []rune(t)
	depth, assigned := 0, false
	for i := 0; i < len(r); i++ {
		switch c := r[i]; {
		case strings.ContainsRune("([{", c):
			depth++
		case strings.ContainsRune(")]}", c):
			depth--
		case c == '=' && depth == 0:
			if i+1 < len(r) && (r[i+1] == '=' || r[i+1] == '>') {
				i++ // a comparison or an arrow
				continue
			}
			// "x = [" and "x := [" declare; "x += [" and "x <= (" do not.
			if i == 0 || !strings.ContainsRune("=!<>+-*/%&|^~", r[i-1]) {
				assigned = true
			}
		}
	}
	return assigned && depth > 0
}

// valueEnd is the line where the brackets a value opens at line start are closed again, and whether they are.
func valueEnd(code []string, start int) (int, bool) {
	var scan braceScan
	for n := start; n <= len(code); n++ {
		scan.line(code[n-1])
		switch {
		case scan.depth < 0:
			return 0, false
		case scan.depth == 0:
			return n, true
		}
	}
	return 0, false
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
		if isDeclaration(l) {
			break
		}
	}
	return from, to
}

// blockEnd is the last line of the block that opens at line start: the last line indented deeper than it, or the
// closing line at its own indentation (a brace, bracket, end, fi or done) that follows them.
func blockEnd(all []string, start int) int {
	indent, last := indentOf(all[start-1]), start
	// In a block that opens with a brace, a label ("retry:") is written at the margin and is still inside it.
	braces := opensBlock(all[start-1], "{")
	for n := start + 1; n <= len(all); n++ {
		t := strings.TrimSpace(all[n-1])
		if t == "" || (braces && codeLabel.MatchString(t)) {
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

// marginLabel reports whether line n is a label written at the margin inside a block with braces ("retry:" in a Go
// function), not a key that heads a block of its own: what follows it ends at a closing brace no deeper than it.
func marginLabel(all []string, n int) bool {
	if !codeLabel.MatchString(strings.TrimSpace(all[n-1])) {
		return false
	}
	for m := n + 1; m <= len(all); m++ {
		t := strings.TrimSpace(all[m-1])
		if t == "" || indentOf(all[m-1]) > indentOf(all[n-1]) || codeLabel.MatchString(t) {
			continue
		}
		return strings.HasPrefix(t, "}")
	}
	return false
}

// codeLabel matches a label: a name, in any script, and a colon, alone or with a statement or comment after it.
var codeLabel = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_]*:($|\s)`)

// trailingComment matches a comment that ends a line of code.
var trailingComment = regexp.MustCompile(`\s*(//.*|/\*.*\*/)\s*$`)

// opensBlock reports whether the line t ends by opening a block, a comment after it aside.
func opensBlock(t string, openers ...string) bool {
	t = strings.TrimSpace(trailingComment.ReplaceAllString(t, ""))
	for _, o := range openers {
		if strings.HasSuffix(t, o) {
			return true
		}
	}
	return false
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

// wrapColumns splits s into pieces of at most width terminal columns, so a long line of code is shown whole over
// several rows. It counts what the terminal draws: a wide character takes two columns, and a character with its
// combining marks is never split. There is always at least one piece.
func wrapColumns(s string, width int) []string {
	width = max(1, width)
	var out []string
	var cur strings.Builder
	n, state := 0, -1
	for len(s) > 0 {
		var cluster string
		var w int
		cluster, s, w, state = uniseg.FirstGraphemeClusterInString(s, state)
		if n > 0 && n+w > width {
			out = append(out, cur.String())
			cur.Reset()
			n = 0
		}
		cur.WriteString(cluster)
		n += w
	}
	return append(out, cur.String())
}

var sgr = regexp.MustCompile(`^\x1b\[[0-9;]*m`)

// wrapStyled is wrapColumns for highlighted text: colour escapes take no room, each piece ends with a reset, and the
// colours in force where a piece ends start the next one.
func wrapStyled(s string, width int) []string {
	width = max(1, width)
	var out, active []string
	var cur strings.Builder
	n, state := 0, -1
	for len(s) > 0 {
		if esc := sgr.FindString(s); esc != "" {
			if esc == ansiReset || esc == "\x1b[m" {
				active = nil
			} else {
				active = append(active, esc)
			}
			cur.WriteString(esc)
			s, state = s[len(esc):], -1
			continue
		}
		var cluster string
		var w int
		cluster, s, w, state = uniseg.FirstGraphemeClusterInString(s, state)
		if n > 0 && n+w > width {
			out = append(out, cur.String()+ansiReset)
			cur.Reset()
			cur.WriteString(strings.Join(active, ""))
			n = 0
		}
		cur.WriteString(cluster)
		n += w
	}
	return append(out, cur.String())
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

// highlightRows highlights a snippet's rows, the file's own lines as one text and each run of removed lines as
// another: a removed line is no part of the code now, and one that opens a string or comment it does not close (a
// run cut short, or a removal that the kept lines closed) must not colour the lines after it.
func highlightRows(file string, nums []int, code []string, style *chroma.Style) []string {
	out := make([]string, len(code))
	var kept []int
	for i := 0; i < len(nums); i++ {
		if nums[i] != removedRow {
			kept = append(kept, i)
			continue
		}
		end := i
		for end < len(nums) && nums[end] == removedRow {
			end++
		}
		copy(out[i:end], highlight(file, code[i:end], style))
		i = end - 1
	}
	lines := make([]string, len(kept))
	for k, i := range kept {
		lines[k] = code[i]
	}
	for k, l := range highlight(file, lines, style) {
		out[kept[k]] = l
	}
	return out
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
