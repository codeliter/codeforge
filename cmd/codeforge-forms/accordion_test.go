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
		Intro:      "Intro line.",
		Items:      []forms.Section{{Title: "a.go", Summary: "1 issue", Body: "issue body"}, {Title: "b.go", Summary: "2 suggestions", Body: strings.Repeat("line\n", 40)}},
		Outro:      "3 steps",
		Confirm:    "post on the PR",
		ConfirmKey: "p",
		Accent:     "",
		Dim:        "",
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
	require.Contains(t, v, "p post on the PR")

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

func TestAccordion_KeysThatEnd(t *testing.T) {
	m := testAccordion()
	require.NotNil(t, m.key("q"))
	require.False(t, m.confirmed)
	m = testAccordion()
	require.NotNil(t, m.key("p"))
	require.True(t, m.confirmed)
	m = testAccordion()
	require.NotNil(t, m.key("ctrl+c"))
	require.True(t, m.aborted)
	require.Nil(t, testAccordion().key("x"), "other keys do nothing")
}

func TestAccordion_NoConfirmKeyWithoutConfirm(t *testing.T) {
	m := newAccordion(forms.Accordion{Items: []forms.Section{{Title: "a"}}})
	require.NotContains(t, view(m), "post")
	require.Nil(t, m.key("p"))
}
