package main

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
)

func testSteps(n int) *steps {
	s := forms.Steps{Title: "Walkthrough", Keys: []forms.Action{{Key: "f", Label: "the whole change"}}}
	s.Items = append(s.Items, forms.Step{Tab: "Overview", Body: "what it does"})
	for i := 1; i <= n; i++ {
		group := "Your comments"
		if i > 2 {
			group = "Other changes"
		}
		s.Items = append(s.Items, forms.Step{Tab: string(rune('0' + i)), Group: group, Status: "met", Body: strings.Repeat("line\n", 40) + "last"})
	}
	return &steps{s: s, width: 100, height: 14}
}

func TestSteps_MoveBetweenStepsAndScrollWithinOne(t *testing.T) {
	m := testSteps(4)
	require.False(t, m.key("right"))
	require.Equal(t, 1, m.cur)
	m.key("down")
	m.key("pgdown")
	require.Equal(t, 1+m.visible(), m.offset)
	m.key("right")
	require.Equal(t, []int{2, 0}, []int{m.cur, m.offset}, "a step opens at its top")
	m.key("4")
	require.Equal(t, 4, m.cur, "its tab typed jumps to a step")
	m.key("right")
	require.Equal(t, 4, m.cur, "never past the last")
	m.key("end")
	require.Equal(t, len(m.lines())-m.visible(), m.offset)
	m.key("pgdown")
	require.Equal(t, len(m.lines())-m.visible(), m.offset, "never past the end")
	m.key("o")
	m.key("left")
	require.Equal(t, 0, m.cur)
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	require.Equal(t, 0, m.offset, "a step that fits does not scroll")
}

func TestSteps_TheStripSaysWhereTheReaderIs(t *testing.T) {
	m := testSteps(4)
	m.key("3")
	view := ansi.Strip(m.View())
	rows := strings.Split(view, "\n")
	require.Len(t, rows, m.height)
	require.Contains(t, rows[0], "Walkthrough")
	require.Contains(t, rows[0], "Your comments 1  2")
	require.Contains(t, rows[0], "Other changes 3  4")
	require.True(t, strings.HasSuffix(rows[0], "4 of 5 · met"), rows[0])
	require.Contains(t, m.tabs(100), reverse+" 3 ")
	require.Contains(t, rows[len(rows)-1], "f the whole change")
	require.Contains(t, rows[len(rows)-1], "%", "a long step says how far down it is")

	m.width = 44
	require.NotContains(t, ansi.Strip(m.strip()), "Your comments", "the group names go first when room is short")
	for _, row := range strings.Split(m.View(), "\n") {
		require.LessOrEqual(t, ansi.StringWidth(row), m.width)
	}
	wide := testSteps(9)
	wide.width = 40
	wide.key("5")
	strip := ansi.Strip(wide.strip())
	require.Contains(t, strip, " 5 ")
	require.Contains(t, strip, "…", "tabs that do not fit are left out either side of the one shown")
	require.LessOrEqual(t, ansi.StringWidth(strip), wide.width)
}

func TestSteps_ClosingKeys(t *testing.T) {
	m := testSteps(2)
	require.True(t, m.key("f"))
	require.Equal(t, "f", m.pressed)
	m = testSteps(2)
	require.True(t, m.key("q"))
	require.Empty(t, m.pressed)
	require.False(t, m.aborted)
	require.True(t, m.key("ctrl+c"))
	require.True(t, m.aborted)
	_, err := showSteps(forms.Steps{})
	require.ErrorIs(t, err, errNoSteps)
}

func TestSteps_FitAnyTerminal(t *testing.T) {
	for _, height := range []int{1, 2, 3, 4, 5, 6, 14} {
		m := testSteps(2)
		m.height = height
		m.key("1")
		rows := strings.Split(m.View(), "\n")
		require.Len(t, rows, height, "the frame is as tall as the terminal")
		require.Contains(t, ansi.Strip(m.View()), "line", "and shows the step from its top")
	}
	m := testSteps(1)
	m.s.Items[1].Body = "start " + strings.Repeat("word ", 30) + "end\nshort"
	m.key("1")
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 14})
	rows := m.lines()
	require.Greater(t, len(rows), 3, "a line laid out for a wider terminal goes on under itself")
	require.Contains(t, strings.Join(rows, " "), "end")
	for _, row := range rows {
		require.LessOrEqual(t, ansi.StringWidth(row), 40)
	}
	m.height = 5
	m.key("end")
	require.Equal(t, len(rows)-m.visible(), m.offset, "and scrolling reaches its last row")
}

func TestSteps_ATabOfTwoDigitsIsTyped(t *testing.T) {
	s := forms.Steps{Items: []forms.Step{{Tab: "Overview"}}}
	for i := 1; i <= 12; i++ {
		s.Items = append(s.Items, forms.Step{Tab: strconv.Itoa(i)})
	}
	m := &steps{s: s, width: 100, height: 14}
	m.key("1")
	require.Equal(t, 1, m.cur)
	m.key("2")
	require.Equal(t, 12, m.cur, "1 then 2 is the twelfth")
	m.key("3")
	require.Equal(t, 3, m.cur, "there is no 123: the digit starts again")
	m.key("1")
	m.key("0")
	require.Equal(t, 10, m.cur)
	m.key("right")
	m.key("0")
	require.Equal(t, 0, m.cur, "0 alone is the overview")
	m.key("1")
	m.key("down")
	m.key("1")
	require.Equal(t, 1, m.cur, "another key between them ends the number")
}

func TestSteps_DigitsAreKeptWhileTheyBeginATab(t *testing.T) {
	m := &steps{s: forms.Steps{Items: []forms.Step{{Tab: "Overview"}, {Tab: "123"}, {Tab: "100"}, {Tab: "001"}, {Tab: "7"}}}, width: 100, height: 14}
	for typed, want := range map[string]int{"123": 1, "100": 2, "001": 3, "7": 4, "97": 4, "1237": 4, "0": 0, "90": 0} {
		m.to(0)
		m.typed = ""
		if want == 0 {
			m.to(4)
		}
		for _, k := range typed {
			m.key(string(k))
		}
		require.Equal(t, want, m.cur, typed)
	}
}

func TestSteps_TheTabShownIsNeverCutFromTheStrip(t *testing.T) {
	s := forms.Steps{Title: "Changes since your review", Items: []forms.Step{{Tab: "Overview"}}}
	for i := 1; i <= 12; i++ {
		s.Items = append(s.Items, forms.Step{Tab: strconv.Itoa(i), Status: "answers your comment"})
	}
	m := &steps{s: s, width: 100, height: 14}
	m.to(12)
	for width := 100; width >= 6; width-- {
		m.width = width
		strip := ansi.Strip(m.strip())
		require.Contains(t, strip, " 12 ", "at width %d", width)
		require.LessOrEqual(t, ansi.StringWidth(strip), width)
	}
	m.width = 60
	require.Contains(t, ansi.Strip(m.strip()), "13 of 13", "the count stays while there is room for it")
}
