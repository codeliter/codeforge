package src

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

func TestWorkMode(t *testing.T) {
	require.NoError(t, config.ValidMode("work"))
	require.Contains(t, helpText, `work ["task"]`)
	mode, err := pickMode(config.Config{DefaultMode: "work"}, nil)
	require.NoError(t, err)
	require.Equal(t, "work", mode)
	home := t.TempDir()
	writeConfig(t, home, `{"default_mode":"work"}`)
	cfg, err := config.LoadGlobal(home)
	require.NoError(t, err)
	require.Equal(t, "work", cfg.DefaultMode)
	t.Setenv("CODEFORGE_MODE", "work")
	cfg, err = config.LoadGlobal(t.TempDir())
	require.NoError(t, err)
	require.Equal(t, "work", cfg.DefaultMode)
}

func TestModeArgs_Work(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "empty", want: []string{"--model", "a/coder", "--effort", "medium"}},
		{name: "explicit effort", input: []string{"--effort", "high", "finish this change"}, want: []string{"--model", "a/coder", "--effort", "high", "finish this change"}},
		{name: "explicit model", input: []string{"--model", "b/coder", "--effort", "low"}, want: []string{"--model", "b/coder", "--effort", "low"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mode, args, err := modeArgs(t.Context(), "/nonexistent", config.Config{Model: "a/coder"}, "work", tt.input)
			require.NoError(t, err)
			require.Equal(t, "work", mode)
			require.Equal(t, tt.want, args)
		})
	}
	_, _, err := modeArgs(t.Context(), "/nonexistent", config.Config{}, "work", []string{"--effort", "max"})
	require.ErrorIs(t, err, errEffortAboveCeiling)
}

func TestWorkMode_SkipsEffortJudges(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	t.Setenv("CODEFORGE_WORK_JUDGE_PROBE", marker)
	bin := filepath.Join(dir, "empryo")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nprintf called > \"$CODEFORGE_WORK_JUDGE_PROBE\"\nexit 1\n"), execPerm))
	mode, args, err := modeArgs(t.Context(), bin, config.Config{Model: "a/coder", Judges: []string{"a/judge"}}, "work", []string{"finish this change"})
	require.NoError(t, err)
	require.Equal(t, "work", mode)
	require.Equal(t, []string{"--model", "a/coder", "--effort", "medium", "finish this change"}, args)
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "work should not spend model calls choosing its starting effort")
}

//nolint:revive // The subprocess exercises Run through syscall.Exec without replacing the test runner.
func TestWorkCLI(t *testing.T) {
	if os.Getenv("CODEFORGE_WORK_TEST") == "1" {
		if err := Run(t.Context(), []string{"work", "--headless", "finish this change"}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	home, wd, binDir := t.TempDir(), t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "empryo"), []byte("#!/bin/sh\nprintf '%s\n' \"$@\"\n"), execPerm))
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWorkCLI$")
	cmd.Dir = wd
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "CODEFORGE_MODE=chat", "CODEFORGE_WORK_TEST=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), "--profile\ncodeforge-work\n")
	require.Contains(t, string(out), "finish this change")
	require.Contains(t, string(out), "--effort\nmedium\n")
	require.NotContains(t, string(out), "codeforge-ticket-to-pr")
	data, err := os.ReadFile(filepath.Join(home, ".empryo", "agents", "codeforge-work.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "forgeMode: auto")
	require.Contains(t, string(data), "effort: medium")
	require.NotContains(t, string(data), "{{")
	require.NotContains(t, string(data), "codeforge-ticket-to-pr")
}
