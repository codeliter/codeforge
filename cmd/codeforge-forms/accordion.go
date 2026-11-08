package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"

	"github.com/codeliter/codeforge/src/forms"
)

const (
	reset       = "\x1b[0m"
	bodyIndent  = "    "
	footerLines = 2 // a blank line and the key help
	minHeight   = 3
	// The size assumed until the terminal reports its own.
	startWidth, startHeight = 80, 24
	percent                 = 100
	wheelLines              = 3 // lines one wheel notch scrolls
)

// accordion is the view of a forms.Accordion: a cursor over the sections, which are open or closed, scrolled so the
// cursor's row stays on screen.
type accordion struct {
	a       forms.Accordion
	open    []bool
	cursor  int
	offset  int
	width   int
	height  int
	pressed string // the Action key that closed the view
	aborted bool
}

func newAccordion(a forms.Accordion) *accordion {
	m := &accordion{a: a, open: make([]bool, len(a.Items)), width: startWidth, height: startHeight}
	for i := range m.open {
		m.open[i] = a.Open || a.Items[i].Open
	}
	if m.a.Dim == "" {
		m.a.Dim = "\x1b[2m"
	}
	if m.a.Accent == "" {
		m.a.Accent = "\x1b[1m"
	}
	return m
}

func (*accordion) Init() tea.Cmd { return nil }

func (m *accordion) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, max(minHeight, msg.Height)
		m.follow()
	case tea.KeyMsg:
		return m, m.key(msg.String())
	case tea.MouseMsg:
		m.mouse(msg)
	}
	return m, nil
}

// mouse opens or closes the section whose row was clicked, and scrolls with the wheel.
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
		_, rows := m.lines()
		line := m.offset + msg.Y
		for i, r := range rows {
			if r == line {
				m.cursor = i
				m.open[i] = !m.open[i]
				m.follow()
				return
			}
		}
	default:
	}
}

// visible is how many lines of the view fit above the key help.
func (m *accordion) visible() int { return max(1, m.height-footerLines) }

// scroll moves the view by n lines without moving the cursor, so reading down a long section never snaps back to it.
func (m *accordion) scroll(n int) {
	lines, _ := m.lines()
	m.offset = max(0, min(m.offset+n, len(lines)-m.visible()))
}

// key acts on one key press and returns tea.Quit when the view is done.
func (m *accordion) key(k string) tea.Cmd {
	page := max(1, m.height-footerLines-1)
	switch k {
	case "pgdown", "ctrl+d", "f":
		m.scroll(page)
		return nil
	case "pgup", "ctrl+u", "b":
		m.scroll(-page)
		return nil
	}
	switch k {
	case "ctrl+c":
		m.aborted = true
		return tea.Quit
	case "q", "esc":
		return tea.Quit
	case "up", "k", "shift+tab":
		m.cursor = max(0, m.cursor-1)
	case "down", "j", "tab":
		m.cursor = min(len(m.a.Items)-1, m.cursor+1)
	case "enter", " ", "right", "left", "l", "h":
		if len(m.open) > 0 {
			m.open[m.cursor] = k == "right" || k == "l" || (k != "left" && k != "h" && !m.open[m.cursor])
		}
	case "a":
		all := true
		for _, o := range m.open {
			all = all && o
		}
		for i := range m.open {
			m.open[i] = !all
		}
	case "home", "g":
		m.cursor, m.offset = 0, 0
	case "end", "G":
		m.cursor = max(0, len(m.a.Items)-1)
	default:
		for _, act := range m.a.Keys {
			if act.Key != "" && k == act.Key {
				m.pressed = act.Key
				return tea.Quit
			}
		}
		return nil
	}
	m.follow()
	return nil
}

// lines lays the view out and returns where each section's row is.
func (m *accordion) lines() ([]string, []int) {
	var out []string
	add := func(text string) {
		if text != "" {
			out = append(out, strings.Split(strings.TrimRight(text, "\n"), "\n")...)
			out = append(out, "")
		}
	}
	add(m.a.Intro)
	rows := make([]int, len(m.a.Items))
	for i, s := range m.a.Items {
		rows[i] = len(out)
		pointer, arrow := "  ", "▸"
		if i == m.cursor {
			pointer = m.a.Accent + "›" + reset + " "
		}
		if m.open[i] {
			arrow = "▾"
		}
		row := pointer + m.a.Dim + arrow + reset + " " + s.Title
		if s.Summary != "" {
			row += "  " + m.a.Dim + s.Summary + reset
		}
		out = append(out, ansi.Truncate(row, m.width, "…"))
		if m.open[i] && strings.TrimSpace(s.Body) != "" {
			for _, l := range strings.Split(strings.TrimRight(s.Body, "\n"), "\n") {
				out = append(out, ansi.Truncate(bodyIndent+l, m.width, "…"))
			}
			out = append(out, "")
		}
	}
	if m.a.Outro != "" {
		out = append(out, "")
		add(m.a.Outro)
	}
	return out, rows
}

// follow scrolls so the cursor's row is on screen and the view never scrolls past its end.
func (m *accordion) follow() {
	lines, rows := m.lines()
	visible := max(1, m.height-footerLines)
	if len(rows) > 0 {
		row := rows[m.cursor]
		if row < m.offset {
			m.offset = row
		}
		if row >= m.offset+visible {
			m.offset = row - visible + 1
		}
	}
	m.offset = max(0, min(m.offset, len(lines)-visible))
}

func (m *accordion) View() string {
	lines, _ := m.lines()
	visible := max(1, m.height-footerLines)
	end := min(len(lines), m.offset+visible)
	shown := append([]string(nil), lines[min(m.offset, end):end]...)
	for len(shown) < visible {
		shown = append(shown, "")
	}
	// The keys that matter most come first, so a narrow terminal cuts the least useful ones.
	var keys []string
	if len(lines) > visible {
		keys = append(keys, fmt.Sprintf("%d%%", min(percent, percent*end/len(lines))))
	}
	for _, act := range m.a.Keys {
		keys = append(keys, act.Key+" "+act.Label)
	}
	keys = append(keys, "click or ⏎ open", "↑↓ move", "a all", "q quit", "wheel or pgup/pgdn scroll")
	help := strings.Join(keys, " · ")
	return strings.Join(shown, "\n") + "\n\n" + ansi.Truncate(m.a.Dim+help+reset, m.width, "…")
}

// showAccordion runs the view on the alternate screen and answers with the Action key that closed it, if any.
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
	return forms.Answers{forms.AccordionKey: {Value: m.pressed}}, nil
}
