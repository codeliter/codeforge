package effort

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "run.sh")
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700)) //nolint:gosec // a test script must be executable
	return p
}

func TestBound_CancelUnblocksOutputHeldByADescendant(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, script(t, "sleep 30 &\nsleep 30\n"))
	Bound(cmd)
	start := time.Now()
	_, err := cmd.Output()
	Reap(cmd)
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second, "the descendant holding stdout must not keep Output blocked")
}

func TestReap_KillsWhatAFinishedParentLeftRunning(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child")
	cmd := exec.CommandContext(t.Context(), script(t, "(sleep 1; touch \""+marker+"\") >/dev/null 2>&1 &\necho done\n"))
	Bound(cmd)
	out, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "done\n", string(out))
	Reap(cmd)
	time.Sleep(1500 * time.Millisecond)
	require.NoFileExists(t, marker, "the background child outlived its parent")
}
