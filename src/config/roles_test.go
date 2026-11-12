package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApply_RejectsWhatARoleCannotTake(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		models []string
		want   error
	}{
		{"unknown role", "nope", []string{"a/b"}, ErrUnknownRole},
		{"no models", "model", nil, ErrNoModels},
		{"two for a single slot", "model", []string{"a/b", "c/d"}, ErrOneModel},
		{"not a model reference", "judges", []string{"a/b", "not!a/model/ref"}, ErrNotProviderModel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults(Config{})
			before := Clone(cfg)
			require.ErrorIs(t, Apply(&cfg, tt.key, tt.models), tt.want)
			require.Equal(t, before, cfg, "a rejected change leaves the configuration alone")
		})
	}
}

func TestApply_SetsEveryRoleAndCurrentReadsItBack(t *testing.T) {
	for _, role := range Roles {
		t.Run(role.Key, func(t *testing.T) {
			models := []string{"a/one", "b/two"}[:role.Count]
			cfg := Defaults(Config{})
			require.NoError(t, Apply(&cfg, role.Key, models))
			require.Equal(t, models, Current(cfg, role.Key))
		})
	}
}

func TestCurrent_UnknownOrEmptyIsNothing(t *testing.T) {
	require.Nil(t, Current(Config{}, "model"))
	require.Nil(t, Current(Defaults(Config{}), "nope"))
}

func TestFindRoleAndRoleKeys(t *testing.T) {
	role, ok := FindRole("judges")
	require.True(t, ok)
	require.Equal(t, 2, role.Count)
	_, ok = FindRole("nope")
	require.False(t, ok)
	for _, r := range Roles {
		require.Contains(t, RoleKeys(), r.Key)
	}
}

func TestModelNameFamilyAndKey(t *testing.T) {
	require.Equal(t, "claude-opus-5.5", ModelName("copilot/claude-opus-5.5"))
	require.Equal(t, "claude-opus-5.5", ModelName("claude-opus-5.5"))
	require.Equal(t, "claude", Family("copilot/claude-opus-5.5"))
	require.Equal(t, "gpt", Family("openai/gpt-6.1-sol"))
	require.Equal(t, Family("copilot/gpt-6.1-sol"), Family("openai/gpt-5.5"))
	require.Equal(t, "claude-opus-5-5", ModelKey("Claude_Opus-5.5"))
}

func TestProviderChains(t *testing.T) {
	var none *ProviderPolicy
	require.Empty(t, none.PrimaryFor("claude-opus-5.5"))
	require.Nil(t, none.Backups())
	require.Nil(t, none.Clone())

	policy := NewProviderPolicy("copilot", []string{"anthropic"})
	policy.Rules = []ProviderRule{{Match: "Sonnet*", Primary: "anthropic", Fallbacks: []string{"copilot"}}}

	require.Equal(t, "anthropic", policy.PrimaryFor("claude-sonnet-5.5"))
	require.Equal(t, []string{"copilot"}, policy.ChainFor("claude-sonnet-5.5").Backups())
	require.Equal(t, "copilot", policy.PrimaryFor("gpt-6.1-sol"))
	require.Same(t, policy, policy.ChainFor("gpt-6.1-sol"))

	clone := policy.Clone()
	clone.Fallbacks[0] = "changed"
	clone.Rules[0].Fallbacks[0] = "changed"
	require.Equal(t, []string{"anthropic"}, policy.Fallbacks)
	require.Equal(t, []string{"copilot"}, policy.Rules[0].Fallbacks)
}

func TestVerifyFallbacks(t *testing.T) {
	plan := ModelPlan{Routes: []ModelRoute{
		{Primary: "copilot/m", Fallbacks: []string{"anthropic/m"}},
		{Primary: "copilot/solo"},
	}}
	require.Equal(t, map[string][]string{"copilot/m": {"anthropic/m"}}, plan.FallbackChains())

	require.NoError(t, plan.VerifyFallbacks(map[string][]string{"copilot/m": {"anthropic/m"}}))
	require.NoError(t, plan.VerifyFallbacks(map[string][]string{"copilot/m": {"anthropic/m"}, "anthropic/m": {"copilot/m"}}))

	err := plan.VerifyFallbacks(map[string][]string{})
	require.ErrorIs(t, err, ErrNativeFallback)
	require.ErrorContains(t, err, "copilot/m needs exactly")

	err = plan.VerifyFallbacks(map[string][]string{"copilot/m": {"anthropic/m"}, "anthropic/m": {"openai/other"}})
	require.ErrorIs(t, err, ErrNativeFallback)
	require.ErrorContains(t, err, "undeclared model openai/other")
}
