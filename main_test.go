package main

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src"
)

// tree describes every file under roots (path, mode, size, content hash), so two snapshots can be compared.
func tree(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return fmt.Errorf("stat %s: %w", path, err)
			}
			sum := ""
			if info.Mode().IsRegular() {
				raw, err := os.ReadFile(path)
				if err != nil {
					return fmt.Errorf("reading %s: %w", path, err)
				}
				sum = fmt.Sprintf("%x", sha256.Sum256(raw))
			}
			out[path] = fmt.Sprintf("%v %d %s", info.Mode(), info.Size(), sum)
			return nil
		}))
	}
	return out
}

func TestEveryCommandAnswersHelpAndChangesNothing(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codeforge")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".").CombinedOutput()
	require.NoError(t, err, string(out))

	home, project := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codeforge"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codeforge", "config.yaml"), []byte("model: sonnet\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codeforge", "state.json"), []byte("{}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(project, "keep.txt"), []byte("mine\n"), 0o600))
	before := tree(t, home, project)

	// Each registered command with each help flag, and the forms that write: a subcommand of a command and extra flags.
	var calls [][]string
	for _, cmd := range src.RegisteredCommands() {
		if cmd == "gh-shim" {
			continue // it forwards its arguments to gh (a stale shim sends gh --help here), so it is not a help target
		}
		calls = append(calls, []string{cmd, "--help"}, []string{cmd, "-h"})
	}
	calls = append(calls,
		[]string{"--help"}, []string{"-h"}, []string{"help"},
		[]string{"models", "set", "review.model", "x", "--help"},
		[]string{"models", "apply", "--help"},
		[]string{"provider", "set", "litellm", "--help"},
		[]string{"learn", "correct", "--help"},
		[]string{"learn", "accept", "1", "-h"},
		[]string{"init", "--repo", "--help"},
		[]string{"review", "123", "--comment=yes", "--help"},
		[]string{"--cwd", project, "repair", "--help"},
	)

	for _, args := range calls {
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Dir = project
		cmd.Stdin = strings.NewReader(`{"tool_name":"Bash"}`)
		cmd.Env = append(os.Environ(), "HOME="+home, "CODEFORGE_LAUNCH=1", "CODEFORGE_GUARD=")
		stdout, err := cmd.Output()
		require.NoError(t, err, "codeforge %s must exit 0", strings.Join(args, " "))
		require.Contains(t, string(stdout), "Usage:", "codeforge %s prints usage", strings.Join(args, " "))
	}
	require.Equal(t, before, tree(t, home, project), "help left config, state and the project exactly as they were")
}

func TestEngineExitCode(t *testing.T) {
	code, ok := engineExitCode(&src.EngineExitError{Code: 143})
	require.True(t, ok)
	require.Equal(t, 143, code)
	err := exec.CommandContext(t.Context(), "sh", "-c", "exit 7").Run()
	require.Error(t, err)
	_, ok = engineExitCode(fmt.Errorf("setup failed: %w", err))
	require.False(t, ok, "non-engine subprocess errors must retain their diagnostics")
}
