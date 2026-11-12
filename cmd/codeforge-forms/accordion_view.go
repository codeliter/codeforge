package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/codeliter/codeforge/src/forms"
)

// span is where a note's lines are in the view.
type span struct{ start, end int }

// layout is the view laid out: its lines, where each section's row is, and where each note of an open section is.
type layout struct {
	lines []string
	rows  []int
	notes [][]span
}

func (m *accordion) layout() layout {
	var lay layout
	add := func(text string) {
		if text != "" {
			lay.lines = append(lay.lines, strings.Split(strings.TrimRight(text, "\n"), "\n")...)
			lay.lines = append(lay.lines, "")
		}
	}
	add(m.a.Intro)
	lay.rows = make([]int, len(m.a.Items))
	lay.notes = make([][]span, len(m.a.Items))
	for i, s := range m.a.Items {
		lay.rows[i] = len(lay.lines)
		pointer, arrow := "  ", "▸"
		if i == m.cursor && m.sub < 0 {
			pointer = m.a.Accent + "›" + reset + " "
			if m.a.Edits {
				pointer = m.a.Accent + "▌" + reset + " " // the same bar as on a note, so there is one cursor to look for
			}
		}
		if m.open[i] {
			arrow = "▾"
		}
		row := pointer + m.a.Dim + arrow + reset + " " + s.Title
		if s.Summary != "" {
			row += "  " + m.a.Dim + s.Summary + reset
		}
		if n := m.sectionChanges(i); m.a.Edits && n > 0 {
			row += "  " + m.a.Warn + "· " + countChanges(n) + reset // seen even while the section is closed
		}
		lay.lines = append(lay.lines, m.cut(row))
		if c := m.find(forms.ChangeAsk, "", ansi.Strip(s.Title)); m.a.Edits && c >= 0 {
			lay.lines = append(lay.lines, m.cut(bodyIndent+m.mark("?", "About "+ansi.Strip(s.Title)+": "+m.changes[c].Text, "asked")))
		}
		if !m.open[i] {
			continue
		}
		body := strings.TrimSpace(s.Body) != ""
		if body {
			for _, l := range strings.Split(strings.TrimRight(s.Body, "\n"), "\n") {
				lay.lines = append(lay.lines, m.cut(bodyIndent+l))
			}
		}
		for j, n := range s.Notes {
			if body || j > 0 {
				lay.lines = append(lay.lines, "")
			}
			start := len(lay.lines)
			bar := bodyIndent
			if m.a.Edits && i == m.cursor && j == m.sub {
				bar = "  " + m.a.Accent + "▌" + reset + " "
			}
			for _, l := range m.noteLines(n) {
				lay.lines = append(lay.lines, m.cut(bar+l))
			}
			lay.notes[i] = append(lay.notes[i], span{start, len(lay.lines)})
		}
		if body || len(s.Notes) > 0 {
			lay.lines = append(lay.lines, "")
		}
	}
	if m.a.Edits {
		if c := m.find(forms.ChangeAsk, "", ""); c >= 0 {
			lay.lines = append(lay.lines, "", m.cut("  "+m.mark("/", "About the whole review: "+m.changes[c].Text, "asked")))
		}
	}
	if m.a.Outro != "" {
		lay.lines = append(lay.lines, "")
		add(m.a.Outro)
	}
	return lay
}

// noteLines is a note as shown: its body, or one struck-out line when it is marked for deletion; a rewritten note
// shows its body faded, as what is replaced, with the rewrite under it; then the user's question about it.
func (m *accordion) noteLines(n forms.Note) []string {
	if m.deleted(n.ID) {
		first := n.Raw
		if first == "" {
			first, _, _ = strings.Cut(strings.TrimSpace(ansi.Strip(n.Body)), "\n")
		}
		// The sign says it as well, for terminals that draw no strikethrough.
		return []string{m.a.Warn + "✗ " + reset + m.a.Dim + strike + first + reset + "  " + m.tag("deleted by you")}
	}
	lines := strings.Split(strings.TrimRight(n.Body, "\n"), "\n")
	if c := m.find(forms.ChangeEdit, n.ID, ""); c >= 0 {
		for k, l := range lines {
			lines[k] = m.a.Dim + ansi.Strip(l) + reset
		}
		// A rewrite of several lines shows each of them; the tag goes on the last.
		text := strings.Split(m.changes[c].Text, "\n")
		for k, l := range text {
			sign := "→"
			if k > 0 {
				sign = " "
			}
			if k == len(text)-1 {
				lines = append(lines, m.mark(sign, l, "edited by you"))
				continue
			}
			lines = append(lines, m.a.Warn+sign+" "+l+reset)
		}
	}
	if c := m.find(forms.ChangeAsk, n.ID, ""); c >= 0 {
		lines = append(lines, m.mark("?", m.changes[c].Text, "asked"))
	}
	return lines
}

// mark is a change the user made, shown where it applies: a sign, the text and a tag.
func (m *accordion) mark(sign, text, tag string) string {
	return m.a.Warn + sign + " " + text + reset + "  " + m.tag(tag)
}

func (m *accordion) tag(text string) string { return m.a.Warn + reverse + " " + text + " " + reset }

func (m *accordion) cut(s string) string { return ansi.Truncate(s, m.width, "…") }

// follow scrolls so the cursor's row, or the note under it, is on screen and the view never scrolls past its end.
func (m *accordion) follow() {
	lay := m.layout()
	visible := m.visible()
	if len(lay.rows) > 0 {
		top, bottom := lay.rows[m.cursor], lay.rows[m.cursor]
		if m.sub >= 0 && m.sub < len(lay.notes[m.cursor]) {
			n := lay.notes[m.cursor][m.sub]
			top, bottom = n.start, max(n.start, n.end-1)
		}
		if bottom >= m.offset+visible {
			m.offset = bottom - visible + 1
		}
		if top < m.offset {
			m.offset = top
		}
	}
	m.offset = max(0, min(m.offset, len(lay.lines)-visible))
}

// key is one key and what it does in the footer: the key stands out, the words are quiet.
func (m *accordion) keyHelp(k, label string) string {
	return m.a.Accent + k + reset + " " + m.a.Dim + label + reset
}

// joinHelp joins footer entries with a quiet separator.
func (m *accordion) joinHelp(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), m.a.Dim+"  ·  "+reset)
}

// boxPadding is the room the input box takes around the text: a border and a space on each side, and one cell for the
// cursor past the end.
const (
	boxPadding = 5
	boxSides   = 2 // the two border cells of a line, or the two spaces inside them
)

// inputBox is the box the user types in, titled with what the text is for, so it is plain where to type.
func (m *accordion) inputBox() []string {
	inner := max(boxPadding, m.width-boxSides)
	title := " " + strings.TrimSuffix(m.inputPrompt(), ": ") + " "
	if ansi.StringWidth(title) > inner-boxSides {
		title = ansi.Truncate(title, inner-boxSides, "… ")
	}
	bodies := []string{m.input.View()}
	if m.multiline {
		bodies = strings.Split(strings.TrimRight(m.area.View(), "\n"), "\n")
	}
	edge := func(s string) string { return m.a.Accent + s + reset }
	box := []string{edge("┌─") + m.a.Accent + title + reset + edge(strings.Repeat("─", max(0, inner-1-ansi.StringWidth(title)))+"┐")}
	for _, body := range bodies {
		box = append(box, edge("│")+" "+body+strings.Repeat(" ", max(0, inner-boxSides-ansi.StringWidth(body)))+" "+edge("│"))
	}
	return append(box, edge("└"+strings.Repeat("─", inner)+"┘"))
}

// inputPrompt is the words before the input while the user types.
func (m *accordion) inputPrompt() string {
	switch {
	case m.pending.Kind == forms.ChangeEdit && m.multiline:
		n, _ := m.noteByID(m.pending.Note)
		return "✎ Rewrite " + cmpOr(n.Label, "this note") + ": "
	case m.pending.Kind == forms.ChangeEdit:
		n, _ := m.noteByID(m.pending.Note)
		return "✎ Rewrite " + cmpOr(n.Label, "this finding") + " (its first line): "
	case m.pending.Note != "":
		n, _ := m.noteByID(m.pending.Note)
		return "? Ask about " + cmpOr(n.Label, "this finding") + ": "
	case m.pending.Section != "":
		return "? Ask about " + m.pending.Section + ": "
	}
	return "/ Ask about the whole review: "
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// footer is the help under the view. Without Edits it is one line of keys. With Edits it is one short line: what the
// user can do on the row under the cursor, the changes waiting to be sent, and p for the ways to finish, which take
// the line's place until one is picked. A notice goes on a line of its own above it.
func (m *accordion) footer(lay layout) []string {
	if !m.a.Edits {
		return []string{m.plainHelp(lay)}
	}
	if m.typing {
		enter := "add"
		if m.pending.Kind == forms.ChangeEdit || m.find(m.pending.Kind, m.pending.Note, m.pending.Section) >= 0 {
			enter = "save"
		}
		if m.multiline {
			return append(m.inputBox(), m.joinHelp(m.keyHelp("ctrl+s", "save"), m.keyHelp("⏎", "new line"), m.keyHelp("esc", "cancel")))
		}
		return append(m.inputBox(), m.joinHelp(m.keyHelp("⏎", enter), m.keyHelp("esc", "cancel")))
	}
	var lines []string
	if m.notice != "" {
		lines = append(lines, m.a.Warn+m.notice+reset)
	}
	if m.picking {
		return append(lines, m.pickLines()...)
	}
	var parts []string
	// Sending is what the user marked changes for, so it leads.
	if n := len(m.changes); n > 0 {
		parts = append(parts, m.a.Accent+"s"+reset+" "+m.a.Warn+"send "+countChanges(n)+reset, m.keyHelp("u", "undo"))
	}
	note, onNote := m.note()
	switch {
	case onNote && m.deleted(note.ID):
		parts = append(parts, m.keyHelp("x", "keep"), m.keyHelp("?", "ask"))
	case onNote && note.Raw != "":
		parts = append(parts, m.keyHelp("e", "edit"), m.keyHelp("x", "delete"), m.keyHelp("?", "ask"))
	case onNote:
		parts = append(parts, m.keyHelp("x", "delete"), m.keyHelp("?", "ask"))
	case len(m.a.Items) > 0 && m.open[m.cursor]:
		parts = append(parts, m.keyHelp("⏎", "close"), m.keyHelp("?", "ask"))
	case len(m.a.Items) > 0:
		parts = append(parts, m.keyHelp("⏎", "open"), m.keyHelp("?", "ask"))
	}
	// A question about the whole review can be asked from any row.
	parts = append(parts, m.keyHelp("/", "ask about all"))
	for _, act := range m.a.Keys {
		if act.Footer {
			parts = append(parts, m.keyHelp(act.Key, act.Label))
		}
	}
	if len(m.a.Keys) > 0 {
		parts = append(parts, m.keyHelp("p", "post"))
	}
	parts = append(parts, m.keyHelp("y", "copy"), m.keyHelp("m", m.mouseHelp()), m.keyHelp("q", "quit"), m.scrollPosition(lay))
	return append(lines, m.joinHelp(parts...))
}

// pickLines lists the ways to finish, one per line so none is ever cut off at the edge, under the heads of their
// groups. What happens to the marked changes comes first: rewrites and deletions go up with the pick, questions not
// sent are dropped.
func (m *accordion) pickLines() []string {
	var lines []string
	if n := len(m.changes) - m.questions(); n > 0 {
		if slices.ContainsFunc(m.a.Keys, func(a forms.Action) bool { return a.Keeps }) {
			lines = append(lines, m.a.Accent+countChanges(n)+" of yours go up with the review when you post it; any other pick drops them."+reset)
		} else {
			// Nothing here posts: every pick drops them, so the list says so and offers to send them instead.
			lines = append(lines, m.a.Warn+countChanges(n)+" of yours: picking below drops them."+reset+"  "+m.keyHelp("s", "send them first"))
		}
	}
	if n := m.questions(); n > 0 {
		lines = append(lines, m.a.Warn+countQuestions(n)+" not sent: picking below drops them."+reset+"  "+m.keyHelp("s", "send them first"))
	}
	group := ""
	for k, act := range m.a.Keys {
		g := cmpOr(act.Group, "Post as")
		if k == 0 || g != group {
			lines = append(lines, m.a.Accent+g+reset)
			group = g
		}
		label := strings.TrimPrefix(act.Label, "post as ")
		if g != "Post as" && label != "" {
			label = strings.ToUpper(label[:1]) + label[1:]
		}
		lines = append(lines, "  "+m.keyHelp(act.Key, label))
	}
	return append(lines, "  "+m.keyHelp("esc", "back"))
}

// scrollPosition is how far down the view is, when it is longer than the screen.
func (m *accordion) scrollPosition(lay layout) string {
	visible := max(1, m.height-m.footerHeight()-1)
	if len(lay.lines) <= visible {
		return ""
	}
	end := min(len(lay.lines), m.offset+visible)
	return m.a.Dim + fmt.Sprintf("%d%%", min(percent, percent*end/len(lay.lines))) + reset
}

// footerHeight is as many lines as the footer has, without laying the view out again: one, plus one for the input
// while typing or for a notice; while picking, a title, a line per way to finish and one for esc.
func (m *accordion) footerHeight() int {
	if !m.a.Edits {
		return 1
	}
	n := 1
	switch {
	case m.typing:
		n += len(m.inputBox())
	case m.notice != "":
		n++
	}
	if m.picking {
		n += len(m.pickLines()) - 1 // the list takes the key line's place
	}
	return n
}

// plainHelp is the one-line help of an accordion without Edits.
func (m *accordion) plainHelp(lay layout) string {
	visible := max(1, m.height-m.footerHeight()-1)
	var keys []string
	if len(lay.lines) > visible {
		end := min(len(lay.lines), m.offset+visible)
		keys = append(keys, fmt.Sprintf("%d%%", min(percent, percent*end/len(lay.lines))))
	}
	// The keys that matter most come first, so a narrow terminal cuts the least useful ones.
	for _, act := range m.a.Keys {
		keys = append(keys, act.Key+" "+act.Label)
	}
	keys = append(keys, "click or ⏎ open", "↑↓ move", "a all", "y copy", "m "+m.mouseHelp(), "q quit", "wheel or pgup/pgdn scroll")
	return m.a.Dim + strings.Join(keys, " · ") + reset
}
