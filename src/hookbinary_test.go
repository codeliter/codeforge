package src

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallHookBinarySurvivesTemporaryExecutableRemoval(t *testing.T) {
	source := filepath.Join(t.TempDir(), "cf")
	dir := t.TempDir()
	body := []byte("#!/bin/sh\nprintf 'guard:%s\\n' \"$1\"\n")
	require.NoError(t, os.WriteFile(source, body, execPerm))
	target, err := installHookBinary(dir, source)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "hooks", "cf"), target)
	info, err := os.Stat(target)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(privateExec), info.Mode().Perm())
	require.NoError(t, os.Remove(source))
	out, err := exec.CommandContext(t.Context(), target, "hook").CombinedOutput()
	require.NoError(t, err, string(out))
	require.Equal(t, "guard:hook\n", string(out))

	require.NoError(t, os.WriteFile(source, []byte("#!/bin/sh\nprintf refreshed\\n\n"), execPerm))
	_, err = installHookBinary(dir, source)
	require.NoError(t, err)
	updated, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Contains(t, string(updated), "refreshed")
	require.NoError(t, os.Remove(source))
	_, err = installHookBinary(dir, source)
	require.Error(t, err)
	preserved, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, updated, preserved, "a failed refresh must keep the last working guard")
}

func TestInstallHookBinaryReplacesSymlinkWithoutChangingItsTarget(t *testing.T) {
	source, other := filepath.Join(t.TempDir(), "source"), filepath.Join(t.TempDir(), "other")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(source, []byte("#!/bin/sh\nexit 0\n"), execPerm))
	require.NoError(t, os.WriteFile(other, []byte("keep this"), privateFile))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "hooks"), privateDir))
	target := filepath.Join(dir, "hooks", "cf")
	require.NoError(t, os.Symlink(other, target))
	_, err := installHookBinary(dir, source)
	require.NoError(t, err)
	info, err := os.Lstat(target)
	require.NoError(t, err)
	require.Zero(t, info.Mode()&os.ModeSymlink)
	data, err := os.ReadFile(other)
	require.NoError(t, err)
	require.Equal(t, "keep this", string(data))
}
