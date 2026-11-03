package src

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
	require.NoError(t, atomicWriteFile(path, a, privateFile))
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 30; i++ {
			body := a
			if i%2 == 0 {
				body = b
			}
			if err := atomicWriteFile(path, body, privateFile); err != nil {
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

func TestUnchangedProjectProfilesKeepTheirFileIdentity(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Model: "provider/coder", Review: ReviewConfig{Model: "other/reviewer"}}
	require.NoError(t, syncProjectAgents(root, cfg))
	path := filepath.Join(root, ".empryo", "agents", "codeforge-work.md")
	before, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, syncProjectAgents(root, cfg))
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
}
