package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadGlobalYAMLConfiguration(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(configDir(home), privateDir))
	require.NoError(t, os.WriteFile(filepath.Join(configDir(home), "config.yaml"), []byte("model: example/coder\nreview:\n  model: other/reviewer\n"), privateFile))
	cfg, err := loadConfig(home)
	require.NoError(t, err)
	require.Equal(t, "example/coder", cfg.Model)
	require.Equal(t, "other/reviewer", cfg.Review.Model)
}

func TestModelCatalogAllowsNamespacedModels(t *testing.T) {
	models := parseModelList(strings.NewReader("  openrouter/anthropic/claude-sonnet-4-5  200k ctx\n"))
	require.Equal(t, []string{"openrouter/anthropic/claude-sonnet-4-5"}, models)
}
