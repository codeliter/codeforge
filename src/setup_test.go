package src

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseReady(t *testing.T) {
	list := "anthropic          No key  (ANTHROPIC_API_KEY)\ncopilot            Ready  (COPILOT_API_KEY)\nopenai Ready\n"
	require.Equal(t, []string{"copilot", "openai"}, parseReady(list))
	require.Empty(t, parseReady("anthropic No key\n"))
}

func TestCheckGit(t *testing.T) {
	c := checkGit(t.Context())
	require.Equal(t, "git", c.name)
	require.NotEmpty(t, c.note)
}

func TestPrintChecks(t *testing.T) {
	var b strings.Builder
	printChecks(&b, []check{{name: "git", ok: true, note: "2.54"}, {name: "gh", note: "not signed in"}})
	require.Contains(t, b.String(), "✓ git")
	require.Contains(t, b.String(), "✗ gh")
}

func TestInstallEmpryo_NeedsConsent(t *testing.T) {
	var b strings.Builder
	_, err := installEmpryo(t.Context(), t.TempDir(), setupOptions{out: &b})
	require.ErrorIs(t, err, errNoConsent, "never runs the installer without a yes")
}
