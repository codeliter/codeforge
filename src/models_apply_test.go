package src

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelsApplyRequiresConfirmationAndPreservesSavedChoices(t *testing.T) {
	if interactive() {
		t.Skip("non-interactive apply")
	}
	home := t.TempDir()
	require.NoError(t, writeConfigFile(home, Config{Model: "saved/coder", Review: ReviewConfig{Model: "saved/reviewer"}}))
	path := filepath.Join(configDir(home), "config.yaml")
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
func TestModelApplyLoadsOnlySelectedSavedScope(t *testing.T) {
	if interactive() {
		t.Skip("non-interactive trust behaviour")
	}
	home, root := t.TempDir(), t.TempDir()
	require.NoError(t, writeConfigFile(home, Config{Model: "global/coder"}))
	require.NoError(t, writeConfigYAML(filepath.Join(root, ".codeforge"), Config{Model: "project/coder"}))
	require.NoError(t, approveProjectConfig(home, root))
	t.Setenv("CODEFORGE_MODEL", "oneoff/coder")
	global, err := readModelApplyConfig(home, root, "global")
	require.NoError(t, err)
	require.Equal(t, "global/coder", global.Model)
	project, err := readModelApplyConfig(home, root, "project")
	require.NoError(t, err)
	require.Equal(t, "project/coder", project.Model)
	require.NoError(t, os.WriteFile(projectConfigPath(root), []byte("invalid: broken\n"), privateFile))
	global, err = readModelApplyConfig(home, root, "global")
	require.NoError(t, err)
	require.Equal(t, "global/coder", global.Model, "global application must not load checkout configuration")
	project, err = readModelApplyConfig(home, root, "project")
	require.NoError(t, err)
	require.Equal(t, "global/coder", project.Model, "changed unapproved project choices are not applied")
}

func TestPlannedNativeFallbacksPreserveUnrelatedRows(t *testing.T) {
	plan := ModelPlan{Routes: []ModelRoute{{Role: "model", Primary: "primary/model", Fallback: "backup/model"}}}
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
	require.NoError(t, plan.verifyFallbacks(project), "inactive global backup hops must not fail verification")
	preview := nativeFallbackPreview(native.Global, global)
	require.Contains(t, preview, `"primary/model": ["old/model"] -> ["backup/model"]`)
	require.Contains(t, preview, `"backup/model": ["extra/model"] -> []`)
	require.NotContains(t, preview, "other/model:")
}
