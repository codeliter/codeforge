//go:build darwin || linux

package fsutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenameNoReplace(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	require.NoError(t, os.MkdirAll(from, PrivateDir))
	require.NoError(t, os.WriteFile(filepath.Join(from, "a"), []byte("x"), PrivateFile))
	require.NoError(t, os.Mkdir(to, PrivateDir))
	require.ErrorIs(t, RenameNoReplace(from, to), fs.ErrExist, "even an empty directory already there is not replaced")
	require.NoError(t, os.Remove(to))
	require.NoError(t, RenameNoReplace(from, to))
	data, err := os.ReadFile(filepath.Join(to, "a"))
	require.NoError(t, err)
	require.Equal(t, "x", string(data))

	file, taken := filepath.Join(dir, "file"), filepath.Join(dir, "taken")
	require.NoError(t, os.WriteFile(file, []byte("new"), PrivateFile))
	require.NoError(t, os.WriteFile(taken, []byte("old"), PrivateFile))
	require.ErrorIs(t, RenameNoReplace(file, taken), fs.ErrExist, "nor is a file")
	data, err = os.ReadFile(taken)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))
	require.NoError(t, RenameNoReplace(file, filepath.Join(dir, "free")), "a file moves as a directory does")
}
