package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
)

func testSearch() *search {
	m := newSearch(forms.Field{
		Key: "answer", Kind: forms.Search, Title: "Open a session", Description: "3 sessions", Header: "WHEN  MODE  TITLE",
		Placeholder: "Type to search",
		Options: []forms.Option{
			{Label: "today 10:00  chat    Provider intersection", Value: "a"},
			{Label: "yesterday    review* Review PR #3", Value: "b"},
			{Label: "Fri 15:00    ticket  SQLite session", Value: "c"},
		},
		Value: "b",
	})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	return m
}

func typeKeys(m *search, s string) { m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }

func TestSearch_TheBoxIsShownBeforeAnythingIsTyped(t *testing.T) {
	m := testSearch()
	v := ansi.Strip(m.View())
	require.Contains(t, v, "┌─ Search ─")
	require.Contains(t, v, "Type to search")
	require.Contains(t, v, "WHEN  MODE  TITLE")
	require.Contains(t, v, "▌ yesterday    review* Review PR #3", "the offered value starts under the cursor")
	require.Contains(t, v, "\n  today 10:00", "the other rows start in the same column")
	require.Contains(t, v, "2 of 3")
}

func TestSearch_TypingNarrowsByEveryWord(t *testing.T) {
	tests := []struct {
		typed string
		want  []string
	}{
		{typed: "review", want: []string{"b"}},
		{typed: "SQLITE fri", want: []string{"c"}},
		{typed: "session chat", want: nil},
		{typed: "", want: []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.typed, func(t *testing.T) {
			m := testSearch()
			typeKeys(m, tt.typed)
			var got []string
			for _, i := range m.matches {
				got = append(got, m.f.Options[i].Value)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSearch_KeysPickClearAndLeave(t *testing.T) {
	m := testSearch()
	typeKeys(m, "e")
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, 1, m.cursor)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	require.Equal(t, m.f.Options[m.matches[1]].Value, m.chosen)

	m = testSearch()
	typeKeys(m, "nothing like it")
	require.Contains(t, ansi.Strip(m.View()), "Nothing matches")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Nil(t, cmd, "enter with nothing listed does nothing")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.Empty(t, m.input.Value(), "esc clears the search first")
	require.False(t, m.aborted)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)
	require.True(t, m.aborted, "a second esc leaves")
}

func TestSearch_ALongListScrollsWithTheCursor(t *testing.T) {
	var options []forms.Option
	for i := range 50 {
		options = append(options, forms.Option{Label: "session " + string(rune('A'+i%26)), Value: string(rune('a' + i%26))})
	}
	m := newSearch(forms.Field{Key: "answer", Kind: forms.Search, Options: options})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	for range 30 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	require.Equal(t, 30, m.cursor)
	require.LessOrEqual(t, m.offset, 30)
	require.Greater(t, m.offset+m.rows(), 30, "the cursor's option is on screen")
	require.Equal(t, 20, len(splitLines(m.View())), "the view fills the screen and no more")
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
