package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func providerTestCatalog() []string {
	return []string{"llmgateway/claude-sonnet-5-5", "llmgateway/claude-opus-5-5", "llmgateway/gpt-6-1-sol", "copilot/claude-sonnet-5.5", "copilot/claude-opus-5.5", "copilot/gpt-6.1-sol"}
}

func TestProviderModelResolution(t *testing.T) {
	policy := &ProviderPolicy{Primary: "DevPass", Fallback: "copilot"}
	for _, name := range []string{"Sonnet 5.5", "sonnet-5.5", "claude-sonnet-5.5"} {
		route, err := ResolveModelReference("model", name, policy, providerTestCatalog())
		require.NoError(t, err)
		require.Equal(t, "llmgateway/claude-sonnet-5-5", route.Primary)
		require.Equal(t, []string{"copilot/claude-sonnet-5.5"}, route.Fallbacks)
		require.False(t, route.Explicit)
	}
	route, err := ResolveModelReference("expert", "custom/private-model", policy, nil)
	require.NoError(t, err)
	require.True(t, route.Explicit)
	require.Empty(t, route.Fallbacks)
	_, err = ResolveModelReference("model", "Sonnet 5.5", nil, providerTestCatalog())
	require.ErrorIs(t, err, ErrAmbiguousModel)
	_, err = ResolveModelReference("model", "unknown", policy, providerTestCatalog())
	require.ErrorIs(t, err, ErrModelName)
	_, err = ResolveModelReference("model", "Opus 5.5", policy, []string{"llmgateway/claude-opus-5.5"})
	require.ErrorIs(t, err, ErrModelName)
}

func TestDefaultFableFallsBackToOpusOnAProviderWithoutIt(t *testing.T) {
	policy := &ProviderPolicy{Primary: "copilot"}
	route, err := ResolveModelReference("expert", ModelName(DefaultExpertModel), policy, []string{"copilot/claude-opus-5.5", "copilot/claude-sonnet-5.5"})
	require.NoError(t, err)
	require.Equal(t, "copilot/claude-opus-5.5", route.Primary)
	_, err = ResolveModelReference("expert", "claude-opus-5", policy, []string{"copilot/claude-sonnet-5.5"})
	require.ErrorIs(t, err, ErrModelName, "a model the user chose still fails loudly")
}

func TestProviderPlanAliasesCannotDuplicateReviewers(t *testing.T) {
	cfg := Config{Providers: &ProviderPolicy{Primary: "devpass"}, Model: "Sonnet 5.5", Review: ReviewConfig{Model: "claude-sonnet-5-5"}}
	_, err := ResolvePlan(cfg, providerTestCatalog())
	require.ErrorIs(t, err, ErrReviewerIsCoder)
	cfg.Review.Model = "GPT 6.1 Sol"
	cfg.Judges = []string{"Sonnet 5.5", "claude-sonnet-5-5"}
	_, err = ResolvePlan(cfg, providerTestCatalog())
	require.ErrorIs(t, err, ErrDuplicateModels)
}

func TestResolvePlan_TheLightReviewerGetsItsProvider(t *testing.T) {
	cfg := Defaults(Config{}) // every role a full id
	cfg.Providers = &ProviderPolicy{Primary: "copilot"}
	cfg.Review.Light = "deepseek-v4.1-flash" // as Defaults writes it under a provider policy
	require.True(t, NeedsCatalog(cfg))
	plan, err := ResolvePlan(cfg, []string{"copilot/deepseek-v4.1-flash"})
	require.NoError(t, err)
	require.Equal(t, "copilot/deepseek-v4.1-flash", plan.Config.Review.Light)

	plan, err = ResolvePlan(cfg, []string{"copilot/other"})
	require.NoError(t, err, "a light reviewer that cannot be found never fails the plan")
	require.Empty(t, plan.Config.Review.Light, "it is dropped, so the normal reviewer takes docs")
}
