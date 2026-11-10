package src

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

func TestModelsApplyRequiresConfirmationAndPreservesSavedChoices(t *testing.T) {
	if interactive() {
		t.Skip("non-interactive apply")
	}
	home := t.TempDir()
	require.NoError(t, config.WriteFile(home, config.Config{Model: "saved/coder", Review: config.ReviewConfig{Model: "saved/reviewer"}}))
	path := filepath.Join(config.Dir(home), "config.yaml")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "empryo")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' 'copilot/claude-sonnet-5.5' 'copilot/claude-opus-5.5' 'copilot/claude-haiku-5.5' 'copilot/gpt-6.1-sol' 'copilot/kimi-k3'\n"), 0700))
	require.Error(t, runModels(t.Context(), home, bin, []string{"apply"}), "apply must ask before changing native settings, not replace saved roles with recommendations")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
func TestModelApplyScopeSelection(t *testing.T) {
	require.Equal(t, "project", defaultModelApplyScope(true))
	require.Equal(t, "global", defaultModelApplyScope(false))
	for _, scope := range []string{"project", "global"} {
		value, err := parseModelApplyScope([]string{"--" + scope})
		require.NoError(t, err)
		require.Equal(t, scope, value)
	}
	value, err := parseModelApplyScope(nil)
	require.NoError(t, err)
	require.Empty(t, value)
	for _, args := range [][]string{{"--global", "--project"}, {"--yes"}, {"--scope", "global"}, {"project"}} {
		_, err := parseModelApplyScope(args)
		require.ErrorIs(t, err, errModelApplyUsage)
	}
}
func TestModelApplyRecommendedArguments(t *testing.T) {
	for _, tc := range []struct {
		args        []string
		scope       string
		recommended bool
	}{{nil, "", false}, {[]string{"--recommended"}, "", true}, {[]string{"--recommended", "--global"}, "global", true}, {[]string{"--project", "--recommended"}, "project", true}} {
		scope, recommended, err := parseModelApplyArgs(tc.args)
		require.NoError(t, err)
		require.Equal(t, tc.scope, scope)
		require.Equal(t, tc.recommended, recommended)
	}
	for _, args := range [][]string{{"--recommended", "--recommended"}, {"--recommended", "--global", "--project"}, {"--recommend"}} {
		_, _, err := parseModelApplyArgs(args)
		require.ErrorIs(t, err, errModelApplyUsage)
	}
}

func TestSaveRecommendedModels(t *testing.T) {
	if interactive() {
		t.Skip("non-interactive trust behaviour")
	}
	catalog := []string{"copilot/claude-sonnet-5.5", "copilot/claude-opus-5.5", "copilot/claude-haiku-5.5", "copilot/gpt-6-luna", "copilot/gpt-6.1-sol", "copilot/kimi-k3"}
	home, root := t.TempDir(), t.TempDir()
	require.NoError(t, config.WriteFile(home, config.Config{Model: "copilot/gpt-6.1-sol", Review: config.ReviewConfig{Model: "copilot/claude-opus-5.5"}, DefaultMode: "work", Routing: map[string]string{"editing": "copilot/gpt-6-luna"}}))
	path := filepath.Join(config.Dir(home), "config.yaml")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	cfg, err := readModelApplyConfig(home, root, "global")
	require.NoError(t, err)
	_, err = saveRecommendedModels(home, root, "global", cfg, catalog, func(string, string) bool { return false })
	require.ErrorIs(t, err, errModelApplyCancelled)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "declining must not save recommendations")
	var preview string
	next, err := saveRecommendedModels(home, root, "global", cfg, catalog, func(_ string, text string) bool { preview = text; return true })
	require.NoError(t, err)
	require.Contains(t, preview, "model: copilot/gpt-6.1-sol -> copilot/claude-opus-5.5")
	require.Equal(t, "copilot/claude-opus-5.5", next.Model)
	saved, err := config.ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, "copilot/claude-opus-5.5", saved.Model)
	require.Equal(t, "copilot/gpt-6.1-sol", saved.Review.Model)
	require.Equal(t, "work", saved.DefaultMode, "non-role settings are kept")
	require.Equal(t, "copilot/gpt-6-luna", saved.Routing["editing"], "helper routing is kept")
	_, err = saveRecommendedModels(home, root, "global", next, catalog, func(string, string) bool { t.Fatal("matching recommendations must not prompt"); return false })
	require.NoError(t, err)

	cfg, err = readModelApplyConfig(home, root, "project")
	require.NoError(t, err)
	project := config.Clone(cfg)
	project.Model = "copilot/gpt-6.1-sol"
	project.Review.Model = "copilot/claude-opus-5.5"
	require.NoError(t, config.WriteYAML(filepath.Join(root, ".codeforge"), config.Config{Model: project.Model, Review: config.ReviewConfig{Model: project.Review.Model}}))
	require.NoError(t, config.ApproveProject(home, root))
	cfg, err = readModelApplyConfig(home, root, "project")
	require.NoError(t, err)
	_, err = saveRecommendedModels(home, root, "project", cfg, catalog, func(string, string) bool { return true })
	require.NoError(t, err)
	effective, err := config.Load(home, root, nil)
	require.NoError(t, err)
	require.Equal(t, "copilot/claude-opus-5.5", effective.Model, "saved project choices are approved and used")
	globalAfter, err := config.ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, saved.Model, globalAfter.Model, "project application must not change global settings")
	local, err := config.ReadTrustedProject(home, root, nil)
	require.NoError(t, err)
	require.Empty(t, local.Routing, "global helper routing must not be copied into the project file")
}

func TestRecommendedModelsKeepProviderNames(t *testing.T) {
	cfg := config.Config{Providers: &config.ProviderPolicy{Primary: "devpass", Fallback: "copilot"}, Model: "GPT 6.1 Sol"}
	next, _, err := recommendedModelConfig(cfg, providerTestCatalog())
	require.NoError(t, err)
	require.Equal(t, "claude-opus-5-5", next.Model, "recommendations follow the primary provider and stay provider-neutral names")
	require.Equal(t, "GPT 6.1 Sol", cfg.Model, "input is not mutated")
}
func TestModelApplyLoadsOnlySelectedSavedScope(t *testing.T) {
	if interactive() {
		t.Skip("non-interactive trust behaviour")
	}
	home, root := t.TempDir(), t.TempDir()
	require.NoError(t, config.WriteFile(home, config.Config{Model: "global/coder"}))
	require.NoError(t, config.WriteYAML(filepath.Join(root, ".codeforge"), config.Config{Model: "project/coder"}))
	require.NoError(t, config.ApproveProject(home, root))
	t.Setenv("CODEFORGE_MODEL", "oneoff/coder")
	global, err := readModelApplyConfig(home, root, "global")
	require.NoError(t, err)
	require.Equal(t, "global/coder", global.Model)
	project, err := readModelApplyConfig(home, root, "project")
	require.NoError(t, err)
	require.Equal(t, "project/coder", project.Model)
	require.NoError(t, os.WriteFile(config.ProjectPath(root), []byte("invalid: broken\n"), privateFile))
	global, err = readModelApplyConfig(home, root, "global")
	require.NoError(t, err)
	require.Equal(t, "global/coder", global.Model, "global application must not load checkout configuration")
	project, err = readModelApplyConfig(home, root, "project")
	require.NoError(t, err)
	require.Equal(t, "global/coder", project.Model, "changed unapproved project choices are not applied")
}

func TestPlannedNativeFallbacksPreserveUnrelatedRows(t *testing.T) {
	plan := config.ModelPlan{Routes: []config.ModelRoute{{Role: "model", Primary: "primary/model", Fallbacks: []string{"backup/model"}}}}
	native := &nativeFallbackSnapshot{Global: map[string][]string{"primary/model": {"old/model"}, "backup/model": {"extra/model"}, "other/model": {"other/backup"}}, Effective: map[string][]string{"primary/model": {"old/model"}, "backup/model": {"extra/model"}, "other/model": {"other/backup"}}}
	global, err := plannedNativeFallbacks(plan, native, "global")
	require.NoError(t, err)
	require.Equal(t, []string{"backup/model"}, global["primary/model"])
	require.Equal(t, []string{}, global["backup/model"])
	require.Equal(t, []string{"other/backup"}, global["other/model"])
	require.Equal(t, []string{"old/model"}, native.Global["primary/model"], "planning must not mutate snapshots")
	project, err := plannedNativeFallbacks(plan, native, "project")
	require.NoError(t, err)
	require.Equal(t, global, project, "a new project map must retain effective global entries")
	native.Project = map[string][]string{"project/model": {"project/backup"}}
	native.Effective = native.Project
	project, err = plannedNativeFallbacks(plan, native, "project")
	require.NoError(t, err)
	require.Equal(t, []string{"project/backup"}, project["project/model"])
	require.NotContains(t, project, "other/model", "do not revive inactive global fallback rows")
	require.Equal(t, native.Project, native.declaredFallbacks(), "native project maps replace the whole global key")
	require.NoError(t, plan.VerifyFallbacks(project), "inactive global backup hops must not fail verification")
	preview := nativeFallbackPreview(native.Global, global)
	require.Contains(t, preview, `"primary/model": ["old/model"] -> ["backup/model"]`)
	require.Contains(t, preview, `"backup/model": ["extra/model"] -> []`)
	require.NotContains(t, preview, "other/model:")
}

func TestGlobalDefaultModelShadowedByProject(t *testing.T) {
	root := t.TempDir()
	plan := config.ModelPlan{Routes: []config.ModelRoute{{Role: "model", Primary: "a/coder"}}}
	require.Empty(t, globalDefaultModelShadowed(root, plan), "no project file")
	dir := filepath.Join(root, ".empryo")
	require.NoError(t, os.MkdirAll(dir, privateDir))
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"defaultModel":"a/coder"}`), privateFile))
	require.Empty(t, globalDefaultModelShadowed(root, plan), "project already matches")
	require.NoError(t, os.WriteFile(path, []byte(`{"defaultModel":"b/other"}`), privateFile))
	warning := globalDefaultModelShadowed(root, plan)
	require.Contains(t, warning, "b/other")
	require.Contains(t, warning, "--project")
}
