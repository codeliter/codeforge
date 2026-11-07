package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

func claudeRulePolicy() *config.ProviderPolicy {
	return &config.ProviderPolicy{
		Primary:   "devpass",
		Fallbacks: []string{"copilot"},
		Rules:     []config.ProviderRule{{Match: "claude-*", Primary: "copilot", Fallbacks: []string{"openrouter"}}},
	}
}

func TestProviderRuleChoosesChainPerFamily(t *testing.T) {
	catalog := threeProviderCatalog()
	for _, name := range []string{"Sonnet 5.5", "sonnet-5.5", "claude-sonnet-5.5"} {
		route, err := config.ResolveModelReference("model", name, claudeRulePolicy(), catalog)
		require.NoError(t, err, name)
		require.Equal(t, "copilot/claude-sonnet-5.5", route.Primary, name)
		require.Equal(t, []string{"openrouter/claude-sonnet-5.5"}, route.Fallbacks, name)
	}
	route, err := config.ResolveModelReference("review.model", "GPT 6.1 Sol", claudeRulePolicy(), catalog)
	require.NoError(t, err)
	require.Equal(t, "llmgateway/gpt-6-1-sol", route.Primary, "a model no rule matches uses the default chain")
	require.Equal(t, []string{"copilot/gpt-6.1-sol"}, route.Fallbacks)
	policy := claudeRulePolicy()
	policy.Rules = []config.ProviderRule{{Match: "Sonnet-*", Primary: "openrouter"}, {Match: "claude-*", Primary: "copilot"}}
	route, err = config.ResolveModelReference("model", "Sonnet 5.5", policy, catalog)
	require.NoError(t, err)
	require.Equal(t, "openrouter/claude-sonnet-5.5", route.Primary, "Sonnet-* means claude-sonnet-*, and the first matching rule wins")
	require.Empty(t, route.Fallbacks, "a rule's chain replaces the default backups")
	fully, err := config.ResolveModelReference("model", "custom/private-model", claudeRulePolicy(), nil)
	require.NoError(t, err)
	require.True(t, fully.Explicit, "fully qualified IDs bypass rules")
	only := &config.ProviderPolicy{Rules: []config.ProviderRule{{Match: "claude-*", Primary: "copilot"}}}
	require.NoError(t, config.ValidateProviderPolicy(only), "rules work without a default chain")
	route, err = config.ResolveModelReference("model", "Sonnet 5.5", only, catalog)
	require.NoError(t, err)
	require.Equal(t, "copilot/claude-sonnet-5.5", route.Primary)
	_, err = config.ResolveModelReference("model", "GPT 6.1 Sol", only, catalog)
	require.ErrorIs(t, err, config.ErrAmbiguousModel, "names outside every rule still need a default provider")
}

func TestProviderRuleStandInStaysInFamily(t *testing.T) {
	catalog := []string{"anthropic/claude-opus-5-5", "openrouter/gpt-6-1-sol"}
	plain := &config.ProviderPolicy{Primary: "anthropic", Fallbacks: []string{"openrouter"}}
	route, err := config.ResolveModelReference("model", "Opus 5.5", plain, catalog)
	require.NoError(t, err)
	require.Equal(t, []string{"openrouter/gpt-6-1-sol"}, route.Fallbacks, "without a rule another vendor's model of the same level stands in")
	ruled := &config.ProviderPolicy{Primary: "anthropic", Rules: []config.ProviderRule{{Match: "claude-*", Primary: "anthropic", Fallbacks: []string{"openrouter"}}}}
	_, err = config.ResolveModelReference("model", "Opus 5.5", ruled, catalog)
	require.ErrorIs(t, err, config.ErrModelName, "a Claude rule never falls back to a GPT model")
	catalog = append(catalog, "openrouter/claude-opus-5")
	route, err = config.ResolveModelReference("model", "Opus 5.5", ruled, catalog)
	require.NoError(t, err)
	require.Equal(t, []string{"openrouter/claude-opus-5"}, route.Fallbacks)
	require.True(t, route.StandIns["openrouter/claude-opus-5"])
}

func TestProviderRuleValidation(t *testing.T) {
	bad := map[string][]config.ProviderRule{
		"empty match":     {{Match: " ", Primary: "copilot"}},
		"bad glob":        {{Match: "claude-[", Primary: "copilot"}},
		"duplicate match": {{Match: "claude-*", Primary: "copilot"}, {Match: "Claude-*", Primary: "openrouter"}},
		"no primary":      {{Match: "gpt-*"}},
		"bad provider":    {{Match: "gpt-*", Primary: "a/b"}},
		"repeat provider": {{Match: "gpt-*", Primary: "copilot", Fallbacks: []string{"copilot"}}},
	}
	for name, rules := range bad {
		require.ErrorIs(t, config.ValidateProviderPolicy(&config.ProviderPolicy{Primary: "devpass", Rules: rules}), config.ErrProviderPolicy, name)
	}
	require.NoError(t, config.ValidateProviderPolicy(claudeRulePolicy()))
}

func TestProviderRulesCloneAndCompare(t *testing.T) {
	policy := claudeRulePolicy()
	clone := policy.Clone()
	clone.Rules[0].Fallbacks[0] = "changed"
	clone.Rules[0].Match = "gpt-*"
	require.Equal(t, "openrouter", policy.Rules[0].Fallbacks[0], "clone copies rules deeply")
	require.Equal(t, "claude-*", policy.Rules[0].Match)
	require.True(t, sameProviderPolicy(policy, claudeRulePolicy()))
	require.False(t, sameProviderPolicy(policy, clone), "models_delta sees a changed rule")
	plain := policy.Clone()
	plain.Rules = nil
	require.False(t, sameProviderPolicy(policy, plain), "models_delta sees a removed rule")
	spelled := claudeRulePolicy()
	spelled.Rules[0] = config.ProviderRule{Match: "Claude-*", Primary: "copilot", Fallbacks: []string{"openrouter"}}
	require.True(t, sameProviderPolicy(policy, spelled), "spelling of a pattern is not a change")
	global := config.Config{Providers: policy}
	edited := config.Clone(global)
	edited.Providers.Rules = nil
	delta := projectModelDelta(config.Config{}, global, edited)
	require.NotNil(t, delta.Providers)
	require.Empty(t, delta.Providers.Rules, "a project that removes the rules saves that")
}

func TestProviderRuleArgs(t *testing.T) {
	rest, rules, err := providerRuleArgs([]string{"devpass", "--for", "claude-*", "anthropic,copilot", "--global", "--for", "gpt-*", "openrouter"})
	require.NoError(t, err)
	require.Equal(t, []string{"devpass", "--global"}, rest)
	require.Equal(t, []ruleArg{{match: "claude-*", ids: []string{"anthropic", "copilot"}}, {match: "gpt-*", ids: []string{"openrouter"}}}, rules)
	for _, args := range [][]string{{"--for"}, {"--for", "claude-*"}, {"--for", "claude-*", "--global"}, {"--for", "claude-*", ","}} {
		_, _, err := providerRuleArgs(args)
		require.ErrorIs(t, err, errProviderUsage, args)
	}
	connected := catalogProviders(threeProviderCatalog())
	_, err = providerRules([]ruleArg{{match: "claude-*", ids: []string{"offline"}}}, connected)
	require.ErrorIs(t, err, config.ErrProviderPolicy)
	got, err := providerRules([]ruleArg{{match: "claude-*", ids: []string{"DevPass", "copilot"}}}, connected)
	require.NoError(t, err)
	require.Equal(t, []config.ProviderRule{{Match: "claude-*", Primary: "llmgateway", Fallbacks: []string{"copilot"}}}, got)
}

func TestProviderSetSavesRules(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Chdir(root)
	require.NoError(t, config.WriteFile(home, config.Config{Model: "Sonnet 5.5", Review: config.ReviewConfig{Model: "GPT 6.1 Sol"}}))
	bin := filepath.Join(t.TempDir(), "empryo")
	require.NoError(t, os.WriteFile(bin, []byte(`#!/bin/sh
case "$1" in
 --list-models) printf '%s\n' 'llmgateway/claude-sonnet-5-5' 'llmgateway/gpt-6-1-sol' 'copilot/claude-sonnet-5.5' 'copilot/gpt-6.1-sol' 'llmgateway/claude-opus-5-5' 'copilot/claude-opus-5.5' 'llmgateway/claude-haiku-5-5' 'copilot/claude-haiku-5.5' 'llmgateway/gpt-6-luna' 'copilot/gpt-6-luna' ;;
 --list-providers) printf '%s\n' 'llmgateway  Ready' 'copilot  Ready' ;;
 *) exit 90 ;;
esac
`), execPerm))
	var out strings.Builder
	require.NoError(t, runProvider(t.Context(), home, bin, []string{"set", "llmgateway", "--for", "claude-*", "copilot", "--global"}, &out))
	require.Contains(t, out.String(), "claude-*: copilot")
	cfg, err := config.ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, "llmgateway", cfg.Providers.Primary)
	require.Equal(t, []config.ProviderRule{{Match: "claude-*", Primary: "copilot"}}, cfg.Providers.Rules, "rules survive the config round trip")
	require.Equal(t, "Sonnet 5.5", cfg.Model, "other saved choices are kept")
	out.Reset()
	require.NoError(t, runProvider(t.Context(), home, bin, nil, &out))
	require.Contains(t, out.String(), "Primary: llmgateway")
	require.Contains(t, out.String(), "Models claude-*: copilot")
	err = runProvider(t.Context(), home, bin, []string{"set", "llmgateway", "--for", "claude-*", "offline", "--global"}, &out)
	require.ErrorIs(t, err, config.ErrProviderPolicy, "a rule needs connected providers")
	cfg, err = config.ReadFile(home)
	require.NoError(t, err)
	require.Len(t, cfg.Providers.Rules, 1, "a refused set changes nothing")
	require.ErrorIs(t, runProvider(t.Context(), home, bin, []string{"clear", "--for", "claude-*", "copilot"}, &out), errProviderUsage)
	require.NoError(t, runProvider(t.Context(), home, bin, []string{"clear", "--global"}, &out))
	cfg, err = config.ReadFile(home)
	require.NoError(t, err)
	require.Nil(t, cfg.Providers, "clear removes the rules with the order")
}
