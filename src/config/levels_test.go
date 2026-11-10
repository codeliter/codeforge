package config

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
		got, ok := EquivalentModel(c.want, c.provider, catalog)
		require.True(t, ok, c.want+" on "+c.provider)
		require.Equal(t, c.got, got, c.want+" on "+c.provider)
	}
	standIn, ok := EquivalentModel("claude-fable-5.2", "copilot", []string{"copilot/claude-sonnet-5.5", "copilot/claude-opus-5.5"})
	require.True(t, ok)
	require.Equal(t, "copilot/claude-opus-5.5", standIn, "Opus stands in for Fable where Fable is not served")
	_, ok = EquivalentModel("claude-opus-5.5", "fast", catalog)
	require.False(t, ok, "a fast-only provider has nothing at the frontier level")
	_, ok = EquivalentModel("claude-opus-5.5", "none", catalog)
	require.False(t, ok, "unknown models never stand in")
	got, ok := EquivalentModel("kimi-k3", "copilot", catalog)
	require.True(t, ok)
	require.Equal(t, "copilot/gpt-6.1-sol", got, "another vendor's frontier model stands in, newest first")
}
