package fsutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitPath(t *testing.T) {
	require.Equal(t, []string{"a", "b", "c"}, SplitPath(filepath.Join("a", "b", "c")))
	require.Empty(t, SplitPath("."))
	require.Empty(t, SplitPath(""))
	require.Equal(t, []string{"tmp", "file"}, SplitPath(filepath.Join(string(filepath.Separator), "tmp", "file")), "an absolute path ends at the root")
}

func TestSymlinked(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "real"), PrivateDir))
	require.NoError(t, os.Symlink(other, filepath.Join(root, "link")))
	require.False(t, Symlinked(root, filepath.Join("real", "file")))
	require.True(t, Symlinked(root, "link"))
	require.True(t, Symlinked(root, filepath.Join("link", "file")), "a linked directory on the way counts")
}
