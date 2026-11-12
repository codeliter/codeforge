package main

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/codeliter/codeforge/src/forms"
)

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
		m.areaTop = 0
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
	m.followArea()
	return cmd
}

// areaRow is the row the textarea's cursor is on, counting the rows its long lines wrap onto.
func (m *accordion) areaRow() int {
	c := m.area // a copy: moving it leaves the real cursor where it is
	row := 0
	for range areaLimitFactor * inputLimit {
		if c.Line() == 0 && c.LineInfo().RowOffset == 0 {
			break
		}
		c.CursorUp()
		row++
	}
	return row
}

// followArea scrolls areaTop as the textarea scrolls its own view: just far enough that the cursor's row shows.
func (m *accordion) followArea() {
	row, height := m.areaRow(), m.area.Height()
	switch {
	case row < m.areaTop:
		m.areaTop = row
	case row >= m.areaTop+height:
		m.areaTop = row - height + 1
	}
}

// clickInput puts the cursor where the text box was clicked; any other click while typing does nothing.
func (m *accordion) clickInput(msg tea.MouseMsg) {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return
	}
	// The box sits under the view and a blank line: its top border, then its text rows, each opening with "│ ".
	row, col := msg.Y-(m.visible()+1)-1, msg.X-boxSides
	if m.multiline {
		if row < 0 || row >= m.area.Height() || col < 0 {
			return
		}
		target, now := m.areaTop+row, m.areaRow()
		for ; now < target && m.area.Line() < m.area.LineCount(); now++ {
			before, offset := m.area.Line(), m.area.LineInfo().RowOffset
			m.area.CursorDown()
			if m.area.Line() == before && m.area.LineInfo().RowOffset == offset {
				break // the last row: a click below the text lands on it
			}
		}
		for ; now > target; now-- {
			m.area.CursorUp()
		}
		m.area.SetCursor(m.area.LineInfo().StartColumn + col)
		m.area, _ = m.area.Update(nil)
		m.followArea()
		return
	}
	if row != 0 || col < 0 {
		return
	}
	m.input.SetCursor(inputOffset(m.input.Value(), ansi.Strip(m.input.View()), m.input.Position()) + col)
}

// inputOffset is the index of the first rune of value the one-line input shows: where its visible text, as drawn, starts
// in value, nearest the cursor at pos when it shows up more than once. A long value scrolls sideways under the cursor.
func inputOffset(value, shown string, pos int) int {
	runes, vis := []rune(value), []rune(strings.TrimRight(shown, " "))
	if len(vis) == 0 || len(vis) > len(runes) {
		return 0
	}
	best, bestGap := 0, -1
	for o := 0; o+len(vis) <= len(runes); o++ {
		if string(runes[o:o+len(vis)]) != string(vis) {
			continue
		}
		gap := pos - o
		if gap < 0 {
			gap = -gap
		}
		if bestGap < 0 || gap < bestGap {
			best, bestGap = o, gap
		}
	}
	return best
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
