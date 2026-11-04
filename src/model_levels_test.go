package src

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEquivalentModelPrefersSameLineThenLevel(t *testing.T) {
	catalog := []string{
		"copilot/claude-sonnet-5", "copilot/claude-sonnet-4.5", "copilot/claude-opus-5", "copilot/gpt-6.1-sol", "copilot/claude-haiku-5.5",
		"other/gpt-6-sol", "other/claude-sonnet-6",
		"fast/gemini-3.8-flash", "fast/gpt-6-luna",
		"none/custom-thing",
	}
	cases := []struct{ want, provider, got string }{
		{"claude-sonnet-5.5", "copilot", "copilot/claude-sonnet-5"},
		{"claude-opus-5.5", "copilot", "copilot/claude-opus-5"},
		{"gpt-6.1-sol", "other", "other/gpt-6-sol"},
		{"claude-sonnet-5.5", "other", "other/claude-sonnet-6"},
		{"claude-haiku-5.5", "fast", "fast/gpt-6-luna"},
		{"Opus 5.5", "copilot", "copilot/claude-opus-5"},
	}
	for _, c := range cases {
		got, ok := equivalentModel(c.want, c.provider, catalog)
		require.True(t, ok, c.want+" on "+c.provider)
		require.Equal(t, c.got, got, c.want+" on "+c.provider)
	}
	_, ok := equivalentModel("claude-opus-5.5", "fast", catalog)
	require.False(t, ok, "a fast-only provider has nothing at the frontier level")
	_, ok = equivalentModel("claude-opus-5.5", "none", catalog)
	require.False(t, ok, "unknown models never stand in")
	got, ok := equivalentModel("kimi-k3", "copilot", catalog)
	require.True(t, ok)
	require.Equal(t, "copilot/gpt-6.1-sol", got, "another vendor's frontier model stands in, newest first")
}

func TestProviderBackupUsesClosestModelInsteadOfFailing(t *testing.T) {
	catalog := []string{"llmgateway/claude-sonnet-5-5", "copilot/claude-sonnet-5", "copilot/claude-opus-5"}
	policy := newProviderPolicy("devpass", []string{"copilot"})
	route, err := resolveModelReference("model", "Sonnet 5.5", policy, catalog)
	require.NoError(t, err)
	require.Equal(t, "llmgateway/claude-sonnet-5-5", route.Primary)
	require.Equal(t, []string{"copilot/claude-sonnet-5"}, route.Fallbacks, "the backup keeps working with its nearest Sonnet")
	require.True(t, route.StandIns["copilot/claude-sonnet-5"])
	require.Equal(t, []string{"copilot/claude-sonnet-5 (closest)"}, markStandIns(route))
	cfg := Config{Model: "Sonnet 5.5"}
	standIns, missing := providerCoverage(cfg, "copilot", catalog)
	require.Equal(t, []string{"Sonnet 5.5 → claude-sonnet-5"}, standIns)
	require.Empty(t, missing)
	require.Equal(t, "copilot — uses Sonnet 5.5 → claude-sonnet-5", coverageLabel(cfg, "copilot", catalog))
	exact, err := resolveModelReference("model", "Sonnet 5.5", policy, append(catalog, "copilot/claude-sonnet-5.5"))
	require.NoError(t, err)
	require.Equal(t, []string{"copilot/claude-sonnet-5.5"}, exact.Fallbacks, "the exact model always wins over a stand-in")
	require.Empty(t, exact.StandIns)
}
