package src

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
)

func TestUnchangedProjectProfilesKeepTheirFileIdentity(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Model: "provider/coder", Review: config.ReviewConfig{Model: "other/reviewer"}}
	require.NoError(t, syncProjectAgents(root, cfg))
	path := filepath.Join(root, ".empryo", "agents", "codeforge-work.md")
	before, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, syncProjectAgents(root, cfg))
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
}
