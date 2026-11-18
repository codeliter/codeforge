package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"

	"github.com/codeliter/codeforge/src/forms"
)

const (
	stepBold = "\x1b[1m"
	// Around the step shown go the strip and the keys, and a rule between each and the step when the terminal has
	// at least ruledHeight lines; one with fewer than bareHeight shows the step alone.
	ruledHeight = 6
	bareHeight  = 3
)

// steps is the view of a forms.Steps: one step a screen, under a strip of tabs that says which.
type steps struct {
	s       forms.Steps
	cur     int
	offset  int
	width   int
	height  int
	pressed string // the Action key that closed the view
	typed   string // the digits of a tab typed so far, so "12" reaches the twelfth stop
	aborted bool
}

var errNoSteps = errors.New("steps: nothing to show")

// showSteps runs the view on the alternate screen and answers with the Action key that closed it, if any.
func showSteps(s forms.Steps) (forms.Answers, error) {
	if len(s.Items) == 0 {
		return nil, errNoSteps
	}
	m := &steps{s: s, width: startWidth, height: startHeight}
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		return nil, fmt.Errorf("steps: %w", err)
	}
	if m.aborted {
		return nil, huh.ErrUserAborted
	}
	return forms.Answers{forms.AccordionKey: {Value: m.pressed}}, nil
}

func (*steps) Init() tea.Cmd { return nil }

func (m *steps) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.scroll(0)
	case tea.KeyMsg:
		if m.key(msg.String()) {
			return m, tea.Quit
		}
	case tea.MouseMsg:
		switch msg.Button { //nolint:exhaustive // only the wheel scrolls
		case tea.MouseButtonWheelUp:
			m.scroll(-wheelLines)
		case tea.MouseButtonWheelDown:
			m.scroll(wheelLines)
		}
	}
	return m, nil
}

// key acts on a key and reports whether it closes the view.
func (m *steps) key(k string) bool {
	for _, act := range m.s.Keys {
		if act.Key == k {
			m.pressed = k
			return true
		}
	}
	if len(k) == 1 && k[0] >= '0' && k[0] <= '9' {
		m.digit(k)
		return false
	}
	m.typed = ""
	switch k {
	case "ctrl+c":
		m.aborted = true
		return true
	case "q", "esc":
		return true
	case "right", "l", "n", "tab", "enter":
		m.to(m.cur + 1)
	case "left", "h", "p", "shift+tab":
		m.to(m.cur - 1)
	case "up", "k":
		m.scroll(-1)
	case "down", "j":
		m.scroll(1)
	case "pgup", "b":
		m.scroll(-m.visible())
	case "pgdown", " ":
		m.scroll(m.visible())
	case "home", "g":
		m.offset = 0
	case "end", "G":
		m.scroll(len(m.lines()))
	case "o":
		m.to(0)
	}
	return false
}

// digit takes one digit of a tab's number. The digits typed so far are kept while they begin some tab's name, so
// "1" then "2" is the twelfth stop when there is one, and "123" is reached though there is no "12"; a digit that
// fits no tab with them starts a number of its own. 0 alone is the first step.
func (m *steps) digit(k string) {
	names := make([]string, len(m.s.Items))
	for i, it := range m.s.Items {
		names[i] = ansi.Strip(it.Tab)
	}
	begins := func(typed string) bool {
		return slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, typed) })
	}
	typed := m.typed + k
	if !begins(typed) {
		typed = k
	}
	m.typed = ""
	if begins(typed) {
		m.typed = typed
	}
	switch i := slices.Index(names, typed); {
	case i >= 0:
		m.to(i)
	case typed == "0":
		m.to(0)
	}
}

// to shows step i from its top, staying within the steps there are.
func (m *steps) to(i int) {
	if i = max(0, min(len(m.s.Items)-1, i)); i != m.cur {
		m.cur, m.offset = i, 0
	}
}

// chrome is how many lines the strip, the keys and the rules take, in a terminal of this height.
func (m *steps) chrome() int {
	switch {
	case m.height >= ruledHeight:
		return 4 //nolint:mnd // the strip, the keys and two rules
	case m.height >= bareHeight:
		return 2 //nolint:mnd // the strip and the keys
	}
	return 0
}

func (m *steps) visible() int { return max(1, m.height-m.chrome()) }

// lines is the step shown as the rows it takes at this width: a line laid out for a wider terminal goes on under
// itself, so nothing of it is cut off.
func (m *steps) lines() []string {
	var rows []string
	for _, l := range strings.Split(strings.TrimRight(m.s.Items[m.cur].Body, "\n"), "\n") {
		if m.width < 1 || ansi.StringWidth(l) <= m.width {
			rows = append(rows, l)
			continue
		}
		rows = append(rows, strings.Split(ansi.Hardwrap(l, m.width, true), "\n")...)
	}
	return rows
}

// scroll moves the step shown by d lines, never past its ends.
func (m *steps) scroll(d int) {
	m.offset = max(0, min(len(m.lines())-m.visible(), m.offset+d))
}

func (m *steps) View() string {
	lines := m.lines()
	rule := m.s.Dim + strings.Repeat("─", max(0, m.width)) + reset
	chrome := m.chrome()
	var rows []string
	if chrome > 0 {
		rows = append(rows, m.strip())
	}
	if chrome >= ruledHeight-2 {
		rows = append(rows, rule)
	}
	for i := range m.visible() {
		row := ""
		if n := m.offset + i; n < len(lines) {
			row = lines[n] + reset
		}
		rows = append(rows, row)
	}
	if chrome >= ruledHeight-2 {
		rows = append(rows, rule)
	}
	if chrome > 0 {
		rows = append(rows, m.help(len(lines)))
	}
	return strings.Join(rows, "\n")
}

// strip is the top line: the title, the tabs with the one shown marked, and at the right which step of how many this
// is, with its status.
func (m *steps) strip() string {
	left := ""
	if m.s.Title != "" {
		left = m.s.Accent + stepBold + m.s.Title + reset + " "
	}
	right := m.s.Dim + fmt.Sprintf("%d of %d", m.cur+1, len(m.s.Items)) + reset
	if status := m.s.Items[m.cur].Status; status != "" {
		right += m.s.Dim + " · " + reset + status + reset
	}
	// The tab shown is what the strip is for: when all of it does not fit, the status goes first, then the title,
	// then the count.
	count := m.s.Dim + fmt.Sprintf("%d of %d", m.cur+1, len(m.s.Items)) + reset
	least := ansi.StringWidth(m.tabs(0))
	for _, sides := range [][2]string{{left, right}, {left, count}, {"", count}, {"", ""}} {
		left, right = sides[0], sides[1]
		if m.width-ansi.StringWidth(left)-ansi.StringWidth(right)-1 >= least {
			break
		}
	}
	tabs := m.tabs(m.width - ansi.StringWidth(left) - ansi.StringWidth(right) - 1)
	pad := max(1, m.width-ansi.StringWidth(left)-ansi.StringWidth(tabs)-ansi.StringWidth(right))
	if right == "" {
		pad = 0
	}
	return ansi.Truncate(left+tabs+strings.Repeat(" ", pad)+right, m.width, "…") + reset
}

// tabs is the tabs that fit in avail columns: all of them under their group names, or without the names, or as many
// either side of the one shown as there is room for.
func (m *steps) tabs(avail int) string {
	cells := make([]string, len(m.s.Items))
	for i, it := range m.s.Items {
		if i == m.cur {
			cells[i] = reverse + " " + ansi.Strip(it.Tab) + " " + reset
		} else {
			cells[i] = it.Color + " " + it.Tab + " " + reset
		}
	}
	join := func(groups bool, from, to int) string {
		var b strings.Builder
		if from > 0 {
			b.WriteString(m.s.Dim + "…" + reset)
		}
		for i := from; i < to; i++ {
			if g := m.s.Items[i].Group; groups && g != "" && (i == 0 || m.s.Items[i-1].Group != g) {
				b.WriteString(m.s.Dim + "  " + g + reset)
			}
			b.WriteString(cells[i])
		}
		if to < len(cells) {
			b.WriteString(m.s.Dim + "…" + reset)
		}
		return b.String()
	}
	for _, groups := range []bool{true, false} {
		if s := join(groups, 0, len(cells)); ansi.StringWidth(s) <= avail {
			return s
		}
	}
	from, to := m.cur, m.cur+1
	for {
		grown := false
		if to < len(cells) && ansi.StringWidth(join(false, from, to+1)) <= avail {
			to, grown = to+1, true
		}
		if from > 0 && ansi.StringWidth(join(false, from-1, to)) <= avail {
			from, grown = from-1, true
		}
		if !grown {
			return join(false, from, to)
		}
	}
}

// help is the key line, with how far down a step longer than the screen is.
func (m *steps) help(n int) string {
	var keys []string
	if v := m.visible(); n > v {
		keys = append(keys, fmt.Sprintf("%d%%", min(percent, percent*min(n, m.offset+v)/n)))
	}
	keys = append(keys, "←/→ step")
	for _, act := range m.s.Keys {
		keys = append(keys, act.Key+" "+act.Label)
	}
	keys = append(keys, "↑↓ scroll", "1-9 jump", "o overview", "q back")
	return m.s.Dim + ansi.Truncate(strings.Join(keys, " · "), m.width, "…") + reset
}
