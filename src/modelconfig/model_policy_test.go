package modelconfig

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/modelcatalog"
)

func TestRecommendationsUseConnectedProviderAliases(t *testing.T) {
	rec := recommend([]string{"llmgateway/claude-sonnet-5-5", "llmgateway/claude-opus-5-5", "copilot/gpt-6.1-sol", "copilot/kimi-k3"})
	require.Equal(t, []string{"llmgateway/claude-opus-5-5"}, rec["model"])
	require.Equal(t, []string{"llmgateway/claude-opus-5-5"}, rec["expert"])
}

func TestArgonIsNotSuggestedOrOffered(t *testing.T) {
	blocked := "copilot/gemini-4-argon"
	catalog := []string{blocked, "copilot/claude-sonnet-5.5", "copilot/claude-opus-5.5", "copilot/gpt-6.1-sol"}
	for _, options := range [][]string{catalog, {blocked}} {
		for _, option := range modelOptions(options, blocked) {
			require.NotEqual(t, blocked, option.Value)
		}
	}
	for _, values := range recommend(catalog) {
		require.NotContains(t, values, blocked)
	}
	parsed := modelcatalog.ParseModels(strings.NewReader("  " + blocked + "  1000k ctx\n"))
	require.Empty(t, parsed)
}

func TestModelCatalogAllowsNamespacedModels(t *testing.T) {
	models := modelcatalog.ParseModels(strings.NewReader("  openrouter/anthropic/claude-sonnet-4-5  200k ctx\n"))
	require.Equal(t, []string{"openrouter/anthropic/claude-sonnet-4-5"}, models)
}
