package guard

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLockContext_GivesUpWhenTheDeadlineEnds(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	require.NoError(t, err)
	defer unlock()

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = LockContext(ctx, dir)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 2*time.Second)
}

func TestLockContext_TakesAFreeLockAndReleasesIt(t *testing.T) {
	dir := t.TempDir()
	unlock, err := LockContext(t.Context(), dir)
	require.NoError(t, err)
	unlock()

	again, err := LockContext(t.Context(), dir)
	require.NoError(t, err)
	again()
}

func TestLockContext_RefusesAFreeLockWhenAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := LockContext(ctx, t.TempDir())
	require.ErrorIs(t, err, context.Canceled)
}
