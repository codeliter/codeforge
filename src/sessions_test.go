package src

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/store"
)

func historicalSession(t *testing.T, wd, id string) {
	t.Helper()
	dir := filepath.Join(wd, ".empryo", "sessions", id)
	require.NoError(t, os.MkdirAll(dir, privateDir))
	meta, err := json.Marshal(map[string]string{"id": id, "cwd": wd})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), meta, privateFile))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("{}\n"), privateFile))
}

func TestResume_RestoresOriginalModeAcrossAliases(t *testing.T) {
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(wd)
	home := t.TempDir()
	for _, mode := range modes {
		id := "saved-" + mode
		args, err := persistSession(t.Context(), home, mode, []string{"--session-id", id})
		require.NoError(t, err)
		require.Contains(t, args, id)
		historicalSession(t, wd, id)
		for _, args := range [][]string{{"--resume", id}, {"--resume=" + id}, {"--session", id}, {"--session=" + id}, {"-s", id}, {"-s=" + id, "continue"}} {
			got, resumed, err := restoreMode(t.Context(), home, "", args)
			require.NoError(t, err)
			require.True(t, resumed)
			require.Equal(t, mode, got)
			got, prepared, err := modeArgs(t.Context(), "/nonexistent", Config{}, got, args)
			require.NoError(t, err)
			require.Equal(t, mode, got)
			require.Equal(t, args, prepared)
		}
	}
}

//nolint:revive // The subprocess must exercise Run through syscall.Exec without replacing the test runner.
func TestResumeCLI(t *testing.T) {
	if os.Getenv("CODEFORGE_RESUME_TEST") == "1" {
		if err := Run(t.Context(), strings.Split(os.Getenv("CODEFORGE_RESUME_ARGS"), "|")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home := t.TempDir()
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "empryo"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf 'guard=%s\\n' \"$CODEFORGE_GUARD\"\n"), execPerm))
	historicalSession(t, wd, "review-saved")
	s, err := store.Open(t.Context(), filepath.Join(configDir(home), "sessions"))
	require.NoError(t, err)
	require.NoError(t, s.Put(t.Context(), store.Session{ID: "review-saved", Mode: modeReview, Profile: profileFor(modeReview), Repository: wd}))
	require.NoError(t, s.Close())
	for _, args := range [][]string{{"--resume", "review-saved"}, {"--session=review-saved"}, {"-s", "review-saved"}, {"--resume", "unknown"}, {"chat", "--resume", "unknown"}, {"chat", "--resume", "review-saved"}, {"respond", "--resume", "review-saved"}, {"ticket", "--fork", "review-saved"}} {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestResumeCLI$")
		cmd.Dir = wd
		cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "CODEFORGE_MODE=chat", "CODEFORGE_RESUME_TEST=1", "CODEFORGE_RESUME_ARGS="+strings.Join(args, "|"))
		out, err := cmd.CombinedOutput()
		if args[0] == "chat" || args[0] == "respond" || args[0] == "ticket" || args[len(args)-1] == "unknown" {
			require.Error(t, err)
			require.NotContains(t, string(out), "guard=")
			continue
		}
		if runtime.GOOS != "darwin" {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err, string(out))
		require.Contains(t, string(out), "codeforge-review")
		require.Contains(t, string(out), "plan")
		require.Contains(t, string(out), "guard=review")
		require.NotContains(t, string(out), "--session-id")
	}
}

func TestResume_UnknownRequiresExplicitModeOnce(t *testing.T) {
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(wd)
	home := t.TempDir()
	historicalSession(t, wd, "historical")
	args := []string{"--resume", "historical"}
	_, _, err = restoreMode(t.Context(), home, "", args)
	require.ErrorIs(t, err, errSessionMode)
	mode, _, err := restoreMode(t.Context(), home, modeReview, args)
	require.NoError(t, err)
	require.Equal(t, modeReview, mode)
	_, err = persistSession(t.Context(), home, mode, args)
	require.NoError(t, err)
	mode, _, err = restoreMode(t.Context(), home, "", args)
	require.NoError(t, err)
	require.Equal(t, modeReview, mode)
	_, _, err = restoreMode(t.Context(), home, modeChat, args)
	require.ErrorIs(t, err, errSessionOverride)
	_, _, err = restoreMode(t.Context(), home, modeChat, []string{"--resume", "absent"})
	require.ErrorIs(t, err, errSessionMissing)
}

func TestResume_MissingAndMismatchedRepositories(t *testing.T) {
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(wd)
	home := t.TempDir()
	args := []string{"--resume", "saved"}
	_, err = persistSession(t.Context(), home, modeReview, []string{"--session-id", "saved"})
	require.NoError(t, err)
	_, _, err = restoreMode(t.Context(), home, "", args)
	require.ErrorIs(t, err, errSessionMissing)
	historicalSession(t, wd, "saved")
	other, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(other)
	historicalSession(t, other, "saved")
	_, _, err = restoreMode(t.Context(), home, "", args)
	require.ErrorIs(t, err, errSessionRepo)
	t.Chdir(wd)
	historicalSession(t, other, "historical")
	require.NoError(t, os.Rename(filepath.Join(other, ".empryo", "sessions", "historical"), filepath.Join(wd, ".empryo", "sessions", "historical")))
	_, _, err = restoreMode(t.Context(), home, modeReview, []string{"-s", "historical"})
	require.ErrorIs(t, err, errSessionRepo)
	require.NoError(t, os.Remove(filepath.Join(wd, ".empryo", "sessions", "saved", "session.jsonl")))
	_, _, err = restoreMode(t.Context(), home, "", args)
	require.ErrorIs(t, err, errSessionMissing)
}

func TestSessionFlags_InvalidAndConflicting(t *testing.T) {
	for _, args := range [][]string{{"--resume"}, {"--session="}, {"-s", "../escape"}, {"--resume", "--plan"}, {"--resume", "id", "--session-id", "new"}, {"--resume", "a", "-s", "b"}} {
		_, _, err := sessionID(args)
		require.ErrorIs(t, err, errSessionArgs, args)
	}
	_, resumed, err := sessionID([]string{"--system", "--resume"})
	require.NoError(t, err)
	require.False(t, resumed)
}

func TestResumeFlags_AreNotSystemValues(t *testing.T) {
	args := []string{"--system", "--resume"}
	mode, err := pickMode(Config{DefaultMode: modeChat}, args)
	require.NoError(t, err)
	require.Equal(t, modeChat, mode)
	_, prepared, err := modeArgs(t.Context(), "/missing", Config{}, mode, args)
	require.NoError(t, err)
	require.Contains(t, prepared, "--effort")
}

func TestLaunch_PersistsBeforeExecAndKeepsReviewProtections(t *testing.T) {
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(wd)
	home := t.TempDir()
	for _, mode := range modes {
		if mode == modeReview && runtime.GOOS != "darwin" {
			continue
		}
		argv, env, err := launchCommand(t.Context(), home, "/not-an-executable", mode, []string{"--session-id", mode})
		require.NoError(t, err)
		require.Equal(t, profileFor(mode), flagValue(argv, "--profile"))
		if mode == modeReview {
			require.Equal(t, sandboxExec, argv[0])
			require.Equal(t, "plan", flagValue(argv, "--mode"))
			require.Contains(t, env, "CODEFORGE_GUARD=review")
			require.Contains(t, argv[2], "(deny file-write*")
		}
		s, err := store.Open(t.Context(), filepath.Join(configDir(home), "sessions"))
		require.NoError(t, err)
		saved, err := s.Get(t.Context(), mode)
		require.NoError(t, err)
		require.NoError(t, s.Close())
		require.Equal(t, store.Session{ID: mode, Mode: mode, Profile: profileFor(mode), Repository: wd}, saved)
		historicalSession(t, wd, mode)
		restored, _, err := restoreMode(t.Context(), home, "", []string{"--session", mode})
		require.NoError(t, err)
		resumedArgv, resumedEnv, err := launchCommand(t.Context(), home, "/not-an-executable", restored, []string{"--session", mode})
		require.NoError(t, err)
		if mode == modeReview {
			require.Equal(t, sandboxExec, resumedArgv[0])
			require.Equal(t, "plan", flagValue(resumedArgv, "--mode"))
			require.Contains(t, resumedEnv, "CODEFORGE_GUARD=review")
		}
	}
	for _, args := range [][]string{{"--mode", "auto"}, {"--profile", "codeforge-chat"}, {"--agent=codeforge-chat"}, {"--no-profile"}} {
		_, _, err := launchCommand(t.Context(), home, "/missing", modeReview, args)
		require.ErrorIs(t, err, errSessionOverride)
	}
	_, err = os.Stat(filepath.Join(configDir(home), "state.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(configDir(home), "cache"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestPersistSession_GeneratedHeadlessID(t *testing.T) {
	t.Chdir(t.TempDir())
	args, err := persistSession(t.Context(), t.TempDir(), modeChat, []string{"--headless"})
	require.NoError(t, err)
	require.NotEmpty(t, flagValue(args, "--session-id"))
	require.Contains(t, args, "--save-session")
	require.Contains(t, helpText, "--resume ID, --session ID, -s ID")
}

func TestLaunch_FreshModeFlag(t *testing.T) {
	t.Chdir(t.TempDir())
	args, _, err := launchCommand(t.Context(), t.TempDir(), "/missing", modeChat, []string{"--mode", "plan"})
	require.NoError(t, err)
	require.Equal(t, "plan", flagValue(args, "--mode"))
}
