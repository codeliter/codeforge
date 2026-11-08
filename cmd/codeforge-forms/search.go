package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"

	"github.com/codeliter/codeforge/src/forms"
)

// search is the view of a forms.Search field: a title, a search box that is always there and takes every key typed,
// and the options whose label has every word in the box, with a cursor on one of them.
type search struct {
	f       forms.Field
	input   textinput.Model
	matches []int // indexes into f.Options, in their order
	cursor  int   // index into matches
	offset  int   // first match on screen
	width   int
	height  int
	chosen  string
	aborted bool
}

const (
	accent = "\x1b[1;36m"
	dim    = "\x1b[2m"
	// searchChrome is the lines around the list: title, description, the box (3), the header, a blank and the footer.
	searchChrome = 8
)

func newSearch(f forms.Field) *search {
	m := &search{f: f, width: startWidth, height: startHeight}
	m.input = textinput.New()
	m.input.Prompt = ""
	m.input.Placeholder = cmpOr(f.Placeholder, "Type to search")
	m.input.CharLimit = inputLimit
	m.input.Focus()
	m.filter()
	// The offered value starts under the cursor.
	for i, o := range m.matches {
		if f.Options[o].Value == f.Value {
			m.cursor = i
		}
	}
	m.follow()
	return m
}

func (*search) Init() tea.Cmd { return textinput.Blink }

func (m *search) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, max(minHeight+searchChrome, msg.Height)
		m.input.Width = max(1, m.width-boxPadding)
		m.follow()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.aborted = true
			return m, tea.Quit
		case "esc":
			// Esc clears what was typed first, so leaving takes a second press and a search is never lost by accident.
			if m.input.Value() != "" {
				m.input.SetValue("")
				m.filter()
				return m, nil
			}
			m.aborted = true
			return m, tea.Quit
		case "enter":
			if len(m.matches) > 0 {
				m.chosen = m.f.Options[m.matches[m.cursor]].Value
				return m, tea.Quit
			}
			return m, nil
		case "up", "ctrl+p", "shift+tab":
			m.move(-1)
			return m, nil
		case "down", "ctrl+n", "tab":
			m.move(1)
			return m, nil
		case "pgup":
			m.move(-m.rows())
			return m, nil
		case "pgdown":
			m.move(m.rows())
			return m, nil
		}
		before := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.input.Value() != before {
			m.filter()
		}
		return m, cmd
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// filter keeps the options whose label holds every word typed, in any order and any case, and puts the cursor on the
// first of them.
func (m *search) filter() {
	words := strings.Fields(strings.ToLower(m.input.Value()))
	m.matches = m.matches[:0]
	for i, o := range m.f.Options {
		label := strings.ToLower(ansi.Strip(o.Label))
		all := true
		for _, w := range words {
			all = all && strings.Contains(label, w)
		}
		if all {
			m.matches = append(m.matches, i)
		}
	}
	m.cursor, m.offset = 0, 0
}

func (m *search) move(n int) {
	if len(m.matches) == 0 {
		return
	}
	m.cursor = max(0, min(len(m.matches)-1, m.cursor+n))
	m.follow()
}

// rows is how many options fit on screen.
func (m *search) rows() int {
	n := m.height - searchChrome
	if m.f.Lines > 0 {
		n = min(n, m.f.Lines)
	}
	return max(1, n)
}

// follow scrolls so the cursor's option is on screen.
func (m *search) follow() {
	rows := m.rows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	m.offset = max(0, min(m.offset, len(m.matches)-rows))
}

func (m *search) View() string {
	var b strings.Builder
	cut := func(s string) string { return ansi.Truncate(s, m.width, "…") }
	b.WriteString(cut(accent+m.f.Title+reset) + "\n")
	b.WriteString(cut(dim+m.f.Description+reset) + "\n")
	// The box is drawn whatever is in it, so it is plain from the start that typing searches.
	inner := max(boxPadding, m.width-boxSides)
	title := " Search "
	body := m.input.View()
	b.WriteString(accent + "┌─" + title + strings.Repeat("─", max(0, inner-1-len(title))) + "┐" + reset + "\n")
	b.WriteString(accent + "│" + reset + " " + body + strings.Repeat(" ", max(0, inner-boxSides-ansi.StringWidth(body))) + " " + accent + "│" + reset + "\n")
	b.WriteString(accent + "└" + strings.Repeat("─", inner) + "┘" + reset + "\n")
	b.WriteString(cut(dim+"  "+m.f.Header+reset) + "\n")
	rows := m.rows()
	if len(m.matches) == 0 {
		b.WriteString(dim + "  Nothing matches. Esc clears the search." + reset + "\n")
		rows--
	}
	end := min(len(m.matches), m.offset+rows)
	for i := m.offset; i < end; i++ {
		label := ansi.Strip(m.f.Options[m.matches[i]].Label)
		if i == m.cursor {
			// The bar and the highlight's own margin take the two cells the other rows indent by, so columns line up.
			b.WriteString(cut(accent+"▌"+reverse+" "+label+" "+reset) + "\n")
			continue
		}
		b.WriteString(cut("  "+label) + "\n")
	}
	for range rows - (end - m.offset) {
		b.WriteString("\n")
	}
	position := fmt.Sprintf("%d of %d", len(m.matches), len(m.f.Options))
	if len(m.matches) > 0 {
		position = fmt.Sprintf("%d of %d", m.cursor+1, len(m.matches))
		if len(m.matches) < len(m.f.Options) {
			position += fmt.Sprintf(" (of %d)", len(m.f.Options))
		}
	}
	keys := dim + position + "  ·  " + reset + accent + "↑↓" + reset + dim + " move  ·  " + reset + accent + "⏎" + reset + dim + " open  ·  " + reset +
		accent + "esc" + reset + dim + " clear, then quit" + reset
	b.WriteString("\n" + cut(keys))
	return b.String()
}

// showSearch runs a Search field on the alternate screen and answers with the Value picked.
func showSearch(f forms.Field) (forms.Answers, error) {
	m := newSearch(f)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if m.aborted {
		return nil, huh.ErrUserAborted
	}
	return forms.Answers{f.Key: {Value: m.chosen}}, nil
}
