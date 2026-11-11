package src

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/jobs"
)

func TestParseJob(t *testing.T) {
	now := time.Date(2026, 11, 11, 22, 0, 0, 0, time.UTC)
	j, err := parseJob([]string{"--name", "drain", "--until", "scripts/idle.sh", "--hold", "60m", "--while", "vpn eu", "--every", "1s", "--", "make", "drain"}, now)
	require.NoError(t, err)
	require.Equal(t, "'make' 'drain'", j.Command)
	require.Equal(t, time.Hour, j.Hold)
	require.Equal(t, jobs.MinEvery, j.Every, "checks are never run more often than MinEvery")
	require.Equal(t, now.Add(jobs.DefaultExpire), j.Expire)
	require.Len(t, j.Until, 1)
	require.Len(t, j.While, 1)

	j, err = parseJob([]string{"--at", "+2d", "--", "deploy"}, now)
	require.NoError(t, err)
	require.True(t, j.Expire.After(j.At), "a job set to start after the default expiry still gets its wait")

	for _, bad := range [][]string{
		{"--", "x"}, {"--at", "02:00"}, {"--hold", "1h", "--", "x"}, {"--bogus", "1", "--", "x"}, {"--every", "-1s", "--at", "+1m", "--", "x"},
	} {
		_, err := parseJob(bad, now)
		require.Error(t, err, bad)
	}
}

func TestScheduleRefusal(t *testing.T) {
	j := &jobs.Job{Command: "make", Until: []jobs.Check{{Command: "codeforge gate reset"}}}
	require.ErrorIs(t, scheduleRefusal(j), errJobGuard)
	require.NoError(t, scheduleRefusal(&jobs.Job{Command: "gh pr merge 42", While: []jobs.Check{{Command: "gh pr checks 42"}}}))
}

func TestRunJob_UnknownSubcommandDoesNotExist(t *testing.T) {
	require.ErrorIs(t, runJob(t.Context(), t.TempDir(), []string{"nope"}, &strings.Builder{}), errNoSuchCommand)
	require.ErrorIs(t, runJob(t.Context(), t.TempDir(), []string{"show"}, &strings.Builder{}), errJobUsage)
	t.Setenv("CODEFORGE_GUARD", "review")
	require.ErrorIs(t, runJob(t.Context(), t.TempDir(), []string{"add", "--at", "+1m", "--", "x"}, &strings.Builder{}), errJobInReview)
}

func TestJobCancel_WithoutARunnerRecordsIt(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Waiting, Command: "x"}))
	var out strings.Builder
	require.NoError(t, jobCancel(t.Context(), st, "j1", &out))
	j, err := st.Load("j1")
	require.NoError(t, err)
	require.Equal(t, jobs.Cancelled, j.State)
}

func TestPrintJobNotice_TellsOnceWhatHappenedWhileAway(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	now := time.Now()
	ws, elsewhere := t.TempDir(), t.TempDir()
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", Session: "29b12d1c-5", Workspace: ws, State: jobs.Done, Command: "make drain", Created: now}))
	require.NoError(t, st.Save(&jobs.Job{ID: "j2", Session: "29b12d1c-5", Workspace: ws, State: jobs.Waiting, Command: "deploy", Created: now, Expire: now.Add(time.Hour)}))
	require.NoError(t, st.Save(&jobs.Job{ID: "j3", Session: "other", State: jobs.Done, Command: "x", Created: now}))
	require.NoError(t, st.Save(&jobs.Job{ID: "j4", Session: "29b12d1c-5", Workspace: elsewhere, State: jobs.Done, Command: "y", Created: now}))
	st.MarkDelivered("j1")
	var out strings.Builder
	printJobNotice(&out, st, ws, "29b12d1c-5", now, false)
	require.NotContains(t, out.String(), "j4", "the same session id in another workspace is another session")
	require.Contains(t, out.String(), "while you were away, job j1 done")
	require.Contains(t, out.String(), "posted into this conversation")
	require.Contains(t, out.String(), "job j2 waiting")
	require.NotContains(t, out.String(), "j3")
	out.Reset()
	printJobNotice(&out, st, ws, "29b12d1c-5", now, false)
	require.NotContains(t, out.String(), "j1", "a finished job is reported once")
}

func TestJobRun_TakesAJobToItsEndAndKeepsTheOutput(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Waiting, Command: "echo hello; exit 3", Dir: t.TempDir(),
		Until: []jobs.Check{{Command: "true"}}, Every: jobs.MinEvery, Expire: time.Now().Add(time.Minute), Created: time.Now()}))
	require.NoError(t, jobRun(t.Context(), t.TempDir(), st, "j1"))
	j, err := st.Load("j1")
	require.NoError(t, err)
	require.Equal(t, jobs.Failed, j.State)
	require.Equal(t, 3, j.Exit)
	require.Contains(t, j.Output, "hello")
	require.True(t, j.Until[0].Passing)

	var out strings.Builder
	require.ErrorIs(t, jobWait(t.Context(), st, "j1", &out), errJobFailed)
	require.Contains(t, out.String(), "<job_output>\nhello")
}

func TestParseJob_KeepsTheCommandsQuoting(t *testing.T) {
	j, err := parseJob([]string{"--at", "+1m", "--", "git", "commit", "-m", "fix bug", "it's"}, time.Now())
	require.NoError(t, err)
	require.Equal(t, "'git' 'commit' '-m' 'fix bug' 'it'\\''s'", j.Command)
	j, err = parseJob([]string{"--at", "+1m", "--", "kubectl get pods | wc -l"}, time.Now())
	require.NoError(t, err)
	require.Equal(t, "kubectl get pods | wc -l", j.Command, "one word is a shell line as written")
}

func TestJobRun_ACancelAskedForBeforeTheRunnerStartsWins(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Waiting, Command: "touch ran", Dir: t.TempDir(),
		At: time.Now(), Every: jobs.MinEvery, Expire: time.Now().Add(time.Minute), Created: time.Now()}))
	require.NoError(t, jobCancel(t.Context(), st, "j1", &strings.Builder{}))
	require.NoError(t, jobRun(t.Context(), t.TempDir(), st, "j1"))
	j, err := st.Load("j1")
	require.NoError(t, err)
	require.Equal(t, jobs.Cancelled, j.State)
	require.NoFileExists(t, filepath.Join(j.Dir, "ran"), "a cancelled job never starts its command")
}

func TestJobRun_RefusesAJobChangedToSomethingTheGuardRefuses(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Waiting, Command: "codeforge gate reset", Dir: t.TempDir(),
		At: time.Now(), Every: jobs.MinEvery, Expire: time.Now().Add(time.Minute), Created: time.Now()}))
	require.ErrorIs(t, jobRun(t.Context(), t.TempDir(), st, "j1"), errJobGuard)
	j, err := st.Load("j1")
	require.NoError(t, err)
	require.Equal(t, jobs.Cancelled, j.State)
}

func TestJobRun_ACancelFileBeforeTheRunnerWinsOverAWaitingJob(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Waiting, Command: "touch ran", Dir: t.TempDir(),
		At: time.Now(), Every: jobs.MinEvery, Expire: time.Now().Add(time.Minute), Created: time.Now()}))
	// The cancel was asked for while the runner was starting: the job file still says waiting.
	require.NoError(t, st.RequestCancel("j1"))
	require.NoError(t, jobRun(t.Context(), t.TempDir(), st, "j1"))
	j, err := st.Load("j1")
	require.NoError(t, err)
	require.Equal(t, jobs.Cancelled, j.State)
	require.NoFileExists(t, filepath.Join(j.Dir, "ran"))
}

func TestRealDeps_ACheckOverItsLimitTakesItsChildrenWithIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	code, _ := realDeps(jobs.Store{}).Check(ctx, t.TempDir(), "sleep 30 & wait")
	require.NotZero(t, code)
	require.Less(t, time.Since(start), 5*time.Second, "a child holding the output open does not keep the check running")
}

func TestTailBuffer_KeepsOnlyTheEnd(t *testing.T) {
	b := &tailBuffer{max: 4}
	_, err := b.Write([]byte("abcdef"))
	require.NoError(t, err)
	_, err = b.Write([]byte("gh"))
	require.NoError(t, err)
	require.Equal(t, "efgh", b.String())
}

func TestRunJob_LogRefusesAPathThatIsNotAJob(t *testing.T) {
	require.ErrorIs(t, runJob(t.Context(), t.TempDir(), []string{"log", "../../etc/passwd"}, &strings.Builder{}), jobs.ErrNotFound)
}

// noRunners keeps tests from starting a runner, which would be a copy of the test binary.
func noRunners(t *testing.T) *[]string {
	t.Helper()
	var started []string
	old := startRunner
	startRunner = func(_ context.Context, _ jobs.Store, j *jobs.Job) error { started = append(started, j.ID); return nil }
	t.Cleanup(func() { startRunner = old })
	return &started
}

func TestReviveJobs_ACommandCutOffByARestartIsNotRunAgain(t *testing.T) {
	started := noRunners(t)
	st := jobs.Store{Dir: t.TempDir()}
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Running, Command: "make deploy", RunnerPID: 999999, Created: time.Now()}))
	reviveJobs(t.Context(), st, "")
	j, err := st.Load("j1")
	require.NoError(t, err)
	require.Equal(t, jobs.Failed, j.State)
	require.Contains(t, j.Reason, "not run again")
	require.Empty(t, *started, "a failed job with no session needs no runner")
}

func TestJobRun_AJobLeftRunningIsNotRunAgain(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	dir := t.TempDir()
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Running, Command: "touch ran", Dir: dir, Started: time.Now(), Created: time.Now()}))
	require.NoError(t, jobRun(t.Context(), t.TempDir(), st, "j1"))
	j, err := st.Load("j1")
	require.NoError(t, err)
	require.Equal(t, jobs.Failed, j.State)
	require.NoFileExists(t, filepath.Join(dir, "ran"))
}

func TestReviveJobs_LeavesTheResumedSessionsResultsToItsNotice(t *testing.T) {
	started := noRunners(t)
	st := jobs.Store{Dir: t.TempDir()}
	now := time.Now()
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Done, Session: "s1", Finished: now, Created: now, Command: "x"}))
	require.NoError(t, st.Save(&jobs.Job{ID: "j2", State: jobs.Done, Session: "s2", Finished: now, Created: now, Command: "x"}))
	reviveJobs(t.Context(), st, "s1")
	require.Equal(t, []string{"j2"}, *started)
}

func TestJobCancel_StopsACommandItsRunnerLeftBehind(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	cmd := groupCommand(t.Context(), t.TempDir(), "sleep 30", nil, nil)
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Running, Command: "sleep 30", Started: time.Now(), Created: time.Now(),
		CommandPID: cmd.Process.Pid, CommandStart: processStart(cmd.Process.Pid), CommandBoot: bootID(t.Context())}))
	require.NoError(t, jobCancel(t.Context(), st, "j1", &strings.Builder{}))
	select {
	case err := <-done:
		require.Error(t, err, "stopped by a signal")
	case <-time.After(15 * time.Second):
		t.Fatal("the command was left running")
	}
	j, err := st.Load("j1")
	require.NoError(t, err)
	require.Equal(t, jobs.Cancelled, j.State)
}

func TestCommandGroupAlive_ARecordedStartThatDiffersIsAnotherProcess(t *testing.T) {
	cmd := groupCommand(t.Context(), t.TempDir(), "sleep 30", nil, nil)
	require.NoError(t, cmd.Start())
	defer stopGroup(cmd.Process.Pid, time.Second)
	j := &jobs.Job{CommandPID: cmd.Process.Pid, CommandStart: processStart(cmd.Process.Pid), CommandBoot: bootID(t.Context())}
	require.True(t, commandGroupAlive(t.Context(), j))
	j.CommandStart = "Mon Jan  1 00:00:00 2024"
	require.False(t, commandGroupAlive(t.Context(), j), "a reused pid is never stopped")
}

func TestParseJob_AnExplicitExpireIsNeverMoved(t *testing.T) {
	_, err := parseJob([]string{"--at", "+2h", "--expire", "1m", "--", "true"}, time.Now())
	require.ErrorIs(t, err, errJobUsage)
}

func TestRun_AJobsCommandKeepsItsCwdFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Dispatched before --cwd is read: an unknown word is the error, not a missing directory.
	err := Run(t.Context(), []string{"job", "nope", "--", "tool", "--cwd", "/nonexistent-dir"})
	require.ErrorIs(t, err, errNoSuchCommand)
	require.NotContains(t, fmt.Sprint(err), "changing to --cwd")
	_, statErr := os.Stat("/nonexistent-dir")
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestScheduleRefusal_ReadsScriptsFromTheJobsDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rec.sh"), []byte("codeforge gate reset\n"), 0o600))
	require.ErrorIs(t, scheduleRefusal(&jobs.Job{Command: "sh rec.sh", Dir: dir}), errJobGuard)
	require.ErrorIs(t, scheduleRefusal(&jobs.Job{Command: "true", Until: []jobs.Check{{Command: "./rec.sh"}}, Dir: dir}), errJobGuard)
}

func TestJobRun_AScriptChangedWhileWaitingIsRefusedBeforeItRuns(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	dir := t.TempDir()
	script := filepath.Join(dir, "deploy.sh")
	require.NoError(t, os.WriteFile(script, []byte("touch ran\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "next"), []byte("codeforge observe x\ntouch ran\n"), 0o600))
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Waiting, Command: "sh deploy.sh", Dir: dir,
		Until: []jobs.Check{{Command: "cp next deploy.sh"}}, Every: jobs.MinEvery, Expire: time.Now().Add(time.Minute), Created: time.Now()}))
	require.NoError(t, jobRun(t.Context(), t.TempDir(), st, "j1"))
	j, err := st.Load("j1")
	require.NoError(t, err)
	require.Equal(t, jobs.Cancelled, j.State)
	require.Contains(t, j.Reason, "will not schedule")
	require.NoFileExists(t, filepath.Join(dir, "ran"))
}

func TestParseJob_AStartAtTheDefaultExpiryStillGetsItsWindow(t *testing.T) {
	now := time.Now()
	j, err := parseJob([]string{"--at", "+24h", "--", "true"}, now)
	require.NoError(t, err)
	require.True(t, j.Expire.After(j.At))
	_, err = parseJob([]string{"--at", "+1h", "--expire", "1h", "--", "true"}, now)
	require.ErrorIs(t, err, errJobUsage)
}

func TestRealDeps_ABackgroundChildEndsWithItsCommand(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	j := &jobs.Job{ID: "j1", Command: "sleep 617 & echo started", Dir: t.TempDir()}
	done, _, err := realDeps(st).Start(t.Context(), j)
	require.NoError(t, err)
	select {
	case ex := <-done:
		require.Zero(t, ex.Code)
	case <-time.After(20 * time.Second):
		t.Fatal("the job did not end with its shell")
	}
	require.Error(t, syscall.Kill(-j.CommandPID, 0), "nothing of the group is left")
}

func TestJobRun_AScriptItsCheckCreatesIsReadWhenItRuns(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "next"), []byte("touch ran\n"), 0o600))
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Waiting, Command: "sh deploy.sh", Dir: dir,
		Until: []jobs.Check{{Command: "cp next deploy.sh"}}, Every: jobs.MinEvery, Expire: time.Now().Add(time.Minute), Created: time.Now()}))
	require.NoError(t, jobRun(t.Context(), t.TempDir(), st, "j1"))
	j, err := st.Load("j1")
	require.NoError(t, err)
	require.Equal(t, jobs.Done, j.State, j.Reason)
	require.FileExists(t, filepath.Join(dir, "ran"))
}

func TestCommandRefusal_ANativeBinaryByPathIsAllowed(t *testing.T) {
	require.NoError(t, commandRefusal(t.TempDir(), "/usr/bin/true"))
}

func TestPrintJobNotice_AReviewLaunchMarksNothingSeen(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	ws := t.TempDir()
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", Session: "s", Workspace: ws, State: jobs.Done, Command: "x", Created: time.Now()}))
	var out strings.Builder
	printJobNotice(&out, st, ws, "s", time.Now(), true)
	require.Contains(t, out.String(), "j1")
	require.False(t, st.Seen("j1"))
}

func TestRealDeps_ACheckLeavesNothingBehind(t *testing.T) {
	code, _ := realDeps(jobs.Store{}).Check(t.Context(), t.TempDir(), "sleep 618 >/dev/null 2>&1 & exit 0")
	require.Zero(t, code)
	time.Sleep(200 * time.Millisecond)
	out, err := exec.CommandContext(t.Context(), "pgrep", "-f", "^sleep 618").Output()
	require.Error(t, err, "pgrep finds nothing: %s", out)
}

func TestCommandGroupAlive_AChildOutlivesItsShell(t *testing.T) {
	cmd := groupCommand(t.Context(), t.TempDir(), "sleep 619 >/dev/null 2>&1 & exit 0", nil, nil)
	require.NoError(t, cmd.Start())
	j := &jobs.Job{CommandPID: cmd.Process.Pid, CommandStart: processStart(cmd.Process.Pid), CommandBoot: bootID(t.Context())}
	require.NoError(t, cmd.Wait())
	defer stopGroup(j.CommandPID, time.Second)
	require.True(t, commandGroupAlive(t.Context(), j), "the shell is gone but its child still runs in the group")
	j.CommandBoot = "another boot"
	require.False(t, commandGroupAlive(t.Context(), j))
}

func TestResultInSession_IsTheAcknowledgement(t *testing.T) {
	ws := t.TempDir()
	j := &jobs.Job{ID: "j1", Workspace: ws, Session: "s1", Command: "x", State: jobs.Done, DeliveryToken: "t0k3n"}
	require.False(t, resultInSession(j))
	dir := filepath.Join(ws, ".empryo", "sessions", "s1")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(`{"role":"user","text":"`+j.ResultMark()+`x) is done"}`+"\n"+`{"text":"`+deliveryMark(j)+`"}`+"\n"), 0o600))
	require.True(t, resultInSession(j))
	other := *j
	other.DeliveryToken = "another"
	require.False(t, resultInSession(&other), "a mention of the job is not its posted result")
}

func TestJobCancel_WithoutARunnerStillPostsTheCancelBack(t *testing.T) {
	started := noRunners(t)
	st := jobs.Store{Dir: t.TempDir()}
	require.NoError(t, st.Save(&jobs.Job{ID: "j1", State: jobs.Waiting, Session: "s", Workspace: t.TempDir(), Command: "x", Created: time.Now()}))
	require.NoError(t, jobCancel(t.Context(), st, "j1", &strings.Builder{}))
	require.Equal(t, []string{"j1"}, *started)
}

func TestRunnerActive_WritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "jobs")
	st := jobs.Store{Dir: dir}
	require.False(t, st.RunnerActive("j1"))
	_, err := os.Stat(dir)
	require.ErrorIs(t, err, os.ErrNotExist, "watching a job creates nothing")
}

func TestRealDeps_ACancelJustBeforeTheReleaseHoldsTheCommandBack(t *testing.T) {
	st := jobs.Store{Dir: t.TempDir()}
	dir := t.TempDir()
	j := &jobs.Job{ID: "j1", Command: "touch ran", Dir: dir}
	require.NoError(t, st.RequestCancel("j1"))
	_, _, err := realDeps(st).Start(t.Context(), j)
	require.ErrorIs(t, err, errHeldBack)
	require.NoFileExists(t, filepath.Join(dir, "ran"))
}
