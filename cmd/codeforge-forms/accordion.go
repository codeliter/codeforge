package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"

	"github.com/codeliter/codeforge/src/forms"
)

const (
	reset      = "\x1b[0m"
	strike     = "\x1b[9m"
	reverse    = "\x1b[7m"
	bodyIndent = "    "
	minHeight  = 3
	// The size assumed until the terminal reports its own.
	startWidth, startHeight = 80, 24
	percent                 = 100
	wheelLines              = 3 // lines one wheel notch scrolls
	inputLimit              = 2000
	// A rewrite of several lines: how much it may hold, and the box's height while it is typed.
	areaLimitFactor = 4
	areaMinLines    = 3
	areaMaxLines    = 10
	areaSpareLines  = 2 // room to add lines below the text
)

// accordion is the view of a forms.Accordion: a cursor over the sections, which are open or closed, scrolled so the
// cursor's row stays on screen. With Edits the cursor also stops on each note of an open section, and the user marks
// changes there that are sent all at once.
type accordion struct {
	a       forms.Accordion
	open    []bool
	cursor  int
	sub     int // the note under the cursor in the cursor's section, -1 for the section's own row
	offset  int
	width   int
	height  int
	pressed string // the Action key that closed the view
	aborted bool

	changes []forms.Change
	// While typing: the input, and what it is for.
	typing  bool
	input   textinput.Model
	pending forms.Change
	// multiline is set while rewriting a note of several lines, such as the final comment: it is typed in area, where
	// Enter starts a new line and ctrl+s keeps the text.
	multiline bool
	area      textarea.Model
	// notice is a line that asks for attention, and armed the key that, pressed again, goes ahead past it.
	notice string
	armed  string
	// picking is true while the footer offers the ways to finish (p), so the rest of the time it stays one short line.
	picking bool
}

func newAccordion(a forms.Accordion) *accordion {
	m := &accordion{a: a, open: make([]bool, len(a.Items)), width: startWidth, height: startHeight, sub: -1}
	for i := range m.open {
		m.open[i] = a.Open || a.Items[i].Open
	}
	if m.a.Dim == "" {
		m.a.Dim = "\x1b[2m"
	}
	if m.a.Accent == "" {
		m.a.Accent = "\x1b[1m"
	}
	if m.a.Warn == "" {
		m.a.Warn = m.a.Accent
	}
	if a.Edits {
		m.selectFirstNote()
	}
	m.input = textinput.New()
	m.input.Prompt = ""
	m.input.CharLimit = inputLimit
	m.area = textarea.New()
	m.area.Prompt = ""
	m.area.ShowLineNumbers = false
	m.area.CharLimit = inputLimit * areaLimitFactor
	return m
}

// selectFirstNote puts the cursor on the first note of the first open section that has notes, opening the first such
// section when none is open, so the keys that act on a note work from the first press.
func (m *accordion) selectFirstNote() {
	first := -1
	for i, s := range m.a.Items {
		if len(s.Notes) == 0 {
			continue
		}
		if m.open[i] {
			m.cursor, m.sub = i, 0
			return
		}
		if first < 0 {
			first = i
		}
	}
	if first >= 0 {
		m.open[first] = true
		m.cursor, m.sub = first, 0
	}
}

func (*accordion) Init() tea.Cmd { return nil }

func (m *accordion) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, max(minHeight, msg.Height)
		m.input.Width = max(1, m.width-boxPadding)
		m.area.SetWidth(max(1, m.width-boxPadding))
		m.follow()
	case tea.KeyMsg:
		if m.typing {
			return m, m.typeKey(msg)
		}
		return m, m.key(msg.String())
	case tea.MouseMsg:
		if !m.typing {
			m.mouse(msg)
		}
	}
	return m, nil
}

// mouse picks the row or note that was clicked, opening or closing a clicked section, and scrolls with the wheel.
func (m *accordion) mouse(msg tea.MouseMsg) {
	if msg.Action != tea.MouseActionPress {
		return
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.scroll(-wheelLines)
	case tea.MouseButtonWheelDown:
		m.scroll(wheelLines)
	case tea.MouseButtonLeft:
		if msg.Y < 0 || msg.Y >= m.visible() {
			return
		}
		lay := m.layout()
		line := m.offset + msg.Y
		for i, r := range lay.rows {
			if r == line {
				m.cursor, m.sub = i, -1
				m.open[i] = !m.open[i]
				m.follow()
				return
			}
		}
		if !m.a.Edits {
			return
		}
		for i, notes := range lay.notes {
			for j, n := range notes {
				if line >= n.start && line < n.end {
					m.cursor, m.sub = i, j
					m.follow()
					return
				}
			}
		}
	default:
	}
}

// visible is how many lines of the view fit above the footer. It counts the footer's lines without laying the view
// out, so a key press lays it out once, not once per question about the screen.
func (m *accordion) visible() int { return max(1, m.height-m.footerHeight()-1) }

// scroll moves the view by n lines without moving the cursor, so reading down a long section never snaps back to it.
func (m *accordion) scroll(n int) {
	lay := m.layout()
	m.offset = max(0, min(m.offset+n, len(lay.lines)-m.visible()))
}

// key acts on one key press and returns tea.Quit when the view is done.
func (m *accordion) key(k string) tea.Cmd {
	page := max(1, m.visible()-1)
	switch k {
	case "pgdown", "ctrl+d", "f":
		m.scroll(page)
		return nil
	case "pgup", "ctrl+u", "b":
		m.scroll(-page)
		return nil
	case "ctrl+c":
		m.aborted = true
		return tea.Quit
	}
	// A notice lasts one key: pressing its key again goes ahead, anything else puts it away.
	armed := m.armed
	m.notice, m.armed = "", ""
	// While picking, s still sends (the notice offers it); esc, p or q go back; anything else is a way to finish or ignored.
	if m.picking && k != forms.KeySend {
		m.picking = k != "esc" && k != "p" && k != "q"
		if !m.picking {
			return nil
		}
		// The list already says what is unsent, so a pick goes ahead.
		cmd, _ := m.finish(k, armed, true)
		return cmd
	}
	m.picking = false
	if m.a.Edits {
		if done, cmd := m.editKey(k); done {
			return cmd
		}
	}
	switch k {
	case "q", "esc":
		if m.unsavedOnQuit(k, armed) {
			return nil
		}
		return tea.Quit
	case "up", "k", "shift+tab":
		m.move(-1)
	case "down", "j", "tab":
		m.move(1)
	case "enter", " ", "right", "left", "l", "h":
		if len(m.open) > 0 {
			m.open[m.cursor] = k == "right" || k == "l" || (k != "left" && k != "h" && !m.open[m.cursor])
			if !m.open[m.cursor] {
				m.sub = -1
			}
		}
	case "a":
		all := true
		for _, o := range m.open {
			all = all && o
		}
		for i := range m.open {
			m.open[i] = !all
		}
		if all {
			m.sub = -1
		}
	case "home", "g":
		m.cursor, m.sub, m.offset = 0, -1, 0
	case "end", "G":
		m.cursor, m.sub = max(0, len(m.a.Items)-1), -1
	default:
		cmd, _ := m.finish(k, armed, false)
		return cmd
	}
	m.follow()
	return nil
}

// finish closes the view when k is one of the Action keys, unless questions are waiting (unsent) and the user has not
// been told yet (warned); ok reports a match. Rewrites and deletions go with the key pressed, to be applied to what is
// posted: they need no reviewer, so they are never sent first. Unsent questions are dropped.
func (m *accordion) finish(k, armed string, warned bool) (cmd tea.Cmd, ok bool) {
	for _, act := range m.a.Keys {
		if act.Key == "" || k != act.Key {
			continue
		}
		if !act.Keeps {
			// A key that posts nothing takes no change with it: any change waits, as on leaving.
			if !warned && m.unsavedOnQuit(k, armed) {
				return nil, true
			}
			m.pressed, m.changes = act.Key, nil
			return tea.Quit, true
		}
		if !warned && m.unsent(k, armed) {
			return nil, true
		}
		m.pressed = act.Key
		m.changes = slices.DeleteFunc(m.changes, func(c forms.Change) bool { return c.Kind == forms.ChangeAsk })
		return tea.Quit, true
	}
	return nil, false
}

// unsent reports whether key k must wait because questions are marked and not sent: the first press explains, a
// second press of the same key (armed) goes ahead without them. Rewrites and deletions never wait: they go with k.
func (m *accordion) unsent(k, armed string) bool {
	n := m.questions()
	if n == 0 || armed == k {
		return false
	}
	m.notice = fmt.Sprintf("%s not sent yet. Press s to send, or %s again to go on without.", countQuestions(n), k)
	m.armed = k
	return true
}

// unsavedOnQuit is unsent for leaving without posting: then rewrites and deletions are lost too, so any change waits.
func (m *accordion) unsavedOnQuit(k, armed string) bool {
	if len(m.changes) == 0 || armed == k {
		return false
	}
	m.notice = fmt.Sprintf("%s not posted or sent. Press s to send, or %s again to go on without them.", countChanges(len(m.changes)), k)
	m.armed = k
	return true
}

// questions counts the questions marked.
func (m *accordion) questions() int {
	n := 0
	for _, c := range m.changes {
		if c.Kind == forms.ChangeAsk {
			n++
		}
	}
	return n
}

func countQuestions(n int) string {
	if n == 1 {
		return "1 question"
	}
	return fmt.Sprintf("%d questions", n)
}

func countChanges(n int) string {
	if n == 1 {
		return "1 change"
	}
	return fmt.Sprintf("%d changes", n)
}

// editKey handles the keys of an accordion with Edits; done reports whether k was one of them.
func (m *accordion) editKey(k string) (done bool, cmd tea.Cmd) {
	note, onNote := m.note()
	switch k {
	case "e":
		switch {
		case !onNote:
			m.notice = "Move to a finding first (↓) to rewrite it."
		case note.Raw == "":
			m.notice = "This one cannot be rewritten; you can delete it or ask about it."
		case m.deleted(note.ID):
			m.notice = "This one is marked for deletion; press x to keep it."
		default:
			text := note.Raw
			if c := m.find(forms.ChangeEdit, note.ID, ""); c >= 0 {
				text = m.changes[c].Text
			}
			m.startTyping(forms.Change{Kind: forms.ChangeEdit, Note: note.ID}, text)
		}
	case "x":
		if !onNote {
			m.notice = "Move to a finding first (↓) to delete it."
			break
		}
		if c := m.find(forms.ChangeDelete, note.ID, ""); c >= 0 {
			m.changes = slices.Delete(m.changes, c, c+1) // x again keeps it
			break
		}
		// A rewrite stays marked under the deletion (which wins when sent), so x again or u brings it back, not the original.
		m.changes = append(m.changes, forms.Change{Kind: forms.ChangeDelete, Note: note.ID})
	case "?":
		target := forms.Change{Kind: forms.ChangeAsk}
		if onNote {
			target.Note = note.ID
		} else if len(m.a.Items) > 0 {
			target.Section = ansi.Strip(m.a.Items[m.cursor].Title)
		}
		m.startTyping(target, m.askedText(target))
	case "/":
		target := forms.Change{Kind: forms.ChangeAsk}
		m.startTyping(target, m.askedText(target))
	case "u":
		if len(m.changes) == 0 {
			m.notice = "Nothing to undo."
			break
		}
		m.changes = m.changes[:len(m.changes)-1]
	case "p":
		if len(m.a.Keys) == 0 {
			return false, nil
		}
		m.picking = true
	case forms.KeySend:
		if len(m.changes) == 0 {
			m.notice = "Nothing to send yet: mark a change first with e, x, ? or /."
			break
		}
		m.pressed = forms.KeySend
		return true, tea.Quit
	default:
		return false, nil
	}
	m.follow()
	return true, nil
}

// askedText is the question already asked about target, offered again for changing.
func (m *accordion) askedText(target forms.Change) string {
	if c := m.find(forms.ChangeAsk, target.Note, target.Section); c >= 0 {
		return m.changes[c].Text
	}
	return ""
}

func (m *accordion) startTyping(target forms.Change, text string) {
	m.typing, m.pending = true, target
	m.multiline = target.Kind == forms.ChangeEdit && strings.Contains(text, "\n")
	if m.multiline {
		m.area.SetValue(text)
		m.area.SetWidth(max(1, m.width-boxPadding))
		m.area.SetHeight(min(areaMaxLines, max(areaMinLines, strings.Count(text, "\n")+areaSpareLines)))
		m.area.Placeholder = "Write it as it should read"
		m.area.Focus()
		return
	}
	m.input.SetValue(text)
	m.input.CursorEnd()
	m.input.Width = max(1, m.width-boxPadding)
	m.input.Placeholder = "Type your question, then press ⏎"
	if target.Kind == forms.ChangeEdit {
		m.input.Placeholder = "Type the finding as it should read, then press ⏎"
	}
	m.input.Focus()
}

// typeKey handles a key while the user types: Enter keeps the text, Esc drops it.
func (m *accordion) typeKey(msg tea.KeyMsg) tea.Cmd {
	if m.multiline {
		return m.typeAreaKey(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		m.aborted = true
		return tea.Quit
	case "esc":
		m.typing = false
		m.input.Blur()
		return nil
	case "enter":
		m.typing = false
		m.input.Blur()
		m.commit(strings.TrimSpace(m.input.Value()))
		m.follow()
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

// typeAreaKey handles a key while the user rewrites a note of several lines: ctrl+s keeps the text, Esc drops it, and
// every other key, Enter included, types.
func (m *accordion) typeAreaKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		m.aborted = true
		return tea.Quit
	case "esc":
		m.typing, m.multiline = false, false
		m.area.Blur()
		return nil
	case "ctrl+s":
		m.typing, m.multiline = false, false
		m.area.Blur()
		m.commit(strings.TrimSpace(m.area.Value()))
		m.follow()
		return nil
	}
	var cmd tea.Cmd
	m.area, cmd = m.area.Update(msg)
	return cmd
}

// commit records what was typed: a rewrite replaces an earlier one of the same note, a question an earlier one about
// the same thing. An empty rewrite, or one that changes nothing, keeps the note as it was; an empty question withdraws it.
func (m *accordion) commit(text string) {
	c := m.pending
	if i := m.find(c.Kind, c.Note, c.Section); i >= 0 {
		m.changes = slices.Delete(m.changes, i, i+1)
	}
	if c.Kind == forms.ChangeEdit {
		if note, ok := m.noteByID(c.Note); ok && (text == "" || text == strings.TrimSpace(note.Raw)) {
			return
		}
	}
	if text == "" {
		return
	}
	c.Text = text
	m.changes = append(m.changes, c)
}

func (m *accordion) find(kind forms.ChangeKind, note, section string) int {
	return slices.IndexFunc(m.changes, func(c forms.Change) bool {
		return c.Kind == kind && c.Note == note && c.Section == section
	})
}

func (m *accordion) deleted(id string) bool { return m.find(forms.ChangeDelete, id, "") >= 0 }

// sectionChanges counts the changes marked on section i: on its notes, or a question about the section itself.
func (m *accordion) sectionChanges(i int) int {
	title, n := ansi.Strip(m.a.Items[i].Title), 0
	for _, c := range m.changes {
		onNote := c.Note != "" && slices.ContainsFunc(m.a.Items[i].Notes, func(x forms.Note) bool { return x.ID == c.Note })
		if onNote || c.Note == "" && c.Section == title {
			n++
		}
	}
	return n
}

// note is the note under the cursor, if the cursor is on one.
func (m *accordion) note() (forms.Note, bool) {
	if m.sub < 0 || len(m.a.Items) == 0 || m.sub >= len(m.a.Items[m.cursor].Notes) {
		return forms.Note{}, false
	}
	return m.a.Items[m.cursor].Notes[m.sub], true
}

func (m *accordion) noteByID(id string) (forms.Note, bool) {
	for _, s := range m.a.Items {
		for _, n := range s.Notes {
			if n.ID == id {
				return n, true
			}
		}
	}
	return forms.Note{}, false
}

// move steps the cursor by one row: with Edits, through the notes of an open section too.
func (m *accordion) move(dir int) {
	if len(m.a.Items) == 0 {
		return
	}
	notes := func(i int) int {
		if !m.a.Edits || !m.open[i] {
			return 0
		}
		return len(m.a.Items[i].Notes)
	}
	if dir > 0 {
		if m.sub+1 < notes(m.cursor) {
			m.sub++
			return
		}
		if m.cursor+1 < len(m.a.Items) {
			m.cursor, m.sub = m.cursor+1, -1
		}
		return
	}
	if m.sub >= 0 {
		m.sub--
		return
	}
	if m.cursor > 0 {
		m.cursor--
		m.sub = notes(m.cursor) - 1
	}
}

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
	if len(m.a.Keys) > 0 {
		parts = append(parts, m.keyHelp("p", "post"))
	}
	parts = append(parts, m.keyHelp("q", "quit"), m.scrollPosition(lay))
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
	keys = append(keys, "click or ⏎ open", "↑↓ move", "a all", "q quit", "wheel or pgup/pgdn scroll")
	return m.a.Dim + strings.Join(keys, " · ") + reset
}

func (m *accordion) View() string {
	lay := m.layout()
	footer := m.footer(lay)
	visible := max(1, m.height-len(footer)-1)
	end := min(len(lay.lines), m.offset+visible)
	shown := append([]string(nil), lay.lines[min(m.offset, end):end]...)
	for len(shown) < visible {
		shown = append(shown, "")
	}
	for i, l := range footer {
		footer[i] = m.cut(l)
	}
	return strings.Join(shown, "\n") + "\n\n" + strings.Join(footer, "\n")
}

// showAccordion runs the view on the alternate screen and answers with the Action key that closed it, if any, and the
// changes the user sent with it.
func showAccordion(a forms.Accordion) (forms.Answers, error) {
	m := newAccordion(a)
	// Mouse reporting lets a click open a section and the wheel scroll; terminals still select text with Option or
	// Shift held while dragging.
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		return nil, fmt.Errorf("accordion: %w", err)
	}
	if m.aborted {
		return nil, huh.ErrUserAborted
	}
	answers := forms.Answers{forms.AccordionKey: {Value: m.pressed}}
	if m.pressed == forms.KeySend || len(m.changes) > 0 {
		data, err := json.Marshal(m.changes)
		if err != nil {
			return nil, fmt.Errorf("accordion: encoding the changes: %w", err)
		}
		answers[forms.ChangesKey] = forms.Answer{Value: string(data)}
	}
	return answers, nil
}
