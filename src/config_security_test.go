package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestUntrustedProjectCannotChooseItsOwnReviewer(t *testing.T) {
	if interactive() {
		t.Skip("tests non-interactive checkout handling")
	}
	home, root := t.TempDir(), t.TempDir()
	global := Config{Model: "global/coder", Review: ReviewConfig{Model: "global/reviewer"}}
	require.NoError(t, writeConfigFile(home, global))
	dir := filepath.Join(root, ".codeforge")
	require.NoError(t, writeConfigYAML(dir, Config{Review: ReviewConfig{Model: "checkout/weak-reviewer"}}))
	cfg, err := loadConfigAt(home, root)
	require.NoError(t, err)
	require.Equal(t, "global/reviewer", cfg.Review.Model)
	require.NoError(t, approveProjectConfig(home, root))
	cfg, err = loadConfigAt(home, root)
	require.NoError(t, err)
	require.Equal(t, "checkout/weak-reviewer", cfg.Review.Model)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("unknown: true\n"), privateFile))
	cfg, err = loadConfigAt(home, root)
	require.NoError(t, err, "changed malformed checkout config must be skipped before parsing")
	require.Equal(t, "global/reviewer", cfg.Review.Model)
}

func TestProjectApprovalUsesSavedBytesNotASecondRead(t *testing.T) {
	if interactive() {
		t.Skip("tests non-interactive approval handling")
	}
	home, root := t.TempDir(), t.TempDir()
	require.NoError(t, writeConfigFile(home, Config{Model: "global/coder", Review: ReviewConfig{Model: "global/reviewer"}}))
	dir := filepath.Join(root, ".codeforge")
	chosen := Config{Model: "chosen/coder", Review: ReviewConfig{Model: "chosen/reviewer"}}
	err := configureModelFile(dir, chosen, nil, func(_ *Config, _ []string) error { return nil }, func(path, approved string) error {
		require.NoError(t, os.WriteFile(path, []byte("model: substituted/coder\n"), privateFile))
		return trust(home, projectConfigPath(root), approved)
	})
	require.NoError(t, err)
	cfg, err := loadConfigAt(home, root)
	require.NoError(t, err)
	require.Equal(t, "global/coder", cfg.Model, "substituted bytes must not inherit the user's approval")
}

func TestModelRoutingInheritIsNotAnImplicitReset(t *testing.T) {
	cfg := Config{Routing: map[string]string{"research": "project/reader"}}
	require.Equal(t, inheritRouting, routingPickerValue(cfg.Routing, "editing"))
	applyRoutingChoices(&cfg, []string{"editing"}, []string{inheritRouting})
	merged := mergeConfig(Config{Routing: map[string]string{"editing": "global/editor"}}, cfg)
	require.Equal(t, "global/editor", merged.Routing["editing"])
	applyRoutingChoices(&cfg, []string{"editing"}, []string{""})
	merged = mergeConfig(Config{Routing: map[string]string{"editing": "global/editor"}}, cfg)
	require.NotContains(t, merged.Routing, "editing")
}

func TestModelTrustPreviewCannotHideReviewersBehindComments(t *testing.T) {
	home := t.TempDir()
	body := strings.Repeat("# harmless padding\n", 200) + "review:\n  model: selected/reviewer\n"
	shown, err := modelConfigTrustPreview(home, body)
	require.NoError(t, err)
	require.Contains(t, shown, "selected/reviewer")
	require.NotContains(t, shown, "harmless padding")
}

func TestDeclinedProjectConfigIsNotSilentlyReplaced(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	dir := filepath.Join(root, ".codeforge")
	require.NoError(t, writeConfigYAML(dir, Config{Model: "project/coder"}))
	err := ensureProjectModelEditing(home, root, func() bool { return false })
	require.ErrorIs(t, err, errUnapprovedModelEdit)
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "project/coder")
	require.NoError(t, ensureProjectModelEditing(home, root, func() bool { return true }))
}

func TestHomeCannotBeMistakenForASeparateProjectConfig(t *testing.T) {
	home := t.TempDir()
	_, err := modelConfigDirectory(home, home, "project")
	require.ErrorIs(t, err, errModelConfigure)
	alias := filepath.Join(t.TempDir(), "home-link")
	require.NoError(t, os.Symlink(home, alias))
	_, err = modelConfigDirectory(home, alias, "project")
	require.ErrorIs(t, err, errModelConfigure)
}

func TestProjectWizardOnlyPinsChangedRoles(t *testing.T) {
	baseline := configDefaults(Config{Model: "global/coder", Review: ReviewConfig{Model: "global/reviewer"}})
	edited := cloneConfig(baseline)
	seedModelChoices(&edited, []string{"global/coder", "global/reviewer"})
	edited.Routing = nil
	delta := projectModelDelta(Config{}, baseline, edited)
	require.Empty(t, delta.Model)
	require.Empty(t, delta.Review.Model)
	require.Empty(t, delta.Init, "opening the form must not create an unrequested init pin")
	edited.Expert = "project/expert"
	delta = projectModelDelta(Config{}, baseline, edited)
	require.Empty(t, delta.Model)
	require.Equal(t, "project/expert", delta.Expert)
}

func TestProfileModelScalarsAreQuotedSafely(t *testing.T) {
	body, err := renderAgent([]byte("---\nname: test\nmodel: {{coder}}\n---\n"), Config{Model: "provider/model:"})
	require.NoError(t, err)
	var header struct {
		Name  string
		Model string
	}
	require.NoError(t, yaml.Unmarshal(body[4:len(body)-4], &header))
	require.Equal(t, "provider/model:", header.Model)
}

func TestGlobalProfilesDoNotCaptureOneOffEnvironment(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, writeConfigFile(home, Config{Model: "global/coder", Review: ReviewConfig{Model: "global/reviewer"}}))
	t.Setenv("CODEFORGE_MODEL", "once/coder")
	base, err := loadBaseConfig(home)
	require.NoError(t, err)
	require.Equal(t, "global/coder", base.Model)
	active, err := loadConfig(home)
	require.NoError(t, err)
	require.Equal(t, "once/coder", active.Model)
}

func TestConfigAndWorkspaceSymlinksAreNotOverwritten(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(other, filepath.Join(root, ".empryo")))
	require.ErrorIs(t, syncProjectAgents(root, Config{}), errProfileRootSymlink)
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "actual.yaml")
	require.NoError(t, os.WriteFile(target, []byte("keep this"), privateFile))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "config.yaml")))
	require.ErrorIs(t, writeConfigYAML(dir, Config{Model: "provider/model"}), errConfigSymlink)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "keep this", string(data))
}
