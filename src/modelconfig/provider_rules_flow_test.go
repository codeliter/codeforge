package modelconfig

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

func routedCatalog() []string {
	return []string{
		"llmgateway/claude-sonnet-5-5", "llmgateway/claude-haiku-5-5", "llmgateway/gpt-6-1-sol",
		"copilot/claude-sonnet-5.5", "copilot/gpt-6.1-sol",
		"anthropic/claude-sonnet-5-5", "anthropic/claude-opus-5-5",
	}
}

func routedPolicy() *config.ProviderPolicy {
	return &config.ProviderPolicy{Primary: "llmgateway", Fallbacks: []string{"copilot"}, Rules: []config.ProviderRule{{Match: "claude-*", Primary: "anthropic"}}}
}

func TestRuleGlobNormalisation(t *testing.T) {
	policy := &config.ProviderPolicy{Rules: []config.ProviderRule{{Match: "Sonnet*", Primary: "anthropic"}, {Match: "gpt-6-?-sol", Primary: "copilot"}}}
	require.NoError(t, config.ValidateProviderPolicy(policy))
	for ref, want := range map[string]string{"Sonnet 5.5": "anthropic", "claude-sonnet-5.5": "anthropic", "GPT 6.1 Sol": "copilot", "gpt-6-1-sol": "copilot", "claude-opus-5.5": "", "gpt-6-12-sol": ""} {
		require.Equal(t, want, policy.PrimaryFor(ref), ref)
	}
	for _, match := range []string{"claude-[", "gpt-[3.5]", "gpt-\\*"} {
		require.ErrorIs(t, config.ValidateProviderPolicy(&config.ProviderPolicy{Rules: []config.ProviderRule{{Match: match, Primary: "copilot"}}}), config.ErrProviderPolicy, match)
	}
}

func TestProviderRuleArgsEqualsForm(t *testing.T) {
	rest, rules, err := providerRuleArgs([]string{"devpass", "--for=claude-*", "anthropic,copilot", "--global"})
	require.NoError(t, err)
	require.Equal(t, []string{"devpass", "--global"}, rest)
	require.Equal(t, []ruleArg{{match: "claude-*", ids: []string{"anthropic", "copilot"}}}, rules)
	_, _, err = providerRuleArgs([]string{"--for=claude-*"})
	require.ErrorIs(t, err, errProviderUsage)
}

func TestProviderSetKeepsWhatItWasNotGiven(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())
	require.NoError(t, config.WriteFile(home, config.Config{Model: "Sonnet 5.5", Review: config.ReviewConfig{Model: "GPT 6.1 Sol"}}))
	bin := filepath.Join(t.TempDir(), "empryo")
	require.NoError(t, os.WriteFile(bin, []byte(`#!/bin/sh
case "$1" in
 --list-models) printf '%s\n' 'llmgateway/claude-sonnet-5-5' 'llmgateway/gpt-6-1-sol' 'copilot/claude-sonnet-5.5' 'copilot/gpt-6.1-sol' 'llmgateway/claude-opus-5-5' 'copilot/claude-opus-5.5' 'llmgateway/claude-haiku-5-5' 'copilot/claude-haiku-5.5' 'llmgateway/gpt-6-luna' 'copilot/gpt-6-luna' 'llmgateway/kimi-k3' 'copilot/kimi-k3' ;;
 --list-providers) printf '%s\n' 'llmgateway  Ready' 'copilot  Ready' ;;
 *) exit 90 ;;
esac
`), execPerm))
	run := func(args ...string) *config.ProviderPolicy {
		var out strings.Builder
		require.NoError(t, RunProvider(t.Context(), home, bin, append([]string{"set"}, append(args, "--global")...), &out))
		cfg, err := config.ReadFile(home)
		require.NoError(t, err)
		return cfg.Providers
	}
	policy := run("llmgateway", "--for", "claude-*", "copilot")
	require.Equal(t, "llmgateway", policy.Primary)
	require.Len(t, policy.Rules, 1)
	policy = run("copilot", "llmgateway")
	require.Equal(t, "copilot", policy.Primary)
	require.Equal(t, []string{"llmgateway"}, policy.Backups())
	require.Len(t, policy.Rules, 1, "setting only a chain keeps the saved rules")
	policy = run("--for", "gpt-*", "copilot")
	require.Equal(t, "copilot", policy.Primary, "setting only --for keeps the saved chain")
	require.Equal(t, []string{"llmgateway"}, policy.Backups())
	require.Equal(t, []config.ProviderRule{{Match: "gpt-*", Primary: "copilot"}}, policy.Rules, "--for replaces the saved rules")
	policy = run("--no-rules")
	require.Equal(t, "copilot", policy.Primary)
	require.Empty(t, policy.Rules, "--no-rules removes only the rules")
	var out strings.Builder
	require.ErrorIs(t, RunProvider(t.Context(), home, bin, []string{"set", "--no-rules", "--for", "gpt-*", "copilot"}, &out), errProviderUsage)
	require.ErrorIs(t, RunProvider(t.Context(), home, bin, []string{"clear", "--no-rules"}, &out), errProviderUsage)
}

func TestRulesOnlyConfigRoundTripsAndProjectReplacesIt(t *testing.T) {
	home := t.TempDir()
	only := config.Config{Providers: &config.ProviderPolicy{Rules: []config.ProviderRule{{Match: "claude-*", Primary: "anthropic", Fallbacks: []string{"copilot"}}}}}
	require.NoError(t, config.WriteFile(home, only))
	got, err := config.ReadFile(home)
	require.NoError(t, err)
	require.Empty(t, got.Providers.Primary)
	require.Equal(t, only.Providers.Rules, got.Providers.Rules)
	merged := config.Merge(got, config.Config{Providers: &config.ProviderPolicy{Primary: "copilot"}})
	require.Empty(t, merged.Providers.Rules, "a project's providers block replaces the global one, rules included")
	merged = config.Merge(got, config.Config{})
	require.Equal(t, only.Providers.Rules, merged.Providers.Rules, "a project without providers inherits the rules")
}

func TestUseProviderDefaultsFollowsTheRule(t *testing.T) {
	cfg := config.Config{Model: "llmgateway/claude-sonnet-5-5", Expert: "llmgateway/gpt-6-1-sol", Init: "llmgateway/claude-haiku-5-5"}
	useProviderDefaults(&cfg, routedPolicy(), routedCatalog())
	require.Equal(t, "claude-sonnet-5-5", cfg.Model, "a pinned Claude ID becomes a name served by the rule's provider")
	require.Equal(t, "gpt-6-1-sol", cfg.Expert)
	require.Equal(t, "llmgateway/claude-haiku-5-5", cfg.Init, "a pin the rule's provider cannot serve stays explicit")
	require.Len(t, cfg.Providers.Rules, 1)
}

func TestProviderModelOptionsOfferEachModelOnItsOwnChain(t *testing.T) {
	var values, labels []string
	for _, option := range providerModelOptions(routedCatalog(), routedPolicy()) {
		values = append(values, option.Value)
		labels = append(labels, option.Label)
	}
	require.ElementsMatch(t, []string{"gpt-6-1-sol", "claude-sonnet-5-5", "claude-opus-5-5"}, values, "Claude models come from the rule's provider, the rest from the default one")
	for i, value := range values {
		if value == "gpt-6-1-sol" {
			require.Contains(t, labels[i], "backups: copilot/gpt-6.1-sol")
		} else {
			require.NotContains(t, labels[i], "backups", "a rule without backups shows none, not the default chain's")
		}
	}
	rulesOnly := &config.ProviderPolicy{Rules: []config.ProviderRule{{Match: "claude-*", Primary: "anthropic"}}}
	values = nil
	for _, option := range providerModelOptions(routedCatalog(), rulesOnly) {
		values = append(values, option.Value)
	}
	require.ElementsMatch(t, []string{"claude-sonnet-5-5", "claude-opus-5-5", "llmgateway/gpt-6-1-sol", "copilot/gpt-6.1-sol"}, values, "with no default order, models outside every rule are offered as explicit IDs")
}

func TestProviderModelOptionsKeepStandInsInFamily(t *testing.T) {
	catalog := []string{"anthropic/claude-opus-5-5", "openrouter/gpt-6-1-sol", "openrouter/claude-opus-5"}
	policy := &config.ProviderPolicy{Primary: "anthropic", Rules: []config.ProviderRule{{Match: "claude-*", Primary: "anthropic", Fallbacks: []string{"openrouter"}}}}
	for _, option := range providerModelOptions(catalog, policy) {
		if option.Value == "claude-opus-5-5" {
			require.Contains(t, option.Label, "openrouter/claude-opus-5 (closest)")
			require.NotContains(t, option.Label, "gpt")
		}
	}
	onlyGPT := []string{"anthropic/claude-opus-5-5", "openrouter/gpt-6-1-sol"}
	for _, option := range providerModelOptions(onlyGPT, policy) {
		if option.Value == "claude-opus-5-5" {
			require.Contains(t, option.Label, "nothing comparable", "a GPT model is not a stand-in under a Claude rule")
		}
	}
}

func TestRuleMatchRejectsProviderPrefix(t *testing.T) {
	require.ErrorIs(t, config.ValidateProviderPolicy(&config.ProviderPolicy{Rules: []config.ProviderRule{{Match: "anthropic/claude-*", Primary: "copilot"}}}), config.ErrProviderPolicy)
}

func TestProjectPreviewPinsOnlyNamesWhoseRuleWasDropped(t *testing.T) {
	baseline := &config.ProviderPolicy{Rules: []config.ProviderRule{{Match: "claude-*", Primary: "anthropic"}, {Match: "gpt-*", Primary: "copilot"}}}
	global := config.Config{Providers: baseline, Routing: map[string]string{"editing": "Sonnet 5.5", "search": "GPT 6.1 Sol"}}
	edited := config.Config{Providers: &config.ProviderPolicy{Rules: []config.ProviderRule{{Match: "claude-*", Primary: "anthropic"}}}}
	got, err := prepareProjectModelPreview(global, config.Config{}, global, &edited, routedCatalog())
	require.NoError(t, err)
	require.Equal(t, "Sonnet 5.5", got.Routing["editing"], "a name whose rule is kept stays a name")
	require.Equal(t, "copilot/gpt-6.1-sol", got.Routing["search"], "a name whose rule was dropped keeps its provider")
}

func TestPolicyKeysMatchBareSonnetPatterns(t *testing.T) {
	policy := &config.ProviderPolicy{Rules: []config.ProviderRule{{Match: "haiku*", Primary: "anthropic"}}}
	require.Equal(t, "anthropic", policy.PrimaryFor("Haiku 5.5"))
	require.Equal(t, "anthropic", policy.PrimaryFor("claude-haiku-5-5"))
}

func TestProviderSetInProjectScope(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Chdir(root)
	git := exec.CommandContext(t.Context(), "git", "init", "-q")
	git.Dir = root
	require.NoError(t, git.Run())
	require.NoError(t, config.WriteFile(home, config.Config{Providers: &config.ProviderPolicy{Primary: "llmgateway", Fallbacks: []string{"copilot"}}, Model: "Sonnet 5.5", Review: config.ReviewConfig{Model: "GPT 6.1 Sol"}}))
	bin := filepath.Join(t.TempDir(), "empryo")
	require.NoError(t, os.WriteFile(bin, []byte(`#!/bin/sh
case "$1" in
 --list-models) printf '%s\n' 'llmgateway/claude-sonnet-5-5' 'llmgateway/gpt-6-1-sol' 'copilot/claude-sonnet-5.5' 'copilot/gpt-6.1-sol' 'llmgateway/claude-opus-5-5' 'copilot/claude-opus-5.5' 'llmgateway/claude-haiku-5-5' 'copilot/claude-haiku-5.5' 'llmgateway/gpt-6-luna' 'copilot/gpt-6-luna' 'llmgateway/kimi-k3' 'copilot/kimi-k3' ;;
 --list-providers) printf '%s\n' 'llmgateway  Ready' 'copilot  Ready' ;;
 *) exit 90 ;;
esac
`), execPerm))
	var out strings.Builder
	require.NoError(t, RunProvider(t.Context(), home, bin, []string{"set", "--for", "claude-*", "copilot", "--project"}, &out))
	require.Contains(t, out.String(), "copied from your global order", "the user is told the inherited chain was copied")
	global, err := config.ReadFile(home)
	require.NoError(t, err)
	require.Empty(t, global.Providers.Rules, "the global file is untouched")
	local, err := config.ReadTrustedProject(home, root, nil)
	require.NoError(t, err)
	require.Equal(t, "llmgateway", local.Providers.Primary, "the project keeps the global chain it did not change")
	require.Len(t, local.Providers.Rules, 1)
	out.Reset()
	require.NoError(t, RunProvider(t.Context(), home, bin, []string{"set", "copilot", "--project"}, &out))
	require.NotContains(t, out.String(), "copied", "a project that already has its own policy copies nothing")
	err = RunProvider(t.Context(), home, bin, []string{"set", "--no-rules", "--project"}, &out)
	require.NoError(t, err)
	local, err = config.ReadTrustedProject(home, root, nil)
	require.NoError(t, err)
	require.Empty(t, local.Providers.Rules)
}

func TestNoRulesOnRulesOnlyPolicyPointsToClear(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())
	require.NoError(t, config.WriteFile(home, config.Config{Providers: &config.ProviderPolicy{Rules: []config.ProviderRule{{Match: "claude-*", Primary: "copilot"}}}}))
	bin := filepath.Join(t.TempDir(), "empryo")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\ncase \"$1\" in\n --list-models) printf '%s\\n' 'copilot/claude-sonnet-5.5' ;;\n --list-providers) printf '%s\\n' 'copilot  Ready' ;;\n *) exit 90 ;;\nesac\n"), execPerm))
	var out strings.Builder
	err := RunProvider(t.Context(), home, bin, []string{"set", "--no-rules", "--global"}, &out)
	require.ErrorIs(t, err, errProviderUsage)
	require.Contains(t, err.Error(), "provider clear")
}

func TestRecommendationsFollowRules(t *testing.T) {
	catalog := append(routedCatalog(), "anthropic/claude-haiku-5-5", "llmgateway/gpt-6-luna", "copilot/gpt-6-luna", "llmgateway/kimi-k3", "copilot/kimi-k3")
	policy := routedPolicy()
	rec := recommendConfigured(catalog, config.Config{Providers: policy}, false)
	require.NotEmpty(t, rec["model"])
	for key, names := range rec {
		for _, name := range names {
			require.NotContains(t, name, "/", key)
			route, err := config.ResolveModelReference(key, name, policy, catalog)
			require.NoError(t, err, name)
			require.Equal(t, policy.PrimaryFor(name), strings.Split(route.Primary, "/")[0], name)
		}
	}
	require.Equal(t, []string{"claude-opus-5-5"}, rec["model"])
}

func TestCoverageIgnoresModelsARuleRoutesElsewhere(t *testing.T) {
	cfg := config.Config{Providers: routedPolicy(), Model: "Sonnet 5.5", Review: config.ReviewConfig{Model: "GPT 6.1 Sol"}}
	catalog := []string{"copilot/gpt-6.1-sol"}
	standIns, missing := providerCoverage(cfg, "copilot", catalog)
	require.Empty(t, standIns)
	require.Empty(t, missing, "Sonnet runs on the rule's chain, so the default backup need not cover it")
	preview := providerChainPreview("global", []string{"llmgateway", "copilot"}, cfg.Providers.Rules...)
	require.Contains(t, preview, "Models claude-*: anthropic")
}

func TestProjectPreviewKeepsRuleRoutedNamesPinned(t *testing.T) {
	global := config.Config{Providers: routedPolicy(), Routing: map[string]string{"editing": "Sonnet 5.5", "search": "GPT 6.1 Sol"}}
	edited := config.Config{}
	got, err := prepareProjectModelPreview(global, config.Config{}, global, &edited, routedCatalog())
	require.NoError(t, err)
	require.Equal(t, "anthropic/claude-sonnet-5-5", got.Routing["editing"], "the rule's provider, not the default one, is kept")
	require.Equal(t, "llmgateway/gpt-6-1-sol", got.Routing["search"])
}

func TestVerifyFallbacksAcceptsABackupThatIsAnotherFamilysPrimary(t *testing.T) {
	plan := config.ModelPlan{Routes: []config.ModelRoute{
		{Role: "model", Primary: "copilot/claude-opus-5.5", Fallbacks: []string{"openrouter/claude-opus-5.5"}},
		{Role: "review.model", Primary: "llmgateway/gpt-6-1-sol", Fallbacks: []string{"copilot/claude-opus-5.5"}},
	}}
	require.NoError(t, plan.VerifyFallbacks(plan.FallbackChains()), "a backup keeps the chain declared for it")
	native := plan.FallbackChains()
	native["copilot/claude-opus-5.5"] = []string{"custom/undeclared"}
	require.ErrorIs(t, plan.VerifyFallbacks(native), config.ErrNativeFallback, "anything not declared is still refused")
}
