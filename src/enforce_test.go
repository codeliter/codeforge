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

func TestRemoveGHShim(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bin", "gh")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), privateDir))
	require.NoError(t, removeGHShim(dir), "no gh is a no-op")

	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+shimMagic+"\nexec codeforge gh-shim \"$@\"\n"), execPerm))
	require.NoError(t, removeGHShim(dir))
	require.NoFileExists(t, p, "our shim is removed")

	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\nsomeone else\n"), execPerm))
	require.NoError(t, removeGHShim(dir))
	require.FileExists(t, p, "never delete a gh we did not write")
}

func TestHelp(t *testing.T) {
	for _, a := range []string{"help", "-h", "--help"} {
		require.True(t, isHelp(a))
	}
	require.Contains(t, helpText, "gate ")
	require.NotContains(t, helpText, "allow RULE")
}

func TestSandboxProfile(t *testing.T) {
	p := sandboxProfile(`/r/my "repo"`)
	require.Contains(t, p, `(deny file-write* (subpath "/r/my \"repo\""))`)
	require.True(t, strings.Index(p, "deny") < strings.Index(p, `allow file-write*`), "the .empryo allow comes last so it wins")
}
