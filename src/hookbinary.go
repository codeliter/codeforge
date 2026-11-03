package src

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const privateExec = 0o700

// installHookBinary keeps the guard independent of temporary builds and Homebrew
// Cellar paths. Refresh it atomically so concurrent sessions never see half a binary.
func installHookBinary(empryoDir, self string) (string, error) {
	data, err := os.ReadFile(self)
	if err != nil {
		return "", fmt.Errorf("reading codeforge executable: %w", err)
	}
	rel := filepath.Join("hooks", "cf")
	target := filepath.Join(empryoDir, rel)
	if !isSymlinked(empryoDir, rel) {
		if have, err := os.ReadFile(target); err == nil && bytes.Equal(have, data) {
			if info, err := os.Stat(target); err == nil && info.Mode().Perm() == privateExec {
				return target, nil
			}
		}
	}
	if err := unlinkParents(empryoDir, rel); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), privateDir); err != nil {
		return "", fmt.Errorf("creating hook directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".cf-*")
	if err != nil {
		return "", fmt.Errorf("creating hook executable: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Chmod(privateExec); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("setting hook executable permissions: %w", err)
	}
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return "", fmt.Errorf("writing hook executable: %w", err)
	}
	if err := os.Rename(f.Name(), target); err != nil {
		return "", fmt.Errorf("installing hook executable: %w", err)
	}
	return target, nil
}
