package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
)

func testAccordion() *accordion {
	m := newAccordion(forms.Accordion{
		Intro: "Intro line.",
		Items: []forms.Section{{Title: "a.go", Summary: "1 issue", Body: "issue body"}, {Title: "b.go", Summary: "2 suggestions", Body: strings.Repeat("line\n", 40)}},
		Outro: "3 steps",
		Keys:  []forms.Action{{Key: "p", Label: "post on the PR"}, {Key: "c", Label: "chat"}},
	})
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	return m
}

// view is what the user reads, colours removed.
func view(m *accordion) string { return ansi.Strip(m.View()) }

func TestAccordion_StartsClosedAndOpensOnEnter(t *testing.T) {
	m := testAccordion()
	v := view(m)
	require.Contains(t, v, "Intro line.")
	require.Contains(t, v, "▸ a.go")
	require.Contains(t, v, "1 issue")
	require.NotContains(t, v, "issue body", "sections start closed")
	require.Contains(t, v, "p post on the PR · c chat")

	m.key("enter")
	require.Contains(t, view(m), "▾ a.go")
	require.Contains(t, view(m), "    issue body")
	m.key("enter")
	require.NotContains(t, view(m), "issue body", "enter closes it again")
}

func TestAccordion_MovesScrollsAndOpensAll(t *testing.T) {
	m := testAccordion()
	m.key("down")
	require.Equal(t, 1, m.cursor)
	m.key("down")
	require.Equal(t, 1, m.cursor, "the cursor stops at the last section")
	m.key("a")
	require.True(t, m.open[0] && m.open[1])
	m.key("pgdown")
	require.Positive(t, m.offset, "a long open section scrolls")
	require.Contains(t, view(m), "%", "the position shows when the view is longer than the screen")
	m.key("a")
	require.False(t, m.open[0] || m.open[1], "a closes all when all are open")
	m.key("home")
	require.Equal(t, 0, m.cursor)
	require.Equal(t, 0, m.offset)
}

// click presses the left button on screen row y.
func click(m *accordion, y int) {
	m.Update(tea.MouseMsg{X: 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
}

func TestAccordion_AClickOnARowOpensAndClosesIt(t *testing.T) {
	m := testAccordion()
	// Rows: 0 intro, 1 blank, 2 a.go, 3 b.go.
	click(m, 3)
	require.Equal(t, 1, m.cursor, "the clicked row takes the cursor")
	require.True(t, m.open[1])
	require.Contains(t, view(m), "▾ b.go")
	click(m, 3)
	require.False(t, m.open[1], "a second click closes it")

	click(m, 0)
	require.False(t, m.open[0] || m.open[1], "a click off a row does nothing")
	click(m, 2)
	require.True(t, m.open[0])
	require.Equal(t, 0, m.cursor)
	m.Update(tea.MouseMsg{Y: 2, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	require.True(t, m.open[0], "only the press counts, so one click toggles once")
}

func TestAccordion_TheWheelScrollsWithoutSnappingBack(t *testing.T) {
	m := testAccordion()
	m.key("a")
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	require.Equal(t, 2*wheelLines, m.offset, "the cursor's row may leave the screen while reading")
	m.key("pgdown")
	require.Greater(t, m.offset, 2*wheelLines)
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	before := m.offset
	m.key("down")
	require.NotEqual(t, before, m.offset, "moving the cursor brings its row back into view")
}

func TestAccordion_KeysThatEnd(t *testing.T) {
	m := testAccordion()
	require.NotNil(t, m.key("q"))
	require.Empty(t, m.pressed)
	for _, k := range []string{"p", "c"} {
		m = testAccordion()
		require.NotNil(t, m.key(k))
		require.Equal(t, k, m.pressed)
	}
	m = testAccordion()
	require.NotNil(t, m.key("ctrl+c"))
	require.True(t, m.aborted)
	require.Nil(t, testAccordion().key("x"), "other keys do nothing")
}

func TestAccordion_NoActionKeysWithoutKeys(t *testing.T) {
	m := newAccordion(forms.Accordion{Items: []forms.Section{{Title: "a"}}})
	require.NotContains(t, view(m), "post")
	require.Nil(t, m.key("p"))
}
