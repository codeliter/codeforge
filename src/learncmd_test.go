package src

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/store"
)

func learnFixture(t *testing.T) (home, repo string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(repo)
	return t.TempDir(), repo
}

func listLearning(t *testing.T, home string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, runLearn(t.Context(), home, append([]string{"list"}, args...), &out))
	return out.String()
}

func TestLearn_CorrectListRevert(t *testing.T) {
	home, repo := learnFixture(t)
	var out bytes.Buffer
	require.NoError(t, runLearn(t.Context(), home, []string{"correct", "Use", "table tests"}, &out))
	require.Contains(t, out.String(), "recorded correction #1")
	require.Contains(t, listLearning(t, home), `correction origin=cli "Use table tests"`)

	require.NoError(t, runLearn(t.Context(), home, []string{"revert", "#1"}, &out))
	require.NotContains(t, listLearning(t, home), "table tests")
	require.Contains(t, listLearning(t, home, "--all"), "reverted")
	require.ErrorIs(t, runLearn(t.Context(), home, []string{"revert", "2"}, &out), store.ErrNoObservation)

	s, err := store.Open(t.Context(), filepath.Join(configDir(home), "sessions"))
	require.NoError(t, err)
	obs, err := s.Observations(t.Context(), repo, true)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.Len(t, obs, 1)
	require.Equal(t, repo, obs[0].Repository)
}

func TestLearn_RejectsBadInput(t *testing.T) {
	home, _ := learnFixture(t)
	var out bytes.Buffer
	for _, args := range [][]string{
		{}, {"approve", "1"}, {"list", "extra"}, {"propose", "x"}, {"revert"}, {"revert", "0"}, {"revert", "x"},
		{"correct", "--session"}, {"correct", "--session", "../x", "text"}, {"outcome", "go", "test"}, {"outcome", "--"},
	} {
		require.ErrorIs(t, runLearn(t.Context(), home, args, &out), errLearnUsage, args)
	}
	for _, text := range []string{"", "two\nlines", "esc \x1b[31m", strings.Repeat("x", maxLearnText+1)} {
		require.ErrorIs(t, runLearn(t.Context(), home, []string{"correct", text}, &out), errLearnText, text)
	}
	for _, text := range []string{"token=abc", "use ghp_" + strings.Repeat("a", 30), "-----BEGIN OPENSSH PRIVATE KEY-----"} {
		require.ErrorIs(t, runLearn(t.Context(), home, []string{"correct", text}, &out), errLearnSecret, text)
	}
	require.ErrorIs(t, runLearn(t.Context(), home, []string{"outcome", "--", "curl", "-H", "Authorization: token=abc"}, &out), errLearnSecret)
	require.ErrorIs(t, runLearn(t.Context(), home, []string{"correct", "--session", "nope", "x"}, &out), errLearnSession)
	require.Contains(t, listLearning(t, home), "No observations")
}

func TestLearn_SessionMustBelongToRepository(t *testing.T) {
	home, repo := learnFixture(t)
	s, err := store.Open(t.Context(), filepath.Join(configDir(home), "sessions"))
	require.NoError(t, err)
	require.NoError(t, s.Put(t.Context(), store.Session{ID: "here", Mode: modeChat, Profile: profileFor(modeChat), Repository: repo}))
	require.NoError(t, s.Put(t.Context(), store.Session{ID: "there", Mode: modeChat, Profile: profileFor(modeChat), Repository: "/elsewhere"}))
	require.NoError(t, s.Close())
	var out bytes.Buffer
	require.NoError(t, runLearn(t.Context(), home, []string{"correct", "--session=here", "x"}, &out))
	require.ErrorIs(t, runLearn(t.Context(), home, []string{"correct", "--session", "there", "x"}, &out), errLearnSession)
	require.ErrorIs(t, runLearn(t.Context(), home, []string{"outcome", "--session", "there", "--", "true"}, &out), errLearnSession)
	require.Contains(t, listLearning(t, home), "session=here")
}

func TestLearn_OutcomeRecordsStatusNotOutput(t *testing.T) {
	home, _ := learnFixture(t)
	var out bytes.Buffer
	require.NoError(t, learnOutcome(t.Context(), home, "/repo", []string{"--", "sh", "-c", "echo hidden-output"}, &out, time.Minute))
	require.ErrorIs(t, learnOutcome(t.Context(), home, "/repo", []string{"--", "sh", "-c", "exit 3"}, &out, time.Minute), errCheckFailed)
	require.ErrorIs(t, learnOutcome(t.Context(), home, "/repo", []string{"--", "sh", "-c", "kill -9 $$"}, &out, time.Minute), errCheckFailed)
	require.ErrorIs(t, learnOutcome(t.Context(), home, "/repo", []string{"--", "sleep", "5"}, &out, 100*time.Millisecond), errCheckFailed)
	require.Error(t, learnOutcome(t.Context(), home, "/repo", []string{"--", "/does/not/exist"}, &out, time.Minute))

	s, err := store.Open(t.Context(), filepath.Join(configDir(home), "sessions"))
	require.NoError(t, err)
	obs, err := s.Observations(t.Context(), "/repo", true)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.Len(t, obs, 4, "a check that never started is not recorded")
	got := make([]string, len(obs))
	for i, o := range obs {
		require.Equal(t, store.OriginRun, o.Origin)
		require.NotContains(t, o.Text, "hidden-output\n")
		got[i] = describeStatus(o)
	}
	require.Equal(t, []string{"passed (exit 0)", "failed (exit 3)", "incomplete", "incomplete"}, got)
	require.Equal(t, "sh -c echo hidden-output", obs[0].Text)
}

func TestLearn_InterruptRecordsIncomplete(t *testing.T) {
	home, _ := learnFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	var out bytes.Buffer
	require.ErrorIs(t, learnOutcome(ctx, home, "/repo", []string{"--", "sleep", "5"}, &out, time.Minute), errCheckFailed)
	require.Contains(t, out.String(), "incomplete")
}

func TestLearn_RunDispatchKeepsArgvAndSkipsSetup(t *testing.T) {
	home, repo := learnFixture(t)
	t.Setenv("HOME", home)
	require.NoError(t, Run(t.Context(), []string{"learn", "outcome", "--", "sh", "-c", "exit 0", "--cwd", "/nope"}))
	require.ErrorIs(t, Run(t.Context(), []string{"learn", "correct", "text", "--session", "s1"}), errLearnUsage)
	require.ErrorIs(t, Run(t.Context(), []string{"--cwd", repo, "learn", "list"}), errLearnCwd)
	s, err := store.Open(t.Context(), filepath.Join(configDir(home), "sessions"))
	require.NoError(t, err)
	obs, err := s.Observations(t.Context(), repo, true)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.Len(t, obs, 1)
	require.Equal(t, "sh -c exit 0 --cwd /nope", obs[0].Text)
	_, err = os.Stat(filepath.Join(home, ".empryo"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunCheck_TimeoutKillsDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	var discard bytes.Buffer
	res, err := runCheck(t.Context(), []string{"sh", "-c", "sleep 30 & echo $! > " + pidFile + "; wait"}, 300*time.Millisecond, &discard, &discard)
	require.NoError(t, err)
	require.Equal(t, store.StatusIncomplete, res.status)
	require.Nil(t, res.exit)
	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 5*time.Second, 50*time.Millisecond)
}

func TestPropose_IsDeterministicEvidenceOnly(t *testing.T) {
	at := time.Now()
	zero, one := 0, 1
	obs := []store.Observation{
		{ID: 1, Kind: store.KindCorrection, Text: "Prefer table tests", SessionID: "b", CreatedAt: at},
		{ID: 2, Kind: store.KindCorrection, Text: "Once only", SessionID: "a", CreatedAt: at},
		{ID: 3, Kind: store.KindCorrection, Text: "use  require", SessionID: "a", CreatedAt: at},
		{ID: 4, Kind: store.KindCorrection, Text: "prefer TABLE   tests", SessionID: "a", CreatedAt: at},
		{ID: 5, Kind: store.KindCorrection, Text: "Use require", CreatedAt: at},
		{ID: 6, Kind: store.KindCorrection, Text: "use require", SessionID: "c", CreatedAt: at},
		{ID: 7, Kind: store.KindOutcome, Text: "go test ./...", SessionID: "a", Status: store.StatusPassed, ExitCode: &zero},
		{ID: 8, Kind: store.KindOutcome, Text: "go vet ./...", SessionID: "b", Status: store.StatusFailed, ExitCode: &one},
		{ID: 9, Kind: store.KindOutcome, Text: "make lint", Status: store.StatusIncomplete},
		{ID: 10, Kind: store.KindCorrection, Text: "Once only", RevertedAt: &at},
	}
	got := propose(obs)
	require.Equal(t, []proposal{
		{Text: "use  require", Corrections: []int64{3, 5, 6}, Sessions: []string{"a", "c"}, Passed: 1},
		{Text: "Prefer table tests", Corrections: []int64{1, 4}, Sessions: []string{"a", "b"}, Passed: 1, Failed: 1},
	}, got)
	require.Equal(t, got, propose(obs))
	require.Empty(t, propose(obs[:3]))
}

func TestLearn_ProposeOutput(t *testing.T) {
	home, _ := learnFixture(t)
	var out bytes.Buffer
	require.NoError(t, runLearn(t.Context(), home, []string{"propose"}, &out))
	require.Contains(t, out.String(), "Nothing is applied")
	require.Contains(t, out.String(), "No correction has been recorded 2 or more times")
	for range 2 {
		require.NoError(t, runLearn(t.Context(), home, []string{"correct", "Keep PRs small"}, &out))
	}
	out.Reset()
	require.NoError(t, runLearn(t.Context(), home, []string{"propose"}, &out))
	require.Contains(t, out.String(), "1. \"Keep PRs small\"\n   corrections: #1 #2\n")
	require.Contains(t, helpText, "learn propose")
}
