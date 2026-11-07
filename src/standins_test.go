package src

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
)

func TestProviderBackupUsesClosestModelInsteadOfFailing(t *testing.T) {
	catalog := []string{"llmgateway/claude-sonnet-5-5", "copilot/claude-sonnet-5", "copilot/claude-opus-5"}
	policy := config.NewProviderPolicy("devpass", []string{"copilot"})
	route, err := config.ResolveModelReference("model", "Sonnet 5.5", policy, catalog)
	require.NoError(t, err)
	require.Equal(t, "llmgateway/claude-sonnet-5-5", route.Primary)
	require.Equal(t, []string{"copilot/claude-sonnet-5"}, route.Fallbacks, "the backup keeps working with its nearest Sonnet")
	require.True(t, route.StandIns["copilot/claude-sonnet-5"])
	require.Equal(t, []string{"copilot/claude-sonnet-5 (closest)"}, markStandIns(route))
	cfg := config.Config{Model: "Sonnet 5.5"}
	standIns, missing := providerCoverage(cfg, "copilot", catalog)
	require.Equal(t, []string{"Sonnet 5.5 → claude-sonnet-5"}, standIns)
	require.Empty(t, missing)
	require.Equal(t, "copilot — uses Sonnet 5.5 → claude-sonnet-5", coverageLabel(cfg, "copilot", catalog))
	exact, err := config.ResolveModelReference("model", "Sonnet 5.5", policy, append(catalog, "copilot/claude-sonnet-5.5"))
	require.NoError(t, err)
	require.Equal(t, []string{"copilot/claude-sonnet-5.5"}, exact.Fallbacks, "the exact model always wins over a stand-in")
	require.Empty(t, exact.StandIns)
}
