package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/atotto/clipboard"
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
	// areaTop is the first line of the rewrite shown in the box: the textarea scrolls as its own view does, and keeps
	// no way to ask, so a click can be placed on the line under it.
	areaTop int
	// notice is a line that asks for attention, and armed the key that, pressed again, goes ahead past it.
	notice string
	armed  string
	// picking is true while the footer offers the ways to finish (p), so the rest of the time it stays one short line.
	picking bool
	// selecting is set while the mouse is left to the terminal, so text can be selected and copied by dragging.
	selecting bool
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
		if m.typing {
			m.clickInput(msg)
			return m, nil
		}
		m.mouse(msg)
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
	case "y":
		m.copyFocused()
		return nil
	case "m":
		// The view takes the mouse for clicks and the wheel; handing it back lets the terminal select text by dragging.
		m.selecting = !m.selecting
		m.notice = "Mouse back on: click to open, wheel to scroll."
		if m.selecting {
			m.notice = "Drag to select text and copy it as usual; press m again for clicks and the wheel."
			return tea.DisableMouse
		}
		return tea.EnableMouseCellMotion
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

// mouseHelp names what m does now.
func (m *accordion) mouseHelp() string {
	if m.selecting {
		return "mouse back"
	}
	return "select text"
}

// focusedText is the text under the cursor as plain words: the note picked, or the section's title and all it holds.
func (m *accordion) focusedText() (what, text string) {
	if note, ok := m.note(); ok {
		return cmpOr(note.Label, "this note"), ansi.Strip(note.Body)
	}
	if len(m.a.Items) == 0 {
		return "", ""
	}
	s := m.a.Items[m.cursor]
	parts := []string{ansi.Strip(s.Title)}
	if strings.TrimSpace(s.Body) != "" {
		parts = append(parts, ansi.Strip(s.Body))
	}
	for _, n := range s.Notes {
		parts = append(parts, ansi.Strip(n.Body))
	}
	return ansi.Strip(s.Title), strings.Join(parts, "\n\n")
}

// copyFocused puts the text under the cursor on the clipboard: through the system clipboard where there is one, and
// through the terminal (OSC 52) as well, which reaches the user's own clipboard over ssh.
func (m *accordion) copyFocused() {
	what, text := m.focusedText()
	if strings.TrimSpace(text) == "" {
		m.notice = "Nothing to copy here."
		return
	}
	text = strings.TrimSpace(text)
	sysErr := writeClipboard(text)
	fmt.Fprint(clipboardTerm, "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(text))+"\x07")
	m.notice = "Copied " + what + "."
	if sysErr != nil {
		m.notice = "Copied " + what + " through the terminal; the system clipboard said: " + sysErr.Error()
	}
}

// writeClipboard and clipboardTerm are where copyFocused writes; tests replace them.
var (
	writeClipboard           = clipboard.WriteAll
	clipboardTerm  io.Writer = os.Stdout
)

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
