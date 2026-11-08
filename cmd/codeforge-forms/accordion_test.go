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

// editAccordion is a review view where the user can mark changes: one file with two findings, then the final comment.
func editAccordion() *accordion {
	m := newAccordion(forms.Accordion{
		Items: []forms.Section{
			{Title: "a.go", Summary: "1 issue · 1 nitpick", Open: true, Notes: []forms.Note{
				{ID: "f0", Body: "- issue (blocking): a.go:3 samples everything", Raw: "issue (blocking): a.go:3 — samples everything", Label: "a.go:3"},
				{ID: "f1", Body: "- nitpick: a.go:9 wrap it", Raw: "nitpick: a.go:9 — wrap it", Label: "a.go:9"},
			}},
			{Title: "Final comment", Body: "Close."},
		},
		Keys:  []forms.Action{{Key: "1", Label: "post as Approve"}, {Key: "c", Label: "chat about it"}},
		Edits: true,
	})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return m
}

// typeText types s into the open input and presses Enter.
func typeText(m *accordion, s string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
}

func TestAccordion_TheCursorStopsOnEachFinding(t *testing.T) {
	m := editAccordion()
	require.Equal(t, 0, m.sub, "the cursor starts on the first finding, so e, x and ? work at once")
	require.Contains(t, view(m), "e edit  ·  x delete  ·  ? ask  ·  / ask about all  ·  p post  ·  q quit")
	require.Contains(t, view(m), "  ▌ - issue (blocking): a.go:3", "the picked finding is marked")
	m.key("up")
	require.Equal(t, -1, m.sub)
	require.Contains(t, view(m), "▌ ▾ a.go", "a file's row takes the same bar")
	require.Contains(t, view(m), "⏎ close  ·  ? ask  ·  / ask about all  ·  p post  ·  q quit", "on a file's row")
	m.key("down")
	m.key("down")
	require.Equal(t, 1, m.sub)
	m.key("down")
	require.Equal(t, 1, m.cursor, "past the last finding is the next section")
	require.Equal(t, -1, m.sub)
	m.key("up")
	require.Equal(t, 0, m.cursor)
	require.Equal(t, 1, m.sub, "up from a section lands on the last finding above")
	require.NotContains(t, view(m), "Approve", "the ways to finish wait behind p")
	m.key("p")
	require.Contains(t, view(m), "Post as\n  1 Approve\n  c chat about it\n  esc back")
	m.key("esc")
	require.False(t, m.picking)
	m.key("p")
	require.NotNil(t, m.key("1"))
	require.Equal(t, "1", m.pressed)
}

func TestAccordion_ChangesAreMarkedAndSentAtOnce(t *testing.T) {
	m := editAccordion()
	m.key("e")
	require.True(t, m.typing)
	require.Equal(t, "issue (blocking): a.go:3 — samples everything", m.input.Value(), "a rewrite starts from the finding as written")
	require.Contains(t, view(m), "┌─ ✎ Rewrite a.go:3 (its first line) ─", "the text goes in a box titled with what it is for")
	require.Contains(t, view(m), "│ issue (blocking): a.go:3 — samples everything")
	require.Contains(t, view(m), "⏎ save  ·  esc cancel")
	m.input.SetValue("")
	typeText(m, "suggestion: a.go:3 — sample less")
	require.Contains(t, view(m), "→ suggestion: a.go:3 — sample less   edited by you ", "the rewrite goes under the finding it replaces")
	m.key("?")
	require.Contains(t, view(m), "⏎ add  ·  esc cancel")
	typeText(m, "still blocking?")
	m.key("down")
	m.key("x")
	require.Contains(t, view(m), "✗ nitpick: a.go:9 — wrap it   deleted by you ", "a deleted finding is struck out, not gone")
	require.Contains(t, view(m), "a.go  1 issue · 1 nitpick  · 3 changes", "the file's row counts its changes")
	require.Contains(t, view(m), "x keep")
	m.key("/")
	require.Contains(t, view(m), "┌─ / Ask about the whole review ─")
	require.Contains(t, view(m), "Type your question, then press ⏎", "an empty box says what to type")
	typeText(m, "the retry path?")
	require.Contains(t, view(m), "/ About the whole review: the retry path?")
	require.Contains(t, view(m), "s send 4 changes  ·  u undo  ·  x keep", "sending leads the keys")
	m.key("u")
	require.Contains(t, view(m), "s send 3 changes")

	require.NotNil(t, m.key("s"))
	require.Equal(t, forms.KeySend, m.pressed)
	require.Equal(t, []forms.Change{
		{Kind: forms.ChangeEdit, Note: "f0", Text: "suggestion: a.go:3 — sample less"},
		{Kind: forms.ChangeAsk, Note: "f0", Text: "still blocking?"},
		{Kind: forms.ChangeDelete, Note: "f1"},
	}, m.changes)
}

func TestAccordion_UnsentChangesAreNeverLostByAccident(t *testing.T) {
	m := editAccordion()
	require.Nil(t, m.key("s"), "nothing to send yet")
	require.Contains(t, view(m), "Nothing to send yet")
	m.key("x")
	require.Nil(t, m.key("1"), "posting waits")
	require.Contains(t, view(m), "1 change not sent yet. Press s to send, or 1 again to go on without.")
	require.NotNil(t, m.key("1"), "pressed again, it goes ahead")
	require.Equal(t, "1", m.pressed)
	require.Empty(t, m.changes)

	m = editAccordion()
	m.key("x")
	m.key("p")
	require.Contains(t, view(m), "1 change not sent: picking below drops them.  s send them first", "the list says it first")
	require.NotNil(t, m.key("s"), "s sends from the list")
	require.Equal(t, forms.KeySend, m.pressed)

	m = editAccordion()
	m.key("x")
	m.key("p")
	require.NotNil(t, m.key("1"), "a pick from the list goes ahead: it said what is unsent")
	require.Equal(t, "1", m.pressed)

	m = editAccordion()
	m.key("x")
	require.Nil(t, m.key("q"))
	m.key("down") // any other key puts the notice away
	require.Nil(t, m.key("q"), "q must be pressed twice in a row")
	require.NotNil(t, m.key("q"))
}

func TestAccordion_EscCancelsTypingAndAnEmptyRewriteKeepsTheFinding(t *testing.T) {
	m := editAccordion()
	m.key("?")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("why")})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.False(t, m.typing)
	require.Empty(t, m.changes)
	m.key("e")
	m.input.SetValue("")
	typeText(m, "")
	require.Empty(t, m.changes, "an empty rewrite is no rewrite")
	m.key("e")
	typeText(m, "")
	require.Empty(t, m.changes, "a rewrite that changes nothing is no rewrite")
}

func TestAccordion_TheListGroupsTheWaysToFinish(t *testing.T) {
	m := editAccordion()
	m.a.Keys = []forms.Action{
		{Key: "1", Label: "post as Approve", Group: "Post as"},
		{Key: "4", Label: "post as separate comments", Group: "Or"},
		{Key: "c", Label: "chat about it", Group: "Or"},
	}
	m.key("p")
	require.Contains(t, view(m), "Post as\n  1 Approve\nOr\n  4 Separate comments\n  c Chat about it\n  esc back")
}

func TestAccordion_AClosedSectionStillCountsItsChanges(t *testing.T) {
	m := editAccordion()
	m.key("?")
	typeText(m, "why?")
	m.key("up")
	m.key("left")
	require.Contains(t, view(m), "▸ a.go  1 issue · 1 nitpick  · 1 change")
	m.key("?")
	typeText(m, "the whole file?")
	require.Contains(t, view(m), "? About a.go: the whole file?", "a question about a file names it")
}

func TestAccordion_WithNoSectionOpenTheFirstWithNotesOpens(t *testing.T) {
	m := newAccordion(forms.Accordion{Edits: true, Items: []forms.Section{
		{Title: "Earlier threads", Body: "none"},
		{Title: "a.go", Notes: []forms.Note{{ID: "f0", Body: "- nitpick: a.go:1 x"}}},
	}})
	require.True(t, m.open[1])
	require.Equal(t, 1, m.cursor)
	require.Equal(t, 0, m.sub)
}

func TestAccordion_DeletingARewriteKeepsTheRewrite(t *testing.T) {
	m := editAccordion()
	m.key("e")
	m.input.SetValue("")
	typeText(m, "suggestion: a.go:3 — sample less")
	m.key("x")
	require.Contains(t, view(m), "✗ issue (blocking): a.go:3")
	m.key("x")
	require.Contains(t, view(m), "→ suggestion: a.go:3 — sample less", "x again brings the rewrite back, not the original")
}

func TestAccordion_EnterOnAFindingClosesItsFileOnTheFilesRow(t *testing.T) {
	m := editAccordion()
	m.key("down")
	m.key("enter")
	require.False(t, m.open[0])
	require.Equal(t, 0, m.cursor)
	require.Equal(t, -1, m.sub, "the cursor is on the row it closed, as drawn")
	m.key("down")
	require.Equal(t, 1, m.cursor, "a closed file's findings are skipped")
}

func TestAccordion_TheFooterHeightMatchesTheFooter(t *testing.T) {
	steps := []struct {
		name string
		act  func(m *accordion)
	}{
		{name: "on a finding", act: func(*accordion) {}},
		{name: "with a notice", act: func(m *accordion) { m.key("s") }},
		{name: "typing", act: func(m *accordion) { m.key("?") }},
		{name: "a change marked", act: func(m *accordion) { m.Update(tea.KeyMsg{Type: tea.KeyEsc}); m.key("x") }},
		{name: "picking with unsent changes", act: func(m *accordion) { m.key("p") }},
		{name: "back from picking", act: func(m *accordion) { m.key("esc") }},
		{name: "an unsent notice", act: func(m *accordion) { m.key("q") }},
		{name: "after undo", act: func(m *accordion) { m.key("u") }},
	}
	m := editAccordion()
	for _, st := range steps {
		st.act(m)
		require.Len(t, m.footer(m.layout()), m.footerHeight(), st.name)
	}
}

func TestAccordion_NoActionKeysWithoutKeys(t *testing.T) {
	m := newAccordion(forms.Accordion{Items: []forms.Section{{Title: "a"}}})
	require.NotContains(t, view(m), "post")
	require.Nil(t, m.key("p"))
}

func TestAccordion_ANoteOfSeveralLinesIsRewrittenWhole(t *testing.T) {
	m := newAccordion(forms.Accordion{Edits: true, Items: []forms.Section{{Title: "Final comment", Open: true, Notes: []forms.Note{
		{ID: "final", Body: "Close.\n- Fix it.", Raw: "Close.\n- Fix it.", Label: "the final comment"},
	}}}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.key("e")
	require.True(t, m.multiline)
	require.Contains(t, view(m), "✎ Rewrite the final comment")
	require.Contains(t, view(m), "ctrl+s save  ·  ⏎ new line  ·  esc cancel")
	m.area.SetValue("Fine.")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("- Ship it.")})
	require.True(t, m.typing, "enter starts a new line")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.False(t, m.typing)
	require.Equal(t, []forms.Change{{Kind: forms.ChangeEdit, Note: "final", Text: "Fine.\n- Ship it."}}, m.changes)
	require.Contains(t, view(m), "→ Fine.")
	require.Contains(t, view(m), "  - Ship it.   edited by you ")
}
