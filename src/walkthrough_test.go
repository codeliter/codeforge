package src

import (
	"context"
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
	require.Equal(t, focus{Line: 520, End: 540, Whole: true}, asked[1], "a stop is shown with every other place of its file the change touches")
	same := []walkStop{{Path: "a.go", Line: 5}, {Path: "b.go", Line: 1}, {Path: "./a.go", Line: 40}}
	require.Equal(t, focus{Whole: true, Stops: []int{40}}, stopFocus(same, 0), "but for the places other stops are about")
	require.Equal(t, focus{Reach: true}, stopFocus(same, 2), "and only at the file's first stop, so no change is read twice")
	require.Equal(t, []string{"app/Games/Ludo/Support/LudoBoard.php", "app/Games/Ludo/LudoHandler.php", "resources/js/Components/Session/Stage/LudoStage.vue"}, findingPaths(walkthroughText(w)))
}

func TestStartWalkthrough_WritesItInAForkInTheBackground(t *testing.T) {
	dir := t.TempDir()
	engine, got := filepath.Join(dir, "engine"), filepath.Join(dir, "args")
	reply, err := json.Marshal(map[string]string{"output": walkReply})
	require.NoError(t, err)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + got + "\nprintf '%s' '" + strings.ReplaceAll(string(reply), "'", "'\\''") + "'\n"
	require.NoError(t, os.WriteFile(engine, []byte(script), 0o700))
	t.Chdir(dir) // the fork is marked where the engine runs, beside the session it forks
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".empryo", "sessions", "rev-1"), 0o700))
	nameForks(t, forkPrefix+"x")
	job := startWalkthrough(t.Context(), []string{engine, "--profile", "codeforge-review", "--session-id", "rev-1", "--save-session", "--headless", "brief"}, os.Environ())
	defer job.stop()
	<-job.done
	require.NoError(t, job.err)
	require.Len(t, job.w.Stops, 3)
	args, err := os.ReadFile(got)
	require.NoError(t, err)
	require.Equal(t, "--fork\nrev-1\n--session-id\n"+forkPrefix+"x\n--profile\ncodeforge-review\n--headless\n"+walkthroughPrompt()+"\n", string(args),
		"a fork of the review's session, so the session itself stays free for questions and the chat; named, so it is removed after")

	old, oldSteps := showAccordion, showSteps
	t.Cleanup(func() { showAccordion, showSteps = old, oldSteps })
	// A forms helper too old for steps: the accordion it knows is shown instead.
	showSteps = func(string, forms.Steps) (string, error) { return "", forms.ErrOldHelper }
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

func TestWalkthroughSteps_OneStopAScreenUnderAnOverview(t *testing.T) {
	w, err := parseWalkthrough(walkReply)
	require.NoError(t, err)
	code := func(file string, _ focus) string { return "  " + file + " code" }
	s := walkthroughSteps(w, codeStyle{width: 100}, code, false)
	require.Equal(t, "Walkthrough", s.Title)
	require.Empty(t, s.Keys)
	require.Len(t, s.Items, 4)
	require.Equal(t, "Overview", s.Items[0].Tab)
	require.Contains(t, s.Items[0].Body, "Adds Ludo as a game")
	require.Contains(t, s.Items[0].Body, " 2  app/Games/Ludo/LudoHandler.php:520-540  Ends the game and gives the winner a…", "the overview lists the stops")
	require.Equal(t, "2", s.Items[2].Tab)
	require.True(t, strings.HasPrefix(s.Items[2].Body, "app/Games/Ludo/LudoHandler.php:520-540\nEnds the game"))
	require.Contains(t, s.Items[2].Body, "app/Games/Ludo/LudoHandler.php code")
	require.Empty(t, s.Items[2].Group)

	w, err = parseWalkthrough("Adds Ludo.\n- `a.php:40` — Ranks the players. Requirement: the finisher ranks first – not met: sorts by score only.\nNot covered: reconnects.")
	require.NoError(t, err)
	s = walkthroughSteps(w, codeStyle{width: 100}, nil, false)
	require.Equal(t, "not met", s.Items[1].Status, "the strip says whether the stop does what was asked")
	require.Contains(t, s.Items[0].Body, "Not covered by the change: reconnects.")
}

func TestFollowUpWalkthrough_GroupsTheStopsByWhatTheyAnswer(t *testing.T) {
	reply := "Two of the three comments were taken up, and the cache moved.\n\n" +
		"- `cache.go:12` — The cache now lives in its own type.\n" +
		"- `a.go:40` — The error is returned now. Answers: the swallowed error in `load`.\n" +
		"- `b.go:7` — The loop answers: stop, at the limit. answers: the unbounded retry\n" +
		"Not addressed: the missing test for an empty list.\n"
	w, err := parseWalkthrough(reply)
	require.NoError(t, err)
	asFollowUp(&w)
	require.Equal(t, []string{"a.go", "b.go", "cache.go"}, []string{w.Stops[0].Path, w.Stops[1].Path, w.Stops[2].Path}, "what answers a comment comes first")
	require.Equal(t, walkStop{Path: "a.go", Line: 40, Text: "The error is returned now.", Answers: "the swallowed error in `load`."}, w.Stops[0])
	require.Equal(t, "the unbounded retry", w.Stops[1].Answers)
	require.Equal(t, "the missing test for an empty list.", w.Unaddressed)

	s := walkthroughSteps(w, codeStyle{width: 100}, nil, true)
	require.Equal(t, "Changes since your review", s.Title)
	require.Equal(t, []forms.Action{{Key: keyWhole, Label: "the whole change"}}, s.Keys)
	require.Equal(t, []string{"", "Your comments", "Your comments", "Other changes"}, []string{s.Items[0].Group, s.Items[1].Group, s.Items[2].Group, s.Items[3].Group})
	require.Equal(t, "answers your comment", s.Items[1].Status)
	require.Empty(t, s.Items[3].Status)
	require.Contains(t, s.Items[1].Body, "Answers your comment: the swallowed error in load.")
	require.Contains(t, s.Items[0].Body, "Not addressed: the missing test for an empty list.")
	require.Contains(t, walkthroughView(w, codeStyle{width: 100}, nil).Intro, "Changes since your review")

	w, err = parseWalkthrough("x\n- `a.go:1` — y\nNot addressed: none.")
	require.NoError(t, err)
	require.Empty(t, w.Unaddressed)
}

func TestStartWalkthrough_AFollowUpRoundToursTheChangesSinceTheLastReview(t *testing.T) {
	dir := t.TempDir()
	engine, got := filepath.Join(dir, "engine"), filepath.Join(dir, "args")
	reply, err := json.Marshal(map[string]string{"output": "Changed.\n- a.go:40 — Returned now. Answers: the swallowed error"})
	require.NoError(t, err)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + got + "\nprintf '%s' '" + strings.ReplaceAll(string(reply), "'", "'\\''") + "'\n"
	require.NoError(t, os.WriteFile(engine, []byte(script), 0o700))
	oldFollow, oldSteps := followUpWalk, showSteps
	t.Cleanup(func() { followUpWalk, showSteps = oldFollow, oldSteps })
	followUpWalk = func(context.Context) bool { return true }
	t.Chdir(dir) // the fork is marked where the engine runs, beside the session it forks
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".empryo", "sessions", "rev-1"), 0o700))
	nameForks(t, forkPrefix+"x")

	job := startWalkthrough(t.Context(), []string{engine, "--session-id", "rev-1", "brief"}, os.Environ())
	defer job.stop()
	<-job.done
	require.NoError(t, job.err)
	require.True(t, job.w.FollowUp)
	require.Equal(t, "the swallowed error", job.w.Stops[0].Answers)
	args, err := os.ReadFile(got)
	require.NoError(t, err)
	require.Equal(t, "--fork\nrev-1\n--session-id\n"+forkPrefix+"x\n"+followUpWalkPrompt()+"\n", string(args))

	var titles []string
	showSteps = func(_ string, s forms.Steps) (string, error) {
		titles = append(titles, s.Title)
		if len(titles) == 1 {
			return keyWhole, nil
		}
		return "", nil
	}
	showWalkthrough(t.Context(), job)
	require.Equal(t, []string{"Changes since your review", "Walkthrough"}, titles, "f goes on to the tour of the whole change")
	args, err = os.ReadFile(got)
	require.NoError(t, err)
	require.Contains(t, string(args), walkthroughPrompt(), "written when first asked for")
	whole := job.wholeJob
	showWalkthrough(t.Context(), job)
	require.Same(t, whole, job.wholeJob, "and written once")

	// A forms helper too old for steps offers the same key on the accordion.
	oldAccordion := showAccordion
	t.Cleanup(func() { showAccordion = oldAccordion })
	showSteps = func(string, forms.Steps) (string, error) { return "", forms.ErrOldHelper }
	var keys [][]forms.Action
	showAccordion = func(_ string, a forms.Accordion) (string, []forms.Change, error) {
		keys = append(keys, a.Keys)
		if len(keys) == 1 {
			return keyWhole, nil, nil
		}
		return "", nil, nil
	}
	showWalkthrough(t.Context(), job)
	require.Equal(t, [][]forms.Action{{{Key: keyWhole, Label: "the whole change", Footer: true}}, nil}, keys)
}
