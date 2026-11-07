//nolint:err113 // dynamic error messages are okay in tests
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(Dir(home), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(Dir(home), "config.json"), []byte(body), 0o600))
}

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := LoadGlobal(t.TempDir())
	require.NoError(t, err)
	require.Equal(t, DefaultModel, cfg.Model)
	require.Equal(t, DefaultReviewModel, cfg.Review.Model)
	require.Equal(t, "copilot/deepseek-v4.1-flash", cfg.Review.Light)
	require.NotEqual(t, cfg.Model, cfg.Review.Model)
	require.Contains(t, cfg.Review.SecondOpinion, "copilot/kimi-k3")
	require.Len(t, cfg.Judges, 2)
}

func TestLoadConfig_FileAndEnv(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{"model":"a/code","judges":["a/b"],"review":{"model":"b/rev","second_opinion":["c/x","d/y"]}}`)

	cfg, err := LoadGlobal(home)
	require.NoError(t, err)
	require.Equal(t, "a/code", cfg.Model)
	require.Equal(t, []string{"a/b"}, cfg.Judges)
	require.Equal(t, ReviewConfig{Model: "b/rev", SecondOpinion: []string{"c/x", "d/y"}, Light: DefaultLightModel}, cfg.Review, "an explicit reviewer keeps the light default")

	t.Setenv("CODEFORGE_JUDGES", "c/d, e/f")
	t.Setenv("CODEFORGE_REVIEW_MODEL", "z/rev")
	t.Setenv("CODEFORGE_REVIEW_SECOND_OPINION", "e/q")
	cfg, err = LoadGlobal(home)
	require.NoError(t, err)
	require.Equal(t, []string{"c/d", "e/f"}, cfg.Judges)
	require.Equal(t, ReviewConfig{Model: "z/rev", SecondOpinion: []string{"e/q"}, Light: DefaultLightModel}, cfg.Review)
}

func TestLoadConfig_ReviewerMustDifferFromCoder(t *testing.T) {
	t.Setenv("CODEFORGE_MODEL", "a/same")
	t.Setenv("CODEFORGE_REVIEW_MODEL", "a/same")

	_, err := LoadGlobal(t.TempDir())
	require.ErrorIs(t, err, ErrReviewerIsCoder)
}

func TestLightModelCannotBeTheCoder(t *testing.T) {
	require.ErrorIs(t, Validate(Config{Model: "a/code", Review: ReviewConfig{Model: "b/rev", Light: "a/code"}}), ErrLightIsCoder)
	require.NoError(t, Validate(Config{Model: "a/code", Review: ReviewConfig{Model: "b/rev", Light: "c/cheap"}}))
}

func TestLightModelLayersAndEnvironment(t *testing.T) {
	merged := Merge(Config{Review: ReviewConfig{Model: "b/rev", Light: "c/old"}}, Config{Review: ReviewConfig{Light: "c/new"}})
	require.Equal(t, "c/new", merged.Review.Light)
	require.Equal(t, "b/rev", merged.Review.Model)
	t.Setenv("CODEFORGE_REVIEW_LIGHT_MODEL", "e/light")
	require.Equal(t, "e/light", Environment(Config{}).Review.Light)
}

func TestLoadConfig_SecondOpinionMustDifferFromReviewer(t *testing.T) {
	t.Setenv("CODEFORGE_REVIEW_MODEL", "a/rev")
	t.Setenv("CODEFORGE_REVIEW_SECOND_OPINION", "a/rev")

	_, err := LoadGlobal(t.TempDir())
	require.ErrorIs(t, err, ErrSecondOpinionIsReviewer)
}

func TestLoadConfig_SecondOpinionMustDifferFromCoder(t *testing.T) {
	t.Setenv("CODEFORGE_MODEL", "a/coder")
	t.Setenv("CODEFORGE_REVIEW_SECOND_OPINION", "a/coder")

	_, err := LoadGlobal(t.TempDir())
	require.ErrorIs(t, err, ErrSecondOpinionIsCoder)
}

func TestConfigFileRoundTrip(t *testing.T) {
	home := t.TempDir()
	cfg := Config{Model: "a/b", Review: ReviewConfig{SecondOpinion: []string{"c/d"}}}
	require.NoError(t, WriteFile(home, cfg))

	got, err := ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, cfg, got)
}
