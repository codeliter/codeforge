package src

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const explainReply = "Adds a retry to the uploader, so a dropped connection no longer loses the file.\n\n" +
	"- `upload.go:40` — Wraps the send in `retry`, three times at most.\n" +
	"- `retry.go:12` — Waits longer after each failure.\n" +
	"Decision: retried in the client instead of the queue, because the queue has no idea of a partial upload.\n" +
	"- Decision: three tries instead of a setting, because nothing else here is configurable.\n" +
	"Outside the task: renamed `send` to `sendOnce`.\n" +
	"Not verified: a server that closes the connection mid-body.\n" +
	"Question: What happens when the third try fails? — The error of the last try is returned, wrapped.\n" +
	"Question: Is a 400 retried? No: only errors of the connection are.\n"

func TestParseExplanation(t *testing.T) {
	e, err := parseExplanation(explainReply)
	require.NoError(t, err)
	require.Equal(t, "Adds a retry to the uploader, so a dropped connection no longer loses the file.", e.Overview)
	require.Len(t, e.Stops, 2)
	require.Equal(t, "Waits longer after each failure.", e.Stops[1].Text, "the explanation's own lines are no part of a stop's note")
	require.Equal(t, []string{
		"retried in the client instead of the queue, because the queue has no idea of a partial upload.",
		"three tries instead of a setting, because nothing else here is configurable.",
	}, e.Decisions)
	require.Equal(t, "renamed `send` to `sendOnce`.", e.Outside)
	require.Equal(t, "a server that closes the connection mid-body.", e.Unverified)
	require.Equal(t, []checkQuestion{
		{Ask: "What happens when the third try fails?", Answer: "The error of the last try is returned, wrapped."},
		{Ask: "Is a 400 retried?", Answer: "No: only errors of the connection are."},
	}, e.Questions)

	e, err = parseExplanation("Small.\n- a.go:1 — x\nOutside the task: none.\nNot verified: None\n")
	require.NoError(t, err)
	require.Empty(t, e.Outside)
	require.Empty(t, e.Unverified)
	_, err = parseExplanation("Decision: nothing to show")
	require.ErrorIs(t, err, errNoWalkthrough)
}

func TestExplanationSteps_AddWhatAReaderOfTheirOwnChangeNeeds(t *testing.T) {
	e, err := parseExplanation(explainReply)
	require.NoError(t, err)
	s := explanationSteps(e, codeStyle{width: 100}, nil)
	require.Equal(t, "Your change", s.Title)
	var tabs []string
	for _, it := range s.Items {
		tabs = append(tabs, it.Tab)
	}
	require.Equal(t, []string{"Overview", "1", "2", "Decisions", "Gaps", "Check", "Answers"}, tabs)
	require.Contains(t, s.Items[3].Body, "1. retried in the client instead of the queue")
	require.Contains(t, s.Items[4].Body, "Outside the task: renamed send to sendOnce.")
	require.Contains(t, s.Items[4].Body, "Not verified: a server that closes the connection mid-body.")
	require.Contains(t, s.Items[5].Body, "2. Is a 400 retried?")
	require.NotContains(t, s.Items[5].Body, "only errors of the connection", "the questions come without their answers")
	require.Contains(t, s.Items[6].Body, "No: only errors of the connection are.")

	bare, err := parseExplanation("Small.\n- a.go:1 — x\n")
	require.NoError(t, err)
	require.Len(t, explanationSteps(bare, codeStyle{width: 100}, nil).Items, 2, "a part there is nothing for is left out")

	text := explanationText(e)
	for _, want := range []string{"1. upload.go:40 — Wraps the send", "Decisions\n- retried", "Outside the task:", "Not verified:", "Check yourself\n- What happens"} {
		require.Contains(t, text, want)
	}
}

func TestRunExplain_AsksTheSessionsAgentInAReadOnlyFork(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	engine, got := filepath.Join(dir, "engine"), filepath.Join(dir, "args")
	reply, err := json.Marshal(map[string]string{"output": explainReply})
	require.NoError(t, err)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + got + "\nprintf '%s\\n' \"$CODEFORGE_GUARD\" >> " + got + "\nprintf '%s' '" + strings.ReplaceAll(string(reply), "'", "'\\''") + "'\n"
	require.NoError(t, os.WriteFile(engine, []byte(script), 0o700))
	old, oldSandbox := explainSession, sandboxRepo
	t.Cleanup(func() { explainSession, sandboxRepo = old, oldSandbox })
	sandboxed := 0
	sandboxRepo = func(_ context.Context, argv []string) ([]string, error) { sandboxed++; return argv, nil }
	nameForks(t, forkPrefix+"x")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".empryo", "sessions", "work-1"), 0o700))
	asked := ""
	explainSession = func(_ context.Context, _, _, id string) (sessionInfo, error) {
		asked = id
		if id == "gone" {
			return sessionInfo{}, errNoWorkSession
		}
		return sessionInfo{ID: "work-1", Mode: modeWork, Title: "retry uploads"}, nil
	}

	var out bytes.Buffer
	require.NoError(t, runExplain(t.Context(), dir, engine, []string{"work"}, &out))
	require.Equal(t, "work", asked)
	require.GreaterOrEqual(t, sandboxed, 2, "the operating system holds the agent to reading, and the git that reads the diff")
	args, err := os.ReadFile(got)
	require.NoError(t, err)
	require.Equal(t, "--fork\nwork-1\n--session-id\n"+forkPrefix+"x\n--headless\n--json\n--profile\n"+profileFor(modeWork)+"\n--mode\nplan\n"+explainPrompt()+"\nreview\n", string(args),
		"a fork in plan mode under the review guard: the session is left as it was and nothing is changed")
	require.Contains(t, out.String(), "Not verified: a server that closes the connection mid-body.", "without a terminal to show steps on, it is printed")

	require.ErrorIs(t, runExplain(t.Context(), dir, engine, []string{"gone"}, &out), errNoWorkSession)
	require.ErrorIs(t, runExplain(t.Context(), dir, engine, []string{"a", "b"}, &out), errExplainUsage)
	require.ErrorIs(t, runExplain(t.Context(), dir, engine, []string{"--list"}, &out), errExplainUsage)
	require.Contains(t, commands, "explain")
}

func TestRunExplain_ASessionInTheMiddleOfATurnIsNotAsked(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	engine, ran := filepath.Join(dir, "engine"), filepath.Join(dir, "ran")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\n: > "+ran+"\n[ -e slow ] && exec sleep 30\nexit 1\n"), 0o700))
	old, oldSandbox := explainSession, sandboxRepo
	t.Cleanup(func() { explainSession, sandboxRepo = old, oldSandbox })
	sandboxRepo = func(_ context.Context, argv []string) ([]string, error) { return argv, nil }
	explainSession = func(context.Context, string, string, string) (sessionInfo, error) {
		return sessionInfo{ID: "work-1", Dir: dir, Mode: modeWork, Title: "retry uploads"}, nil
	}
	session := filepath.Join(dir, ".empryo", "sessions", "work-1")
	require.NoError(t, os.MkdirAll(session, 0o700))
	write := func(name, content string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(session, name), []byte(content), 0o600))
	}
	const done = `{"k":"meta"}` + "\n" + `{"k":"user"}` + "\n" + `{"k":"tool-start"}` + "\n" + `{"k":"assistant"}` + "\n"
	running := done + `{"k":"user"}` + "\n" + `{"k":"tool-start","name":"shell","args":{"command":"codeforge explain"}}` + "\n"
	alive := fmt.Sprintf(`{"pid":%d,"host":"tui"}`, os.Getpid())
	gone := exec.CommandContext(t.Context(), "true")
	require.NoError(t, gone.Run())

	var out bytes.Buffer
	write("session.jsonl", running)
	write("writer.lock", alive)
	err := runExplain(t.Context(), dir, engine, nil, &out)
	require.ErrorIs(t, err, errSessionWorking, "a fork would wait for the turn, and the turn may be waiting for this command")
	require.ErrorContains(t, err, `the session "retry uploads" is in the middle of a turn`)
	require.NoFileExists(t, ran, "the engine is not started to wait out the limit")

	for name, files := range map[string][2]string{
		"its turn is over":         {done, alive},
		"its turn was stopped":     {running + `{"k":"turn-stop"}` + "\n", alive},
		"its engine died mid-turn": {running, fmt.Sprintf(`{"pid":%d}`, gone.Process.Pid)},
		"its lock cannot be read":  {running, "{"},
	} {
		write("session.jsonl", files[0])
		write("writer.lock", files[1])
		require.NoError(t, os.RemoveAll(ran))
		err = runExplain(t.Context(), dir, engine, nil, &out)
		require.NotErrorIs(t, err, errSessionWorking, name)
		require.NotErrorIs(t, err, errExplainSlow, "an engine that fails in time is not called slow")
		require.FileExists(t, ran, name)
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "slow"), nil, 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err = runExplain(ctx, dir, engine, nil, &out)
	require.ErrorIs(t, err, errExplainSlow, "an engine stopped at the limit is said to be that, not a killed process")
}

func TestTicketPlan_IsAgreedBeforeAnyCodeIsWritten(t *testing.T) {
	old, oldSandbox := planRunner, sandboxRepo
	t.Cleanup(func() { planRunner, sandboxRepo = old, oldSandbox })
	sandboxed := 0
	sandboxRepo = func(_ context.Context, argv []string) ([]string, error) { sandboxed++; return argv, nil }
	var ran []string
	planRunner = func(_ context.Context, argv, env []string) ([]byte, error) {
		ran = argv
		require.Contains(t, env, "CODEFORGE_GUARD=review")
		return []byte(`{"output":"Understanding: add a retry.\nFiles:\n- upload.go — the send"}`), nil
	}
	args := []string{"--model", "m", "--effort", "high", "add a retry to uploads"}

	var shown bytes.Buffer
	got, err := ticketPlan(t.Context(), "empryo", args, strings.NewReader("\n"), &shown)
	require.NoError(t, err)
	require.Equal(t, []string{"empryo", "--profile", profileFor(modeTicket), "--mode", "plan", "--headless", "--json", "--model", "m", "--effort", "high",
		ticketPlanPrompt("add a retry to uploads")}, ran)
	require.Contains(t, shown.String(), "Understanding: add a retry.")
	require.Equal(t, 1, sandboxed)
	require.Equal(t, []string{"--model", "m", "--effort", "high"}, got[:4])
	require.Equal(t, "add a retry to uploads\n\nThe plan agreed with the user before work began. Follow it:\nUnderstanding: add a retry.\nFiles:\n- upload.go — the send", got[4])

	got, err = ticketPlan(t.Context(), "empryo", args, strings.NewReader("not the queue, only the client\n"), &shown)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(promptOf(got), "which wins wherever the two differ:\nnot the queue, only the client"))

	_, err = ticketPlan(t.Context(), "empryo", args, strings.NewReader("q\n"), &shown)
	require.ErrorIs(t, err, errPlanStopped)
	for _, ended := range []string{"", "go ahe"} {
		_, err = ticketPlan(t.Context(), "empryo", args, strings.NewReader(ended), &shown)
		require.ErrorIs(t, err, errPlanStopped, "input that ends without an answer agrees to nothing")
	}

	rest, found := takeFlag([]string{"--system", "--no-plan", "--no-plan", "fix retries"}, flagNoPlan)
	require.True(t, found)
	require.Equal(t, []string{"--system", "--no-plan", "fix retries"}, rest, "another flag's value is left alone")
	rest, found = takeFlag([]string{"--ticket", "--no-plan", "fix retries", "--no-plan"}, flagNoPlan)
	require.True(t, found)
	require.Equal(t, []string{"--ticket", "--no-plan", "fix retries"}, rest, "codeforge's own flags keep their values too")
	long, ok := planAnswer(strings.NewReader(strings.Repeat("x", 3*maxPlanAnswer) + "\nrest"))
	require.True(t, ok)
	require.Len(t, long, maxPlanAnswer)
	rest, found = takeFlag([]string{"--model=m", "task", "--effort"}, flagNoPlan)
	require.False(t, found)
	require.Equal(t, []string{"--model=m", "task", "--effort"}, rest)

	planRunner = func(context.Context, []string, []string) ([]byte, error) { return nil, errors.New("offline") } //nolint:err113 // a test's error
	shown.Reset()
	got, err = ticketPlan(t.Context(), "empryo", args, strings.NewReader(""), &shown)
	require.NoError(t, err)
	require.Equal(t, args, got, "a plan that cannot be had never holds the work up")
	require.Contains(t, shown.String(), "no plan (offline); going ahead without one")

	ran = nil
	got, err = ticketPlan(t.Context(), "empryo", []string{"--model", "m"}, strings.NewReader(""), &shown)
	require.NoError(t, err)
	require.Equal(t, []string{"--model", "m"}, got)
	require.Nil(t, ran, "with no task there is nothing to plan")
}

func TestWalkthrough_NamesTheExistingCodeToKnowFirst(t *testing.T) {
	reply := "Adds Ludo.\n\nKnow first: `app/Games/Handler.php:20-60` — The base every game's handler extends: it runs the turn loop.\n" +
		"- Know first: app/Games/Registry.php:8 — Where a game is registered.\n" +
		"- `app/Games/Ludo/LudoHandler.php:12` — Runs a Ludo turn.\n"
	w, err := parseWalkthrough(reply)
	require.NoError(t, err)
	require.Equal(t, "Adds Ludo.", w.Overview)
	require.Equal(t, []walkStop{
		{Path: "app/Games/Handler.php", Line: 20, End: 60, Text: "The base every game's handler extends: it runs the turn loop."},
		{Path: "app/Games/Registry.php", Line: 8, Text: "Where a game is registered."},
	}, w.Background)
	require.Len(t, w.Stops, 1)
	code := func(file string, _ focus) string { return "  " + file + " code" }
	s := walkthroughSteps(w, codeStyle{width: 100}, code, false)
	require.Equal(t, []string{"Overview", "Know first", "1"}, []string{s.Items[0].Tab, s.Items[1].Tab, s.Items[2].Tab})
	require.Contains(t, s.Items[1].Body, "app/Games/Handler.php:20-60\nThe base every game's handler extends")
	require.Contains(t, s.Items[1].Body, "app/Games/Registry.php code")
	a := walkthroughView(w, codeStyle{width: 100}, code)
	require.Equal(t, "Know first", a.Items[0].Title)
	require.Equal(t, "1. app/Games/Ludo/LudoHandler.php:12", a.Items[1].Title)
	require.Equal(t, []string{"app/Games/Handler.php", "app/Games/Registry.php", "app/Games/Ludo/LudoHandler.php"}, findingPaths(walkthroughText(w)), "its code is fetched with the stops'")
	require.Contains(t, walkthroughPrompt(), "Know first: path:line")
}
