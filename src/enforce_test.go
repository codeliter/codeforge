package src

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallPrePush_KeepsHuskyAndBlocksUnreviewedPush(t *testing.T) {
	self := buildSelf(t)
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	remote := t.TempDir()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"},
		{"init", "-q", "--bare", remote}, {"remote", "add", "origin", remote}, {"config", "core.hooksPath", ".husky"},
	} {
		out, err := git(args...)
		require.NoError(t, err, out)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".husky"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".husky", "pre-push"), []byte("#!/bin/sh\necho HUSKY-RAN\n"), 0o755))

	require.NoError(t, installPrePush(t.Context(), dir, self))
	require.NoError(t, installPrePush(t.Context(), dir, self), "idempotent")
	require.True(t, prePushInstalled(t.Context(), dir))

	out, err := git("push", "origin", "main")
	require.Error(t, err, out)
	require.Contains(t, out, "has not passed the review gate")

	out, err = git("push", "origin", "HEAD:refs/heads/other")
	require.Error(t, err, out)
	require.Contains(t, out, "HUSKY-RAN", "husky's own hook still runs")
}

func buildSelf(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "codeforge")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "..")
	wd, err := os.Getwd()
	require.NoError(t, err)
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return bin
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
