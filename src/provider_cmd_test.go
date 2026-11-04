package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/stretchr/testify/require"
)

func TestProviderConfigureHelpers(t *testing.T) {
	catalog := append(threeProviderCatalog(), "small/claude-sonnet-5.5")
	cfg := Config{Model: "Sonnet 5.5", Review: ReviewConfig{Model: "GPT 6.1 Sol"}, Expert: "custom/pinned", Routing: map[string]string{"editing": "Sonnet 5.5", "research": ""}}
	require.Empty(t, missingModels(cfg, "copilot", catalog))
	require.Equal(t, []string{"GPT 6.1 Sol"}, missingModels(cfg, "small", catalog), "explicit IDs and empty routing never count as missing")
	require.Equal(t, "small — nothing comparable to GPT 6.1 Sol", providerLabel("small", missingModels(cfg, "small", catalog)))
	require.Contains(t, providerLabel("llmgateway", nil), "DevPass")
	var values, labels []string
	for _, option := range backupOptions(cfg, catalogProviders(catalog), []string{"llmgateway", "copilot"}, catalog) {
		values = append(values, option.Value)
		labels = append(labels, option.Key)
	}
	require.Equal(t, []string{providerDone, "openrouter", "small"}, values, "Done first, then providers not yet in the chain")
	require.Contains(t, labels, "small — nothing comparable to GPT 6.1 Sol")
	preview := providerChainPreview("project", []string{"llmgateway", "copilot", "openrouter"})
	require.Contains(t, preview, "Primary   llmgateway")
	require.Contains(t, preview, "Backup 2  openrouter")
	require.Contains(t, providerChainPreview("global", []string{"copilot"}), "No backups")
}

func TestProviderConfigureNeedsTerminal(t *testing.T) {
	if interactive() {
		t.Skip("non-interactive behaviour")
	}
	var out strings.Builder
	require.ErrorIs(t, runProvider(t.Context(), t.TempDir(), "/missing", []string{"configure"}, &out), errProviderConfigureTerminal)
	require.ErrorIs(t, runProvider(t.Context(), t.TempDir(), "/missing", []string{"configure", "copilot"}, &out), errProviderUsage)
}

// scriptedAsker answers the menu's questions in order and records what each one offered.
type scriptedAsker struct {
	t       *testing.T
	answers []string
	asked   []string
	offered [][]string
}

func (a *scriptedAsker) next(title string) string {
	a.t.Helper()
	require.NotEmpty(a.t, a.answers, "unexpected question %q", title)
	a.asked = append(a.asked, title)
	answer := a.answers[0]
	a.answers = a.answers[1:]
	return answer
}

func (a *scriptedAsker) choose(title, _ string, options []huh.Option[string], value *string) error {
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

func (a *scriptedAsker) confirm(title, _ string, value *bool) error {
	*value = a.next(title) == "yes"
	return nil
}

func TestProviderConfigureFlow(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	require.NoError(t, writeConfigFile(home, namedRolesConfig()))
	catalog := append(threeProviderCatalog(), "small/claude-sonnet-5.5")
	var out strings.Builder
	ask := &scriptedAsker{t: t, answers: []string{"=", "copilot", "openrouter", providerDone, "yes", "no"}}
	apply, err := pickProviderOrder(ask, home, root, false, "global", catalog, &out)
	require.NoError(t, err)
	require.Empty(t, apply, "saying no to apply stops after saving")
	require.Equal(t, []string{"Primary provider", "Backup 1", "Backup 2", "Backup 3", "Save this provider order?", "Apply it to Empryo now?"}, ask.asked)
	require.Equal(t, []string{providerDone, "copilot", "openrouter", "small"}, ask.offered[1], "Backup 1 offers every provider except the primary")
	require.Equal(t, []string{providerDone, "openrouter", "small"}, ask.offered[2], "a chosen backup is not offered again")
	cfg, err := readConfigFile(home)
	require.NoError(t, err)
	require.Equal(t, "llmgateway", cfg.Providers.Primary, "llmgateway is the default primary")
	require.Equal(t, []string{"copilot", "openrouter"}, cfg.Providers.Backups(), "backups are saved in the order picked")
	require.Contains(t, out.String(), "Saved.")

	ask = &scriptedAsker{t: t, answers: []string{"=", "=", "=", "=", "yes", "yes"}}
	apply, err = pickProviderOrder(ask, home, root, false, "global", catalog, &out)
	require.NoError(t, err)
	require.Equal(t, "global", apply, "yes to apply hands over to models apply for this scope")
	cfg, err = readConfigFile(home)
	require.NoError(t, err)
	require.Equal(t, []string{"copilot", "openrouter"}, cfg.Providers.Backups(), "re-running and pressing Enter keeps the saved order")

	ask = &scriptedAsker{t: t, answers: []string{"=", "small", providerDone}}
	_, err = pickProviderOrder(ask, home, root, false, "global", catalog, &out)
	require.ErrorIs(t, err, errModelName, "a backup with nothing of the same level is refused")
	cfg, err = readConfigFile(home)
	require.NoError(t, err)
	require.Equal(t, []string{"copilot", "openrouter"}, cfg.Providers.Backups(), "a refused order changes nothing")

	ask = &scriptedAsker{t: t, answers: []string{"=", providerDone, "no"}}
	_, err = pickProviderOrder(ask, home, root, false, "global", catalog, &out)
	require.ErrorIs(t, err, errProviderConfigureCancelled)
}

func TestProviderConfigureScopeQuestion(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	require.NoError(t, writeConfigFile(home, namedRolesConfig()))
	ask := &scriptedAsker{t: t, answers: []string{"=", "=", providerDone, "yes", "no"}}
	var out strings.Builder
	_, err := pickProviderOrder(ask, home, root, true, "", threeProviderCatalog(), &out)
	require.NoError(t, err)
	require.Equal(t, []string{"project", "global"}, ask.offered[0])
	data, err := os.ReadFile(projectConfigPath(root))
	require.NoError(t, err, "inside a Git project the default scope is this project")
	require.Contains(t, string(data), "primary: llmgateway")
	cfg, err := loadConfigAt(home, root)
	require.NoError(t, err)
	require.Equal(t, "llmgateway", cfg.Providers.Primary, "the saved project file is approved and used")
}

func TestProviderMenuScreensRender(t *testing.T) {
	cfg := namedRolesConfig()
	catalog := append(threeProviderCatalog(), "small/claude-sonnet-5.5")
	pick := providerDone
	backup := huh.NewSelect[string]().Title("Backup 1").Description("Order so far: llmgateway").Options(backupOptions(cfg, catalogProviders(catalog), []string{"llmgateway"}, catalog)...).Value(&pick)
	form := huh.NewForm(huh.NewGroup(backup)).WithWidth(100)
	form.Init()
	view := form.View()
	for _, want := range []string{"Backup 1", "Order so far: llmgateway", "Done — no more backups", "copilot", "openrouter", "small — nothing comparable to GPT 6.1 Sol"} {
		require.Contains(t, view, want)
	}
	save := true
	confirm := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Save this provider order?").Description(providerChainPreview("global", []string{"llmgateway", "copilot", "openrouter"})).Value(&save))).WithWidth(100)
	confirm.Init()
	for _, want := range []string{"Save this provider order?", "Primary", "llmgateway", "Backup 1", "copilot", "Backup 2", "openrouter"} {
		require.Contains(t, confirm.View(), want)
	}
}

// namedRolesConfig names the coder and reviewer; other roles are pinned IDs, which never move between providers.
func namedRolesConfig() Config {
	return Config{Model: "Sonnet 5.5", Init: "Sonnet 5.5", Expert: "custom/expert", Judges: []string{"custom/judge"}, Review: ReviewConfig{Model: "GPT 6.1 Sol", SecondOpinion: []string{"custom/second"}}}
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
	route, err := resolveModelReference("model", "Sonnet 5.5", policy, threeProviderCatalog())
	require.NoError(t, err)
	require.Equal(t, "llmgateway/claude-sonnet-5-5", route.Primary)
	require.Equal(t, []string{"copilot/claude-sonnet-5.5", "openrouter/claude-sonnet-5.5"}, route.Fallbacks, "backups keep the order given")
	plan := ModelPlan{Routes: []ModelRoute{route}}
	require.Equal(t, map[string][]string{route.Primary: route.Fallbacks}, plan.fallbackChains())
	require.NoError(t, plan.verifyFallbacks(plan.fallbackChains()))
	reordered := map[string][]string{route.Primary: {route.Fallbacks[1], route.Fallbacks[0]}}
	require.ErrorIs(t, plan.verifyFallbacks(reordered), errNativeFallback, "Empryo must try backups in the chosen order")
	hop := plan.fallbackChains()
	hop[route.Fallbacks[1]] = []string{"custom/undeclared"}
	require.ErrorIs(t, plan.verifyFallbacks(hop), errNativeFallback, "a later backup may not route somewhere undeclared")
}

func TestProviderChainRejectsBadInput(t *testing.T) {
	connected := catalogProviders(threeProviderCatalog())
	for _, ids := range [][]string{{"copilot", "copilot"}, {"devpass", "llmgateway"}, {"offline"}, {"a/b"}} {
		_, err := providerChain(ids, connected)
		require.ErrorIs(t, err, errProviderPolicy, ids)
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
	old := &ProviderPolicy{Primary: "devpass", Fallback: "copilot", Fallbacks: []string{"openrouter"}}
	require.Equal(t, []string{"copilot", "openrouter"}, old.Backups(), "fallback stays first, then the list")
	require.NoError(t, validateProviderPolicy(old))
	require.ErrorIs(t, validateProviderPolicy(&ProviderPolicy{Primary: "copilot", Fallbacks: []string{"openrouter", "copilot"}}), errProviderPolicy)
	require.True(t, sameProviderPolicy(old, newProviderPolicy("llmgateway", []string{"copilot", "openrouter"})), "the two spellings of one order are equal")
	clone := old.clone()
	clone.Fallbacks[0] = "changed"
	require.Equal(t, "openrouter", old.Fallbacks[0])
}

func TestProviderSetSavesOnlyTheProviderKey(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Chdir(root)
	require.NoError(t, writeConfigFile(home, Config{Model: "Sonnet 5.5", Review: ReviewConfig{Model: "GPT 6.1 Sol"}, Routing: map[string]string{"editing": "Sonnet 5.5"}}))
	bin := filepath.Join(t.TempDir(), "empryo")
	require.NoError(t, os.WriteFile(bin, []byte(`#!/bin/sh
case "$1" in
 --list-models) printf '%s\n' 'llmgateway/claude-sonnet-5-5' 'llmgateway/gpt-6-1-sol' 'copilot/claude-sonnet-5.5' 'copilot/gpt-6.1-sol' 'openrouter/claude-sonnet-5.5' ;;
 --list-providers) printf '%s\n' 'llmgateway  Ready' 'copilot  Ready' 'openrouter  Ready' ;;
 *) exit 90 ;;
esac
`), execPerm))
	var out strings.Builder
	require.NoError(t, runProvider(t.Context(), home, bin, []string{"set", "devpass", "copilot", "--global"}, &out))
	require.Contains(t, out.String(), "llmgateway > copilot")
	cfg, err := readConfigFile(home)
	require.NoError(t, err)
	require.Equal(t, "llmgateway", cfg.Providers.Primary)
	require.Equal(t, []string{"copilot"}, cfg.Providers.Backups())
	require.Equal(t, "Sonnet 5.5", cfg.Model, "other saved choices are kept")
	require.Equal(t, "Sonnet 5.5", cfg.Routing["editing"])
	out.Reset()
	err = runProvider(t.Context(), home, bin, []string{"set", "devpass", "copilot", "openrouter", "--global"}, &out)
	require.ErrorIs(t, err, errModelName, "openrouter has no frontier model to stand in for GPT 6.1 Sol, so the order is not saved half-working")
	cfg, err = readConfigFile(home)
	require.NoError(t, err)
	require.Equal(t, []string{"copilot"}, cfg.Providers.Backups(), "a failed set leaves the saved order alone")
	out.Reset()
	require.NoError(t, runProvider(t.Context(), home, bin, nil, &out))
	require.Contains(t, out.String(), "Primary: llmgateway")
	require.Contains(t, out.String(), "Backup 1: copilot")
	require.NoError(t, runProvider(t.Context(), home, bin, []string{"clear", "--global"}, &out))
	cfg, err = readConfigFile(home)
	require.NoError(t, err)
	require.Nil(t, cfg.Providers)
}
