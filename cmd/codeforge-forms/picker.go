package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"

	"github.com/codeliter/codeforge/src/forms"
)

// pickRow is one line of a picker: a group's heading (opt below zero, n options under it) or one of its options.
type pickRow struct {
	group string
	opt   int
	n     int
}

// picker is the view of a forms.Search field whose options are grouped, or that takes several: a search line that
// takes every key typed, then each group as a heading that opens and closes, its options under it in columns, and a
// tick on what is chosen now.
type picker struct {
	f         forms.Field
	input     textinput.Model
	groups    []string
	rows      []pickRow
	cursor    int
	offset    int
	width     int
	height    int
	collapsed map[string]bool
	picked    map[string]bool
	order     []string // what is ticked, in the order it was ticked
	chosen    string
	aborted   bool
}

const (
	bold = "\x1b[1m"
	// pickerChrome is the lines around the list: title, description, a blank, the search line, a blank, the column
	// header, the position, a blank and the keys.
	pickerChrome = 9
	// pickerIndent is what comes before an option's label: the margin, the branch and the tick.
	pickerIndent = 7
	minLabel     = 10
)

// grouped reports whether a Search field is shown as a picker rather than a plain list.
func grouped(f forms.Field) bool {
	return f.Multi || slices.ContainsFunc(f.Options, func(o forms.Option) bool { return o.Group != "" })
}

func newPicker(f forms.Field) *picker {
	m := &picker{f: f, width: startWidth, height: startHeight, collapsed: map[string]bool{}, picked: map[string]bool{}}
	m.input = textinput.New()
	m.input.Prompt = ""
	m.input.Placeholder = cmpOr(f.Placeholder, "Type to search")
	m.input.CharLimit = inputLimit
	m.input.Focus()
	for _, o := range f.Options {
		if !slices.Contains(m.groups, o.Group) {
			m.groups = append(m.groups, o.Group)
		}
	}
	// Only what is offered can be ticked: a saved value that is no longer an option would be answered unseen.
	offered := func(v string) bool {
		return slices.ContainsFunc(f.Options, func(o forms.Option) bool { return o.Value == v })
	}
	for _, v := range f.Values {
		if !offered(v) {
			continue
		}
		if !m.picked[v] {
			m.order = append(m.order, v)
		}
		m.picked[v] = true
	}
	if !f.Multi && offered(f.Value) {
		m.picked[f.Value] = true
	}
	m.filter()
	// What is chosen now starts under the cursor.
	for i, r := range m.rows {
		if r.opt >= 0 && m.picked[f.Options[r.opt].Value] {
			m.cursor = i
			break
		}
	}
	m.follow()
	return m
}

func (*picker) Init() tea.Cmd { return textinput.Blink }

func (m *picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, max(minHeight+pickerChrome, msg.Height)
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
			return m, m.enter()
		case "ctrl+s":
			if m.f.Multi && len(m.picked) > 0 {
				return m, tea.Quit
			}
			return m, nil
		case "up", "ctrl+p":
			m.move(-1)
			return m, nil
		case "down", "ctrl+n":
			m.move(1)
			return m, nil
		case "tab":
			m.jump(1)
			return m, nil
		case "shift+tab":
			m.jump(-1)
			return m, nil
		case "pgup":
			m.move(-m.visible())
			return m, nil
		case "pgdown":
			m.move(m.visible())
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

// enter opens or closes the heading under the cursor, or takes the option there: the answer of a picker of one, one
// more or one fewer of a picker of several.
func (m *picker) enter() tea.Cmd {
	if len(m.rows) == 0 {
		return nil
	}
	row := m.rows[m.cursor]
	if row.opt < 0 {
		// While a search is on every match shows, so there is nothing to close.
		if m.input.Value() == "" {
			m.collapsed[row.group] = !m.collapsed[row.group]
			m.filter()
			m.cursor = slices.IndexFunc(m.rows, func(r pickRow) bool { return r.opt < 0 && r.group == row.group })
			m.follow()
		}
		return nil
	}
	value := m.f.Options[row.opt].Value
	if !m.f.Multi {
		m.chosen = value
		return tea.Quit
	}
	if m.picked[value] {
		delete(m.picked, value)
		m.order = slices.DeleteFunc(m.order, func(v string) bool { return v == value })
	} else {
		m.picked[value] = true
		m.order = append(m.order, value)
	}
	return nil
}

// filter lists each group that has an option holding every word typed, in the group's name, the option's label or
// its detail, in any order and any case. A slash separates words, so "sub/son" finds Sonnet under subscriptions.
func (m *picker) filter() {
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(m.input.Value(), "/", " ")))
	m.rows = m.rows[:0]
	for _, g := range m.groups {
		var hits []int
		for i, o := range m.f.Options {
			if o.Group != g {
				continue
			}
			text := strings.ToLower(g + " " + ansi.Strip(o.Label) + " " + o.Detail)
			all := true
			for _, w := range words {
				all = all && strings.Contains(text, w)
			}
			if all {
				hits = append(hits, i)
			}
		}
		if len(hits) == 0 {
			continue
		}
		m.rows = append(m.rows, pickRow{group: g, opt: -1, n: len(hits)})
		if len(words) > 0 || !m.collapsed[g] {
			for _, i := range hits {
				m.rows = append(m.rows, pickRow{group: g, opt: i})
			}
		}
	}
	m.offset = 0
	m.cursor = max(0, slices.IndexFunc(m.rows, func(r pickRow) bool { return r.opt >= 0 }))
}

func (m *picker) move(n int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor = max(0, min(len(m.rows)-1, m.cursor+n))
	m.follow()
}

// jump moves to the next or the previous group's heading, round the ends.
func (m *picker) jump(dir int) {
	var heads []int
	for i, r := range m.rows {
		if r.opt < 0 {
			heads = append(heads, i)
		}
	}
	if len(heads) == 0 {
		return
	}
	at := 0
	for i, h := range heads {
		if h <= m.cursor {
			at = i
		}
	}
	if dir < 0 && heads[at] < m.cursor {
		at++ // from inside a group, back goes to its own heading first
	}
	m.cursor = heads[(at+dir+len(heads))%len(heads)]
	m.follow()
}

// visible is how many rows fit on screen.
func (m *picker) visible() int {
	n := m.height - pickerChrome
	if m.f.Lines > 0 {
		n = min(n, m.f.Lines)
	}
	return max(1, n)
}

// follow scrolls so the cursor's row is on screen.
func (m *picker) follow() {
	rows := m.visible()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	m.offset = max(0, min(m.offset, len(m.rows)-rows))
}

// labelWidth is the width of the label column: the widest label, within what the screen leaves for it.
func (m *picker) labelWidth() int {
	w, details := minLabel, false
	for _, o := range m.f.Options {
		w = max(w, ansi.StringWidth(ansi.Strip(o.Label)))
		details = details || o.Detail != ""
	}
	if !details {
		return max(minLabel, min(w, m.width-pickerIndent)) // no second column to leave room for
	}
	return max(minLabel, min(w, m.width-pickerIndent-minLabel))
}

func (m *picker) View() string {
	var b strings.Builder
	cut := func(s string) string { return ansi.Truncate(s, m.width, "…") }
	b.WriteString(cut(accent+m.f.Title+reset) + "\n")
	b.WriteString(cut(dim+m.f.Description+reset) + "\n\n")
	b.WriteString(cut(accent+"⌕ "+reset+m.input.View()) + "\n\n")
	width := m.labelWidth()
	// A tab in the header separates the label column's name from the detail column's.
	header, detail, _ := strings.Cut(m.f.Header, "\t")
	if detail != "" {
		header += strings.Repeat(" ", max(0, width-ansi.StringWidth(header))) + "  " + detail
	}
	b.WriteString(cut(dim+strings.Repeat(" ", pickerIndent)+header+reset) + "\n")
	rows := m.visible()
	if len(m.rows) == 0 {
		b.WriteString(dim + "  Nothing matches. Esc clears the search." + reset + "\n")
		rows--
	}
	end := min(len(m.rows), m.offset+rows)
	for i := m.offset; i < end; i++ {
		b.WriteString(cut(m.row(i, width)) + "\n")
	}
	for range rows - (end - m.offset) {
		b.WriteString("\n")
	}
	position := "0 / 0"
	if len(m.rows) > 0 {
		position = fmt.Sprintf("%d / %d", m.cursor+1, len(m.rows))
	}
	b.WriteString(cut(dim+"  "+position+reset) + "\n\n")
	key := func(k, what string) string { return accent + k + reset + dim + " " + what + reset }
	take := []string{key("⏎", "select")}
	if m.f.Multi {
		take = []string{key("⏎", "tick or untick"), key("ctrl+s", "done")}
	}
	if m.f.Ordered {
		take[0] = key("⏎", "tick in order, or untick")
	}
	keys := slices.Concat([]string{key("type", "search"), key("↑↓", "pick")}, take, []string{key("tab", "next group"), key("esc", "clear, then close")})
	b.WriteString(cut(strings.Join(keys, dim+"  ·  "+reset)))
	return b.String()
}

// row draws the heading or the option at i, with the label column width wide.
func (m *picker) row(i, width int) string {
	r := m.rows[i]
	if r.opt < 0 {
		arrow := "▾"
		if m.collapsed[r.group] && m.input.Value() == "" {
			arrow = "▸"
		}
		if i == m.cursor {
			return fmt.Sprintf("%s▌%s %s %s  %d %s", accent, reverse, arrow, r.group, r.n, reset)
		}
		return fmt.Sprintf("  %s %s%s%s%s  %d%s", arrow, bold, r.group, reset, dim, r.n, reset)
	}
	o := m.f.Options[r.opt]
	branch := "├─ "
	if i+1 == len(m.rows) || m.rows[i+1].opt < 0 {
		branch = "└─ "
	}
	tick := "  "
	if m.picked[o.Value] {
		tick = "✓ "
		if m.f.Ordered {
			tick = strconv.Itoa(slices.Index(m.order, o.Value)+1) + " "
		}
	}
	label := ansi.Truncate(ansi.Strip(o.Label), width, "…")
	pad := strings.Repeat(" ", max(0, width-ansi.StringWidth(label)))
	if i == m.cursor {
		return accent + "▌" + reverse + " " + branch + tick + label + pad + "  " + o.Detail + " " + reset
	}
	colour := ""
	if m.picked[o.Value] {
		colour = accent
	}
	return "  " + dim + branch + reset + colour + tick + label + reset + pad + "  " + dim + o.Detail + reset
}

// showPicker runs a grouped Search field on the alternate screen and answers with the Value picked, or the Values.
func showPicker(f forms.Field) (forms.Answers, error) {
	m := newPicker(f)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		return nil, fmt.Errorf("picker: %w", err)
	}
	if m.aborted {
		return nil, huh.ErrUserAborted
	}
	if !f.Multi {
		return forms.Answers{f.Key: {Value: m.chosen}}, nil
	}
	return forms.Answers{f.Key: {Values: m.values()}}, nil
}

// values is what is ticked: in the order ticked for an ordered picker, else in the order offered.
func (m *picker) values() []string {
	if m.f.Ordered {
		return slices.Clone(m.order)
	}
	var values []string
	for _, o := range m.f.Options {
		if m.picked[o.Value] && !slices.Contains(values, o.Value) {
			values = append(values, o.Value)
		}
	}
	return values
}
