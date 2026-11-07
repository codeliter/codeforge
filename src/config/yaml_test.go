package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codeliter/codeforge/src/fsutil"

	"github.com/stretchr/testify/require"
)

func TestLoadGlobalYAMLConfiguration(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(Dir(home), fsutil.PrivateDir))
	require.NoError(t, os.WriteFile(filepath.Join(Dir(home), "config.yaml"), []byte("model: example/coder\nreview:\n  model: other/reviewer\n"), fsutil.PrivateFile))
	cfg, err := LoadGlobal(home)
	require.NoError(t, err)
	require.Equal(t, "example/coder", cfg.Model)
	require.Equal(t, "other/reviewer", cfg.Review.Model)
}

func TestValidateRouting_RejectsUnknownJobs(t *testing.T) {
	require.ErrorIs(t, validateRouting(map[string]string{"namer": "provider/model"}), ErrRoutingJob)
}
