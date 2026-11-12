package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
)

func testPicker(multi bool) *picker {
	f := forms.Field{
		Key: "answer", Kind: forms.Search, Title: "Review model", Description: "5 models · 2 providers", Header: "Model\tContext",
		Multi: multi,
		Options: []forms.Option{
			{Label: "claude-opus-5-5", Value: "claude-opus-5-5", Group: "subscriptions", Detail: "1M"},
			{Label: "claude-sonnet-5-5", Value: "claude-sonnet-5-5", Group: "subscriptions", Detail: "1M"},
			{Label: "gpt-6.1-sol", Value: "gpt-6.1-sol", Group: "subscriptions", Detail: "1.1M"},
			{Label: "kimi-k3-fast", Value: "llmgateway/kimi-k3-fast", Group: "llmgateway", Detail: "1M"},
			{Label: "gpt-6.1-sol", Value: "llmgateway/gpt-6.1-sol", Group: "llmgateway", Detail: "1.1M"},
		},
	}
	if multi {
		f.Values = []string{"llmgateway/kimi-k3-fast"}
	} else {
		f.Value = "gpt-6.1-sol"
	}
	m := newPicker(f)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

func pickerKeys(m *picker, s string) { m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }

func pickerValues(m *picker) []string {
	var got []string
	for _, r := range m.rows {
		if r.opt >= 0 {
			got = append(got, m.f.Options[r.opt].Value)
		}
	}
	return got
}

func TestGrouped(t *testing.T) {
	require.False(t, grouped(forms.Field{Options: []forms.Option{{Label: "a", Value: "a"}}}))
	require.True(t, grouped(forms.Field{Options: []forms.Option{{Label: "a", Value: "a", Group: "g"}}}))
	require.True(t, grouped(forms.Field{Multi: true}))
}

func TestPicker_ShowsProvidersAsGroupsWithTheCurrentModelTicked(t *testing.T) {
	m := testPicker(false)
	v := ansi.Strip(m.View())
	require.Contains(t, v, "Review model")
	require.Contains(t, v, "5 models · 2 providers")
	require.Contains(t, v, "Model              Context", "the header's columns sit over the rows' columns")
	require.Contains(t, v, "▾ subscriptions  3")
	require.Contains(t, v, "▾ llmgateway  2")
	require.Contains(t, v, "├─   claude-opus-5-5    1M")
	require.Contains(t, v, "▌ └─ ✓ gpt-6.1-sol        1.1M", "the current model is ticked and starts under the cursor")
	require.Contains(t, v, "└─   gpt-6.1-sol        1.1M", "the same name on another provider is not ticked")
	require.Len(t, splitLines(m.View()), 24, "the view fills the screen and no more")
}

func TestPicker_TypingSearchesProviderAndModel(t *testing.T) {
	tests := []struct {
		typed string
		want  []string
	}{
		{typed: "kimi", want: []string{"llmgateway/kimi-k3-fast"}},
		{typed: "gateway/sol", want: []string{"llmgateway/gpt-6.1-sol"}},
		{typed: "SUB claude", want: []string{"claude-opus-5-5", "claude-sonnet-5-5"}},
		{typed: "nothing like it", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.typed, func(t *testing.T) {
			m := testPicker(false)
			pickerKeys(m, tt.typed)
			require.Equal(t, tt.want, pickerValues(m))
		})
	}
}

func TestPicker_EnterTakesAnOptionAndFoldsAGroup(t *testing.T) {
	m := testPicker(false)
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	require.Equal(t, "claude-sonnet-5-5", m.chosen)

	m = testPicker(false)
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	require.Equal(t, "llmgateway", m.rows[m.cursor].group)
	require.Negative(t, m.rows[m.cursor].opt, "tab lands on the next provider's heading")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Nil(t, cmd, "enter on a heading folds it and stays")
	require.Len(t, pickerValues(m), 3)
	require.Contains(t, ansi.Strip(m.View()), "▸ llmgateway  2")
	pickerKeys(m, "kimi")
	require.Equal(t, []string{"llmgateway/kimi-k3-fast"}, pickerValues(m), "a search looks inside a folded group")
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	require.Equal(t, 0, m.cursor, "tab goes round the headings")
}

func TestPicker_SeveralAreTickedThenSent(t *testing.T) {
	m := testPicker(true)
	require.Contains(t, ansi.Strip(m.View()), "ctrl+s done")
	require.Equal(t, "llmgateway/kimi-k3-fast", m.f.Options[m.rows[m.cursor].opt].Value)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Nil(t, cmd, "enter ticks, it does not leave")
	require.Empty(t, m.picked)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.Nil(t, cmd, "nothing ticked cannot be sent")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)
	require.Equal(t, map[string]bool{"llmgateway/kimi-k3-fast": true, "llmgateway/gpt-6.1-sol": true}, m.picked)
}

func TestPicker_EscClearsThenLeaves(t *testing.T) {
	m := testPicker(false)
	pickerKeys(m, "kimi")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.Empty(t, m.input.Value())
	require.False(t, m.aborted)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)
	require.True(t, m.aborted)
}

func TestPicker_AnOrderedPickerNumbersTheTicksAndAnswersInThatOrder(t *testing.T) {
	m := newPicker(forms.Field{
		Key: "answer", Kind: forms.Search, Title: "Provider order", Multi: true, Ordered: true, Values: []string{"b"},
		Options: []forms.Option{
			{Label: "a", Value: "a", Group: "Connected providers", Detail: "serves every saved model"},
			{Label: "b", Value: "b", Group: "Connected providers"},
			{Label: "c", Value: "c", Group: "Connected providers"},
		},
	})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	require.Equal(t, []string{"b"}, m.values())
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // c
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // a
	require.Equal(t, []string{"b", "c", "a"}, m.values(), "the order ticked, not the order listed")
	v := ansi.Strip(m.View())
	require.Contains(t, v, "3 a")
	require.Contains(t, v, "1 b")
	require.Contains(t, v, "2 c")
	require.Contains(t, v, "tick in order, or untick")
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // untick b
	require.Equal(t, []string{"c", "a"}, m.values())
	require.Contains(t, ansi.Strip(m.View()), "1 c", "the rest move up")
}

func TestPicker_ASavedValueThatIsNoLongerOfferedIsNotAnswered(t *testing.T) {
	m := newPicker(forms.Field{
		Key: "answer", Kind: forms.Search, Multi: true, Ordered: true, Values: []string{"gone", "b", "a"},
		Options: []forms.Option{{Label: "a", Value: "a", Group: "g"}, {Label: "b", Value: "b", Group: "g"}},
	})
	require.Equal(t, []string{"b", "a"}, m.values(), "the saved order of what is still offered stands")
	require.NotContains(t, m.picked, "gone")
}

func TestPicker_SearchesTheDetailAndUsesTheWholeWidthWithoutOne(t *testing.T) {
	m := testPicker(false)
	pickerKeys(m, "1.1m")
	require.Equal(t, []string{"gpt-6.1-sol", "llmgateway/gpt-6.1-sol"}, pickerValues(m))

	long := "today 10:00  chat  a title that is long enough to need most of the line it is on"
	m = newPicker(forms.Field{Key: "answer", Kind: forms.Search, Options: []forms.Option{{Label: long, Value: "0", Group: "Today"}}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	require.Contains(t, ansi.Strip(m.View()), long)
}
