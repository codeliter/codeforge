// Package fsutil holds the file helpers more than one codeforge package needs.
package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// tempPrefix starts the name of the temporary file atomicWriteFile writes beside its destination; os.CreateTemp follows
// it with a run of decimal digits.
const tempPrefix = ".codeforge-"

// Permissions for what codeforge writes for the user alone.
const (
	PrivateDir  = 0o700
	PrivateFile = 0o600
)

// atomicWriteFile assumes the caller has checked the destination and created its
// directory. Readers see either the previous complete file or the new one.
// AtomicWriteFile writes data to path through a temporary file in the same directory, so a reader never sees a partial file.
func AtomicWriteFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), tempPrefix+"*")
	if err != nil {
		return fmt.Errorf("creating temporary file for %s: %w", path, err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return fmt.Errorf("setting permissions for %s: %w", path, err)
	}
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}
