package src

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureEnforced_RemovesOldPrePushHook(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "--local", "hook.codeforge.event", "pre-push")
	git("config", "--local", "hook.codeforge.command", "codeforge pre-push")
	t.Chdir(dir)

	require.NoError(t, ensureEnforced(t.Context(), "codeforge"))
	require.Empty(t, gitConfig(t.Context(), dir, "hook.codeforge.command"), "pushes are no longer gated")
	require.NoError(t, ensureEnforced(t.Context(), "codeforge"), "running again is a no-op")
}

func TestInstallGHShim(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, installGHShim(dir, "/usr/local/bin/codeforge"))
	data, err := os.ReadFile(filepath.Join(dir, "bin", "gh"))
	require.NoError(t, err)
	require.True(t, strings.Contains(string(data), "gh-shim"))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "bin", "gh"), []byte("#!/bin/sh\nsomeone else\n"), 0o755))
	require.Error(t, installGHShim(dir, "/usr/local/bin/codeforge"), "never overwrite a gh we did not write")
}

func TestHelp(t *testing.T) {
	for _, a := range []string{"help", "-h", "--help"} {
		require.True(t, isHelp(a))
	}
	require.Contains(t, helpText, "gate ")
	require.Contains(t, helpText, "allow RULE")
}

func TestSandboxProfile(t *testing.T) {
	p := sandboxProfile(`/r/my "repo"`)
	require.Contains(t, p, `(deny file-write* (subpath "/r/my \"repo\""))`)
	require.True(t, strings.Index(p, "deny") < strings.Index(p, `allow file-write*`), "the .empryo allow comes last so it wins")
}
