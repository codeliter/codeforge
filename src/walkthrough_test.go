package src

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
)

const walkReply = "Adds Ludo as a game: a rules engine, a handler that runs turns, and the stage that shows them.\n\n" +
	"- `app/Games/Ludo/Support/LudoBoard.php:40` — The board: moves, captures and blocks, with no I/O.\n" +
	"2. app/Games/Ludo/LudoHandler.php:520-540 – Ends the game and gives the winner a bonus;\n" +
	"   the recap reads the order from here.\n" +
	"- resources/js/Components/Session/Stage/LudoStage.vue:12: draws the board from the handler's payload."

func TestParseWalkthrough(t *testing.T) {
	w, err := parseWalkthrough(walkReply)
	require.NoError(t, err)
	require.Equal(t, "Adds Ludo as a game: a rules engine, a handler that runs turns, and the stage that shows them.", w.Overview)
	require.Equal(t, []walkStop{
		{Path: "app/Games/Ludo/Support/LudoBoard.php", Line: 40, Text: "The board: moves, captures and blocks, with no I/O."},
		{Path: "app/Games/Ludo/LudoHandler.php", Line: 520, End: 540, Text: "Ends the game and gives the winner a bonus; the recap reads the order from here."},
		{Path: "resources/js/Components/Session/Stage/LudoStage.vue", Line: 12, Text: "draws the board from the handler's payload."},
	}, w.Stops)
	_, err = parseWalkthrough("Nothing to walk through.")
	require.ErrorIs(t, err, errNoWalkthrough)
}

func TestWalkthroughView(t *testing.T) {
	w, err := parseWalkthrough(walkReply)
	require.NoError(t, err)
	var asked []focus
	code := func(file string, fc focus) string { asked = append(asked, fc); return "  " + file + " code" }
	a := walkthroughView(w, codeStyle{width: 100}, code)
	require.Contains(t, a.Intro, "Walkthrough")
	require.Contains(t, a.Intro, "q goes back to the review")
	require.Contains(t, a.Intro, "Adds Ludo as a game")
	require.Len(t, a.Items, 3)
	require.Equal(t, "2. app/Games/Ludo/LudoHandler.php:520-540", a.Items[1].Title)
	require.Equal(t, "Ends the game and gives the winner a…", a.Items[1].Summary)
	require.True(t, a.Items[0].Open)
	require.False(t, a.Items[1].Open)
	require.Contains(t, a.Items[1].Body, "app/Games/Ludo/LudoHandler.php code", "each stop shows its code")
	require.Equal(t, focus{Line: 520, End: 540}, asked[1])
	require.Equal(t, []string{"app/Games/Ludo/Support/LudoBoard.php", "app/Games/Ludo/LudoHandler.php", "resources/js/Components/Session/Stage/LudoStage.vue"}, findingPaths(walkthroughText(w)))
}

func TestShowWalkthrough_AsksOnceAndSaysWhenItFails(t *testing.T) {
	old := showAccordion
	t.Cleanup(func() { showAccordion = old })
	shown := 0
	showAccordion = func(string, forms.Accordion) (string, []forms.Change, error) { shown++; return "", nil, nil }
	turns := 0
	turn := func(_ context.Context, p string) (string, error) {
		turns++
		require.Contains(t, p, "not the review")
		return walkReply, nil
	}
	var w *walkthrough
	showWalkthrough(t.Context(), &w, turn)
	showWalkthrough(t.Context(), &w, turn)
	require.Equal(t, 1, turns, "the walkthrough is written once per review")
	require.Equal(t, 2, shown)

	var none *walkthrough
	_, stderr, err := captured(t, func() error {
		showWalkthrough(t.Context(), &none, func(context.Context, string) (string, error) { return "", errors.New("down") }) //nolint:err113 // a stand-in failure
		return nil
	})
	require.NoError(t, err)
	require.Contains(t, stderr, "no walkthrough: down")
	require.Nil(t, none)
	require.True(t, strings.Contains(walkthroughPrompt(), "path:line"))
}

func TestParseWalkthrough_Requirements(t *testing.T) {
	reply := "Adds Ludo.\n\n" +
		"- `a.php:10` — Ends the game with one player left. Requirement: end the game when one player remains — met: `advanceTurn` returns early.\n" +
		"- `a.php:40` — Ranks the players. Requirement: the finisher ranks first – not met: sorts by score only.\n" +
		"- `b.php:5` — Draws the board.\n" +
		"Not covered: a reconnect after a dropped socket.\n"
	w, err := parseWalkthrough(reply)
	require.NoError(t, err)
	require.Equal(t, walkStop{Path: "a.php", Line: 10, Text: "Ends the game with one player left.",
		Requirement: "end the game when one player remains", Status: "met", Why: "`advanceTurn` returns early"}, w.Stops[0])
	require.Equal(t, "not met", w.Stops[1].Status)
	require.Equal(t, "the finisher ranks first", w.Stops[1].Requirement)
	require.Empty(t, w.Stops[2].Requirement, "a stop no requirement is about has none")
	require.Equal(t, "a reconnect after a dropped socket.", w.Uncovered)

	a := walkthroughView(w, codeStyle{width: 100}, nil)
	require.Contains(t, a.Intro, "Not covered by the change: a reconnect after a dropped socket.")
	require.Contains(t, a.Items[1].Body, "Requirement: the finisher ranks first — not met: sorts by score only")
	require.True(t, strings.HasPrefix(a.Items[1].Summary, "not met · "), "the closed row says it too")

	w, err = parseWalkthrough("x\n- `a.go:1` — y\nNot covered: none.")
	require.NoError(t, err)
	require.Empty(t, w.Uncovered)
}
