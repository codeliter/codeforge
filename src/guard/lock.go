package guard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Lock serialises Load→Decide→Save across the parallel hook processes Empryo starts,
// so a one-off approval cannot be spent twice.
func Lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, privateDir); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(dir, stateFile+".lock"), os.O_CREATE|os.O_RDWR, privateFile)
	if err != nil {
		return nil, fmt.Errorf("opening state lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking state: %w", err)
	}

	// Closing the descriptor releases the flock, so no explicit unlock is needed.
	return func() { f.Close() }, nil
}

// lockPoll is how often LockContext retries a held lock.
const lockPoll = 25 * time.Millisecond

// LockContext is Lock that gives up when ctx ends, so a holder that never lets go cannot outlast a deadline.
func LockContext(ctx context.Context, dir string) (func(), error) {
	if err := os.MkdirAll(dir, privateDir); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, stateFile+".lock"), os.O_CREATE|os.O_RDWR, privateFile)
	if err != nil {
		return nil, fmt.Errorf("opening state lock: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("waiting for the state lock: %w", err)
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			// Closing the descriptor releases the flock, as in Lock.
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("locking state: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("waiting for the state lock: %w", ctx.Err())
		case <-time.After(lockPoll):
		}
	}
}
