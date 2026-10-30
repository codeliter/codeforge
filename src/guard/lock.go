package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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
