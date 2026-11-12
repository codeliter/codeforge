package modelconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
)

func TestProviderConfigureHelpers(t *testing.T) {
	catalog := append(threeProviderCatalog(), "small/claude-sonnet-5.5")
	cfg := config.Config{Model: "Sonnet 5.5", Review: config.ReviewConfig{Model: "GPT 6.1 Sol"}, Expert: "custom/pinned", Routing: map[string]string{"editing": "Sonnet 5.5", "research": ""}}
	_, missing := providerCoverage(cfg, "copilot", catalog)
	require.Empty(t, missing)
	_, missing = providerCoverage(cfg, "small", catalog)
	require.Equal(t, []string{"GPT 6.1 Sol"}, missing, "explicit IDs and empty routing never count as missing")
	require.Equal(t, "nothing comparable to GPT 6.1 Sol", providerDetail(cfg, "small", catalog))
	require.Contains(t, providerDetail(cfg, "llmgateway", catalog), "DevPass")
	require.Equal(t, "serves every saved model", providerDetail(cfg, "copilot", catalog))
	var values []string
	for _, option := range providerOptions(cfg, catalogProviders(catalog), catalog) {
		values = append(values, option.Value)
		require.Equal(t, "Connected providers", option.Group)
	}
	require.Equal(t, []string{"copilot", "llmgateway", "openrouter", "small"}, values, "every connected provider is offered")
	preview := providerChainPreview("project", []string{"llmgateway", "copilot", "openrouter"})
	require.Contains(t, preview, "Primary   llmgateway")
	require.Contains(t, preview, "Backup 2  openrouter")
	require.Contains(t, providerChainPreview("global", []string{"copilot"}), "No backups")
}

func TestProviderConfigureNeedsTerminal(t *testing.T) {
	if forms.Interactive() {
		t.Skip("non-interactive behaviour")
	}
	var out strings.Builder
	require.ErrorIs(t, RunProvider(t.Context(), t.TempDir(), "/missing", []string{"configure"}, &out), errProviderConfigureTerminal)
	require.ErrorIs(t, RunProvider(t.Context(), t.TempDir(), "/missing", []string{"configure", "copilot"}, &out), errProviderUsage)
}

// scriptedAsker answers the menu's questions in order and records what each one offered.
type scriptedAsker struct {
	t        *testing.T
	answers  []string
	asked    []string
	offered  [][]string
	defaults [][]string // the order each order question started from
}

func (a *scriptedAsker) next(title string) string {
	a.t.Helper()
	require.NotEmpty(a.t, a.answers, "unexpected question %q", title)
	a.asked = append(a.asked, title)
	answer := a.answers[0]
	a.answers = a.answers[1:]
	return answer
}

func (a *scriptedAsker) choose(title, _ string, options []forms.Option, value *string) error {
	var values []string
	for _, option := range options {
		values = append(values, option.Value)
	}
	a.offered = append(a.offered, values)
	if answer := a.next(title); answer != "=" {
		require.Contains(a.t, values, answer, title)
		*value = answer
	}
	return nil
}

// order answers with a comma-separated order; "=" keeps the one offered.
func (a *scriptedAsker) order(title, _ string, options []forms.Option, values *[]string) error {
	var offered []string
	for _, option := range options {
		offered = append(offered, option.Value)
	}
	a.offered = append(a.offered, offered)
	a.defaults = append(a.defaults, slices.Clone(*values))
	if answer := a.next(title); answer != "=" {
		*values = strings.Split(answer, ",")
		for _, id := range *values {
			require.Contains(a.t, offered, id, title)
		}
	}
	return nil
}

func (a *scriptedAsker) confirm(title, _ string, value *bool) error {
	*value = a.next(title) == "yes"
	return nil
}

func TestProviderConfigureFlow(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	require.NoError(t, config.WriteFile(home, namedRolesConfig()))
	catalog := append(threeProviderCatalog(), "small/claude-sonnet-5.5")
	var out strings.Builder
	ask := &scriptedAsker{t: t, answers: []string{"llmgateway,copilot,openrouter", "yes", "no"}}
	apply, err := pickProviderOrder(ask, home, root, false, "global", catalog, &out)
	require.NoError(t, err)
	require.Empty(t, apply, "saying no to apply stops after saving")
	require.Equal(t, []string{"Provider order", "Save this provider order?", "Apply it to Empryo now?"}, ask.asked)
	require.Equal(t, []string{"copilot", "llmgateway", "openrouter", "small"}, ask.offered[0], "one screen offers every connected provider")
	require.Equal(t, []string{"llmgateway"}, ask.defaults[0], "llmgateway is the default primary")
	cfg, err := config.ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, "llmgateway", cfg.Providers.Primary)
	require.Equal(t, []string{"copilot", "openrouter"}, cfg.Providers.Backups(), "backups are saved in the order ticked")
	require.Contains(t, out.String(), "Saved.")

	ask = &scriptedAsker{t: t, answers: []string{"=", "yes", "yes"}}
	apply, err = pickProviderOrder(ask, home, root, false, "global", catalog, &out)
	require.NoError(t, err)
	require.Equal(t, "global", apply, "yes to apply hands over to models apply for this scope")
	cfg, err = config.ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, []string{"copilot", "openrouter"}, cfg.Providers.Backups(), "re-running and sending it as offered keeps the saved order")
	require.Equal(t, []string{"llmgateway", "copilot", "openrouter"}, ask.defaults[0], "the saved order is ticked to start with")

	ask = &scriptedAsker{t: t, answers: []string{"llmgateway,small"}}
	_, err = pickProviderOrder(ask, home, root, false, "global", catalog, &out)
	require.ErrorIs(t, err, config.ErrModelName, "a backup with nothing of the same level is refused")
	cfg, err = config.ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, []string{"copilot", "openrouter"}, cfg.Providers.Backups(), "a refused order changes nothing")

	ask = &scriptedAsker{t: t, answers: []string{"llmgateway", "no"}}
	_, err = pickProviderOrder(ask, home, root, false, "global", catalog, &out)
	require.ErrorIs(t, err, errProviderConfigureCancelled)
}

func TestProviderConfigureScopeQuestion(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	require.NoError(t, config.WriteFile(home, namedRolesConfig()))
	ask := &scriptedAsker{t: t, answers: []string{"=", "=", "yes", "no"}}
	var out strings.Builder
	_, err := pickProviderOrder(ask, home, root, true, "", threeProviderCatalog(), &out)
	require.NoError(t, err)
	require.Equal(t, []string{"project", "global"}, ask.offered[0])
	data, err := os.ReadFile(config.ProjectPath(root))
	require.NoError(t, err, "inside a Git project the default scope is this project")
	require.Contains(t, string(data), "primary: llmgateway")
	cfg, err := config.Load(home, root, nil)
	require.NoError(t, err)
	require.Equal(t, "llmgateway", cfg.Providers.Primary, "the saved project file is approved and used")
}

// namedRolesConfig names the coder and reviewer; other roles are pinned IDs, which never move between providers.
func namedRolesConfig() config.Config {
	return config.Config{Model: "Sonnet 5.5", Init: "Sonnet 5.5", Expert: "custom/expert", Judges: []string{"custom/judge"}, Review: config.ReviewConfig{Model: "GPT 6.1 Sol", SecondOpinion: []string{"custom/second"}}}
}

func threeProviderCatalog() []string {
	return []string{
		"llmgateway/claude-sonnet-5-5", "llmgateway/gpt-6-1-sol",
		"copilot/claude-sonnet-5.5", "copilot/gpt-6.1-sol",
		"openrouter/claude-sonnet-5.5", "openrouter/gpt-6.1-sol",
	}
}

func TestProviderChainResolvesEveryBackupInOrder(t *testing.T) {
	policy, err := providerChain([]string{"DevPass", "copilot", "openrouter"}, catalogProviders(threeProviderCatalog()))
	require.NoError(t, err)
	require.Equal(t, "llmgateway", policy.Primary)
	require.Equal(t, []string{"copilot", "openrouter"}, policy.Backups())
	route, err := config.ResolveModelReference("model", "Sonnet 5.5", policy, threeProviderCatalog())
	require.NoError(t, err)
	require.Equal(t, "llmgateway/claude-sonnet-5-5", route.Primary)
	require.Equal(t, []string{"copilot/claude-sonnet-5.5", "openrouter/claude-sonnet-5.5"}, route.Fallbacks, "backups keep the order given")
	plan := config.ModelPlan{Routes: []config.ModelRoute{route}}
	require.Equal(t, map[string][]string{route.Primary: route.Fallbacks}, plan.FallbackChains())
	require.NoError(t, plan.VerifyFallbacks(plan.FallbackChains()))
	reordered := map[string][]string{route.Primary: {route.Fallbacks[1], route.Fallbacks[0]}}
	require.ErrorIs(t, plan.VerifyFallbacks(reordered), config.ErrNativeFallback, "Empryo must try backups in the chosen order")
	hop := plan.FallbackChains()
	hop[route.Fallbacks[1]] = []string{"custom/undeclared"}
	require.ErrorIs(t, plan.VerifyFallbacks(hop), config.ErrNativeFallback, "a later backup may not route somewhere undeclared")
}

func TestProviderChainRejectsBadInput(t *testing.T) {
	connected := catalogProviders(threeProviderCatalog())
	for _, ids := range [][]string{{"copilot", "copilot"}, {"devpass", "llmgateway"}, {"offline"}, {"a/b"}} {
		_, err := providerChain(ids, connected)
		require.ErrorIs(t, err, config.ErrProviderPolicy, ids)
	}
	_, _, err := providerArgs([]string{"copilot", "--project", "--global"})
	require.ErrorIs(t, err, errProviderUsage)
	_, _, err = providerArgs([]string{"--weird"})
	require.ErrorIs(t, err, errProviderUsage)
	ids, scope, err := providerArgs([]string{"copilot", "--global", "openrouter"})
	require.NoError(t, err)
	require.Equal(t, []string{"copilot", "openrouter"}, ids)
	require.Equal(t, "global", scope)
}

func TestProviderPolicyKeepsOldSingleFallbackKey(t *testing.T) {
	old := &config.ProviderPolicy{Primary: "devpass", Fallback: "copilot", Fallbacks: []string{"openrouter"}}
	require.Equal(t, []string{"copilot", "openrouter"}, old.Backups(), "fallback stays first, then the list")
	require.NoError(t, config.ValidateProviderPolicy(old))
	require.ErrorIs(t, config.ValidateProviderPolicy(&config.ProviderPolicy{Primary: "copilot", Fallbacks: []string{"openrouter", "copilot"}}), config.ErrProviderPolicy)
	require.True(t, sameProviderPolicy(old, config.NewProviderPolicy("llmgateway", []string{"copilot", "openrouter"})), "the two spellings of one order are equal")
	clone := old.Clone()
	clone.Fallbacks[0] = "changed"
	require.Equal(t, "openrouter", old.Fallbacks[0])
}

func TestProviderSetSavesOnlyTheProviderKey(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Chdir(root)
	require.NoError(t, config.WriteFile(home, config.Config{Model: "Sonnet 5.5", Review: config.ReviewConfig{Model: "GPT 6.1 Sol"}, Routing: map[string]string{"editing": "Sonnet 5.5"}}))
	bin := filepath.Join(t.TempDir(), "empryo")
	require.NoError(t, os.WriteFile(bin, []byte(`#!/bin/sh
case "$1" in
 --list-models) printf '%s\n' 'llmgateway/claude-sonnet-5-5' 'llmgateway/gpt-6-1-sol' 'copilot/claude-sonnet-5.5' 'copilot/gpt-6.1-sol' 'openrouter/claude-sonnet-5.5' ;;
 --list-providers) printf '%s\n' 'llmgateway  Ready' 'copilot  Ready' 'openrouter  Ready' ;;
 *) exit 90 ;;
esac
`), execPerm))
	var out strings.Builder
	require.NoError(t, RunProvider(t.Context(), home, bin, []string{"set", "devpass", "copilot", "--global"}, &out))
	require.Contains(t, out.String(), "llmgateway > copilot")
	cfg, err := config.ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, "llmgateway", cfg.Providers.Primary)
	require.Equal(t, []string{"copilot"}, cfg.Providers.Backups())
	require.Equal(t, "Sonnet 5.5", cfg.Model, "other saved choices are kept")
	require.Equal(t, "Sonnet 5.5", cfg.Routing["editing"])
	out.Reset()
	err = RunProvider(t.Context(), home, bin, []string{"set", "devpass", "copilot", "openrouter", "--global"}, &out)
	require.ErrorIs(t, err, config.ErrModelName, "openrouter has no frontier model to stand in for GPT 6.1 Sol, so the order is not saved half-working")
	cfg, err = config.ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, []string{"copilot"}, cfg.Providers.Backups(), "a failed set leaves the saved order alone")
	out.Reset()
	require.NoError(t, RunProvider(t.Context(), home, bin, nil, &out))
	require.Contains(t, out.String(), "Primary: llmgateway")
	require.Contains(t, out.String(), "Backup 1: copilot")
	require.NoError(t, RunProvider(t.Context(), home, bin, []string{"clear", "--global"}, &out))
	cfg, err = config.ReadFile(home)
	require.NoError(t, err)
	require.Nil(t, cfg.Providers)
}
