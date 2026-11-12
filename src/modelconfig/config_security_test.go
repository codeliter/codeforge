package modelconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/fsutil"
)

func TestUntrustedProjectCannotChooseItsOwnReviewer(t *testing.T) {
	if forms.Interactive() {
		t.Skip("tests non-interactive checkout handling")
	}
	home, root := t.TempDir(), t.TempDir()
	global := config.Config{Model: "global/coder", Review: config.ReviewConfig{Model: "global/reviewer"}}
	require.NoError(t, config.WriteFile(home, global))
	dir := filepath.Join(root, ".codeforge")
	require.NoError(t, config.WriteYAML(dir, config.Config{Review: config.ReviewConfig{Model: "checkout/weak-reviewer"}}))
	cfg, err := config.Load(home, root, nil)
	require.NoError(t, err)
	require.Equal(t, "global/reviewer", cfg.Review.Model)
	require.NoError(t, config.ApproveProject(home, root))
	cfg, err = config.Load(home, root, nil)
	require.NoError(t, err)
	require.Equal(t, "checkout/weak-reviewer", cfg.Review.Model)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("unknown: true\n"), fsutil.PrivateFile))
	cfg, err = config.Load(home, root, nil)
	require.NoError(t, err, "changed malformed checkout config must be skipped before parsing")
	require.Equal(t, "global/reviewer", cfg.Review.Model)
}

func TestProjectApprovalUsesSavedBytesNotASecondRead(t *testing.T) {
	if forms.Interactive() {
		t.Skip("tests non-interactive approval handling")
	}
	home, root := t.TempDir(), t.TempDir()
	require.NoError(t, config.WriteFile(home, config.Config{Model: "global/coder", Review: config.ReviewConfig{Model: "global/reviewer"}}))
	dir := filepath.Join(root, ".codeforge")
	chosen := config.Config{Model: "chosen/coder", Review: config.ReviewConfig{Model: "chosen/reviewer"}}
	err := configureModelFile(dir, chosen, nil, func(_ *config.Config, _ []string) error { return nil }, func(path, approved string) error {
		require.NoError(t, os.WriteFile(path, []byte("model: substituted/coder\n"), fsutil.PrivateFile))
		return config.Trust(home, config.ProjectPath(root), approved)
	})
	require.NoError(t, err)
	cfg, err := config.Load(home, root, nil)
	require.NoError(t, err)
	require.Equal(t, "global/coder", cfg.Model, "substituted bytes must not inherit the user's approval")
}

func TestModelRoutingInheritIsNotAnImplicitReset(t *testing.T) {
	cfg := config.Config{Routing: map[string]string{"research": "project/reader"}}
	require.Equal(t, inheritRouting, routingPickerValue(cfg.Routing, "editing"))
	applyRoutingChoices(&cfg, []string{"editing"}, []string{inheritRouting})
	merged := config.Merge(config.Config{Routing: map[string]string{"editing": "global/editor"}}, cfg)
	require.Equal(t, "global/editor", merged.Routing["editing"])
	applyRoutingChoices(&cfg, []string{"editing"}, []string{""})
	merged = config.Merge(config.Config{Routing: map[string]string{"editing": "global/editor"}}, cfg)
	require.NotContains(t, merged.Routing, "editing")
}

func TestModelTrustPreviewCannotHideReviewersBehindComments(t *testing.T) {
	home := t.TempDir()
	body := strings.Repeat("# harmless padding\n", 200) + "review:\n  model: selected/reviewer\n"
	shown, err := config.ModelPreview(home, body)
	require.NoError(t, err)
	require.Contains(t, shown, "selected/reviewer")
	require.NotContains(t, shown, "harmless padding")
}

func TestDeclinedProjectConfigIsNotSilentlyReplaced(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	dir := filepath.Join(root, ".codeforge")
	require.NoError(t, config.WriteYAML(dir, config.Config{Model: "project/coder"}))
	err := config.EnsureProjectEditing(home, root, func() bool { return false })
	require.ErrorIs(t, err, config.ErrUnapprovedEdit)
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "project/coder")
	require.NoError(t, config.EnsureProjectEditing(home, root, func() bool { return true }))
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
	baseline := config.Defaults(config.Config{Model: "global/coder", Review: config.ReviewConfig{Model: "global/reviewer"}})
	edited := config.Clone(baseline)
	seedModelChoices(&edited, []string{"global/coder", "global/reviewer"})
	edited.Routing = nil
	delta := projectModelDelta(config.Config{}, baseline, edited)
	require.Empty(t, delta.Model)
	require.Empty(t, delta.Review.Model)
	require.Empty(t, delta.Init, "opening the form must not create an unrequested init pin")
	edited.Expert = "project/expert"
	delta = projectModelDelta(config.Config{}, baseline, edited)
	require.Empty(t, delta.Model)
	require.Equal(t, "project/expert", delta.Expert)
}

func TestGlobalProfilesDoNotCaptureOneOffEnvironment(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, config.WriteFile(home, config.Config{Model: "global/coder", Review: config.ReviewConfig{Model: "global/reviewer"}}))
	t.Setenv("CODEFORGE_MODEL", "once/coder")
	base, err := config.LoadBase(home)
	require.NoError(t, err)
	require.Equal(t, "global/coder", base.Model)
	active, err := config.LoadGlobal(home)
	require.NoError(t, err)
	require.Equal(t, "once/coder", active.Model)
}
