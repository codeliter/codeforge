package fsutil

import (
	"os"
	"path/filepath"
)

// Symlinked reports whether rel, or any directory on the way to it from root, is a symbolic link.
func Symlinked(root, rel string) bool {
	p := root
	for _, part := range SplitPath(rel) {
		p = filepath.Join(p, part)
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// SplitPath is the elements of a relative path, outermost first. An absolute path gives its elements below the root.
func SplitPath(rel string) []string {
	var parts []string
	for rel != "." && rel != "" {
		parent := filepath.Dir(rel)
		if parent == rel { // the root: it has no parent to walk to
			break
		}
		parts = append([]string{filepath.Base(rel)}, parts...)
		rel = parent
	}
	return parts
}
