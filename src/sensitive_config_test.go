//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSensitivePaths_ProjectAddsAndNeverRemoves(t *testing.T) {
	global := Config{Review: ReviewConfig{SensitivePaths: []string{"auth/", "*.sql"}}}
	local := Config{Review: ReviewConfig{SensitivePaths: []string{"payments/", "auth/"}}}
	merged := mergeConfig(global, local)
	require.Equal(t, []string{"auth/", "*.sql", "payments/"}, merged.Review.SensitivePaths)
	require.Equal(t, []string{"auth/", "*.sql"}, global.Review.SensitivePaths, "the global list is not changed in place")

	require.Equal(t, global.Review.SensitivePaths, mergeConfig(global, Config{}).Review.SensitivePaths, "a project without the key keeps the global list")
}

func TestSensitivePaths_AreValidated(t *testing.T) {
	require.NoError(t, validate(Config{Review: ReviewConfig{SensitivePaths: []string{"auth/", "src/guard/*.go", "*.sql"}}}))
	for _, bad := range []string{"", "/etc", "../secrets", "["} {
		err := validate(Config{Review: ReviewConfig{SensitivePaths: []string{bad}}})
		require.ErrorIs(t, err, errSensitivePath, bad)
	}
}

func TestSensitivePaths_SurviveACloneAndAYAMLRoundTrip(t *testing.T) {
	cfg := Config{Review: ReviewConfig{SensitivePaths: []string{"auth/"}}}
	clone := cloneConfig(cfg)
	clone.Review.SensitivePaths[0] = "changed/"
	require.Equal(t, "auth/", cfg.Review.SensitivePaths[0], "cloneConfig copies the list")

	dir := t.TempDir()
	text, err := saveConfigYAML(dir, cfg)
	require.NoError(t, err)
	require.Contains(t, text, "sensitive_paths")
	require.Contains(t, text, "auth/")
}
