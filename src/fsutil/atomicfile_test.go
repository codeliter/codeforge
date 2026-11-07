package fsutil

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAtomicManagedFileReadersSeeCompleteVersions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "profile.md")
	a, b := bytes.Repeat([]byte("a"), 128*1024), bytes.Repeat([]byte("b"), 128*1024)
	require.NoError(t, AtomicWriteFile(path, a, PrivateFile))
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 30; i++ {
			body := a
			if i%2 == 0 {
				body = b
			}
			if err := AtomicWriteFile(path, body, PrivateFile); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for {
		select {
		case err := <-done:
			require.NoError(t, err)
			return
		default:
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.True(t, bytes.Equal(data, a) || bytes.Equal(data, b), "readers must never see a truncated profile")
		}
	}
}
