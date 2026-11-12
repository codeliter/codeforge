package src

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/codeliter/codeforge/src/config"

	"github.com/codeliter/codeforge/assets"
	"github.com/codeliter/codeforge/src/fsutil"
)

const (
	dirPerm     = 0o755
	privateDir  = fsutil.PrivateDir
	publicFile  = 0o644
	privateFile = fsutil.PrivateFile
)

// syncAssets writes the embedded profiles and skills into ~/.empryo, replacing stale copies and symlinks.
// Profiles carry {{coder}} and {{reviewer}} so `/profile` switches inside the TUI keep the configured models.
func syncAssets(empryoDir string, cfg config.Config) error {
	err := fs.WalkDir(assets.Profile, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		want, err := assets.Profile.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}
		if strings.HasPrefix(path, "agents/") {
			want, err = renderAgent(want, cfg)
			if err != nil {
				return err
			}
		}

		dst := filepath.Join(empryoDir, path)
		if have, err := os.ReadFile(dst); err == nil && bytes.Equal(have, want) && !fsutil.Symlinked(empryoDir, path) {
			return nil
		}

		return writeFile(empryoDir, path, want, publicFile)
	})
	if err != nil {
		return fmt.Errorf("syncing assets: %w", err)
	}
	return nil
}

// writeFile writes root/rel after removing symlinks on the way, so writes never land in another checkout.
func writeFile(root, rel string, data []byte, perm os.FileMode) error {
	if err := unlinkParents(root, rel); err != nil {
		return err
	}

	dst := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
	}
	if err := fsutil.AtomicWriteFile(dst, data, perm); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}

func unlinkParents(root, rel string) error {
	p := root
	for _, part := range fsutil.SplitPath(rel) {
		p = filepath.Join(p, part)

		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("checking %s: %w", p, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("removing symlink %s: %w", p, err)
		}
		return nil
	}
	return nil
}
