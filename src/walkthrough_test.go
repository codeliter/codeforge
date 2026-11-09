package src

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestStartWalkthrough_WritesItInAForkInTheBackground(t *testing.T) {
	dir := t.TempDir()
	engine, got := filepath.Join(dir, "engine"), filepath.Join(dir, "args")
	reply, err := json.Marshal(map[string]string{"output": walkReply})
	require.NoError(t, err)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + got + "\nprintf '%s' '" + strings.ReplaceAll(string(reply), "'", "'\\''") + "'\n"
	require.NoError(t, os.WriteFile(engine, []byte(script), 0o700))
	job := startWalkthrough(t.Context(), []string{engine, "--profile", "codeforge-review", "--session-id", "rev-1", "--save-session", "--headless", "brief"}, os.Environ())
	defer job.stop()
	<-job.done
	require.NoError(t, job.err)
	require.Len(t, job.w.Stops, 3)
	args, err := os.ReadFile(got)
	require.NoError(t, err)
	require.Equal(t, "--fork\nrev-1\n--profile\ncodeforge-review\n--headless\n"+walkthroughPrompt()+"\n", string(args),
		"a fork of the review's session, so the session itself stays free for questions and the chat")

	old := showAccordion
	t.Cleanup(func() { showAccordion = old })
	shown := 0
	showAccordion = func(string, forms.Accordion) (string, []forms.Change, error) { shown++; return "", nil, nil }
	showWalkthrough(t.Context(), job)
	showWalkthrough(t.Context(), job)
	require.Equal(t, 2, shown, "once written, it opens at once, as often as asked")
}

func TestStartWalkthrough_StopEndsTheTurnAndAFailureIsSaid(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "engine")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\nsleep 30\n"), 0o700))
	job := startWalkthrough(t.Context(), []string{engine, "--session-id", "rev-1", "brief"}, os.Environ())
	start := time.Now()
	job.stop()
	<-job.done
	require.Less(t, time.Since(start), 5*time.Second, "stopping kills the engine")
	require.Error(t, job.err)

	_, stderr, err := captured(t, func() error { showWalkthrough(t.Context(), job); return nil })
	require.NoError(t, err)
	require.Contains(t, stderr, "no walkthrough:")

	none := startWalkthrough(t.Context(), []string{engine, "brief"}, os.Environ())
	<-none.done
	require.ErrorIs(t, none.err, errSessionMissing, "with no session there is nothing to fork")
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
