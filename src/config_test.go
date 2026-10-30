//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(configDir(home), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configDir(home), "config.json"), []byte(body), 0o600))
}

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := loadConfig(t.TempDir())
	require.NoError(t, err)
	require.Equal(t, defaultModel, cfg.Model)
	require.Equal(t, defaultReviewModel, cfg.Review.Model)
	require.NotEqual(t, cfg.Model, cfg.Review.Model)
	require.Contains(t, cfg.Review.SecondOpinion, "copilot/kimi-k3")
	require.Len(t, cfg.Judges, 2)
}

func TestLoadConfig_FileAndEnv(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{"model":"a/code","judges":["a/b"],"review":{"model":"b/rev","second_opinion":["c/x","d/y"]}}`)

	cfg, err := loadConfig(home)
	require.NoError(t, err)
	require.Equal(t, "a/code", cfg.Model)
	require.Equal(t, []string{"a/b"}, cfg.Judges)
	require.Equal(t, ReviewConfig{Model: "b/rev", SecondOpinion: []string{"c/x", "d/y"}}, cfg.Review)

	t.Setenv("CODEFORGE_JUDGES", "c/d, e/f")
	t.Setenv("CODEFORGE_REVIEW_MODEL", "z/rev")
	t.Setenv("CODEFORGE_REVIEW_SECOND_OPINION", "e/q")
	cfg, err = loadConfig(home)
	require.NoError(t, err)
	require.Equal(t, []string{"c/d", "e/f"}, cfg.Judges)
	require.Equal(t, ReviewConfig{Model: "z/rev", SecondOpinion: []string{"e/q"}}, cfg.Review)
}

func TestLoadConfig_ReviewerMustDifferFromCoder(t *testing.T) {
	t.Setenv("CODEFORGE_MODEL", "a/same")
	t.Setenv("CODEFORGE_REVIEW_MODEL", "a/same")

	_, err := loadConfig(t.TempDir())
	require.ErrorIs(t, err, errReviewerIsCoder)
}

func TestLoadConfig_SecondOpinionMustDifferFromReviewer(t *testing.T) {
	t.Setenv("CODEFORGE_REVIEW_MODEL", "a/rev")
	t.Setenv("CODEFORGE_REVIEW_SECOND_OPINION", "a/rev")

	_, err := loadConfig(t.TempDir())
	require.ErrorIs(t, err, errSecondOpinionIsReviewer)
}

func TestLoadConfig_SecondOpinionMustDifferFromCoder(t *testing.T) {
	t.Setenv("CODEFORGE_MODEL", "a/coder")
	t.Setenv("CODEFORGE_REVIEW_SECOND_OPINION", "a/coder")

	_, err := loadConfig(t.TempDir())
	require.ErrorIs(t, err, errSecondOpinionIsCoder)
}

func TestConfigFileRoundTrip(t *testing.T) {
	home := t.TempDir()
	cfg := Config{Model: "a/b", Review: ReviewConfig{SecondOpinion: []string{"c/d"}}}
	require.NoError(t, writeConfigFile(home, cfg))

	got, err := readConfigFile(home)
	require.NoError(t, err)
	require.Equal(t, cfg, got)
}
