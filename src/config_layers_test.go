package src

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestConfigurationLayersAndEnvironment(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	global := config.Config{Model: "global/coder", Init: "global/writer", Review: config.ReviewConfig{Model: "global/reviewer"}, Routing: map[string]string{"research": "global/reader", "editing": "global/editor"}}
	require.NoError(t, config.WriteFile(home, global))
	require.NoError(t, config.WriteYAML(filepath.Join(project, ".codeforge"), config.Config{Model: "project/coder", Routing: map[string]string{"research": "project/reader", "editing": ""}}))
	require.NoError(t, config.ApproveProject(home, project))
	cfg, err := config.Load(home, project, nil)
	require.NoError(t, err)
	require.Equal(t, "project/coder", cfg.Model)
	require.Equal(t, "global/writer", cfg.Init)
	require.Equal(t, "global/reviewer", cfg.Review.Model)
	require.Equal(t, "project/reader", cfg.Routing["research"])
	require.NotContains(t, cfg.Routing, "editing")
	t.Setenv("CODEFORGE_MODEL", "environment/coder")
	t.Setenv("CODEFORGE_INIT", "environment/writer")
	cfg, err = config.Load(home, project, nil)
	require.NoError(t, err)
	require.Equal(t, "environment/coder", cfg.Model)
	require.Equal(t, "environment/writer", cfg.Init)
	args, err := taskArgs(cfg, []string{"--effort", "low", "--model", "cli/coder", "task"})
	require.NoError(t, err)
	require.Equal(t, "cli/coder", flagValue(args, "--model"))
	preserved, err := config.ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, "global/coder", preserved.Model)
	require.Equal(t, "global/editor", preserved.Routing["editing"])
}

func TestYAMLValidationDoesNotFallBackToLegacy(t *testing.T) {
	for _, body := range []string{"unknown: true\n", "model: [not, a, string]\n", "model: a/b\nmodel: c/d\n", "model: a/b\n---\nmodel: c/d\n"} {
		t.Run(body, func(t *testing.T) {
			home := t.TempDir()
			writeConfig(t, home, `{"model":"legacy/coder"}`)
			require.NoError(t, os.WriteFile(filepath.Join(config.Dir(home), "config.yaml"), []byte(body), privateFile))
			_, err := config.ReadFile(home)
			require.Error(t, err)
		})
	}
}

func TestLegacyJSONMigrationAndPrivateYAML(t *testing.T) {
	home := t.TempDir()
	legacy := `{"model":"old/coder","review":{"model":"old/reviewer"}}`
	writeConfig(t, home, legacy)
	cfg, err := config.ReadFile(home)
	require.NoError(t, err)
	cfg.Model = "new/coder"
	require.NoError(t, config.WriteFile(home, cfg))
	got, err := config.ReadFile(home)
	require.NoError(t, err)
	require.Equal(t, "new/coder", got.Model)
	data, err := os.ReadFile(filepath.Join(config.Dir(home), "config.json"))
	require.NoError(t, err)
	require.Equal(t, legacy, string(data))
	info, err := os.Stat(filepath.Join(config.Dir(home), "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(privateFile), info.Mode().Perm())
}

func TestModelConfigurationCancellationAndValidationKeepFile(t *testing.T) {
	dir := t.TempDir()
	before := config.Config{Model: "saved/coder", Review: config.ReviewConfig{Model: "saved/reviewer"}}
	require.NoError(t, config.WriteYAML(dir, before))
	err := configureModelFile(dir, before, []string{"new/coder"}, func(cfg *config.Config, _ []string) error { cfg.Model = "new/coder"; return context.Canceled })
	require.ErrorIs(t, err, context.Canceled)
	err = configureModelFile(dir, before, nil, func(cfg *config.Config, _ []string) error { cfg.Review.Model = cfg.Model; return nil })
	require.ErrorIs(t, err, config.ErrReviewerIsCoder)
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	var got config.Config
	require.NoError(t, yaml.Unmarshal(data, &got))
	require.Equal(t, before, got)
}

func TestProjectProfilesIsolateModelAndRoutingOverrides(t *testing.T) {
	global, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	native := filepath.Join(global, "config.json")
	require.NoError(t, os.WriteFile(native, []byte(`{"defaultModel":"keep/native"}`), privateFile))
	base := config.Config{Model: "global/coder", Review: config.ReviewConfig{Model: "global/reviewer"}}
	require.NoError(t, syncAssets(global, base))
	for _, tc := range []struct{ root, model string }{{a, "a/coder"}, {b, "b/coder"}} {
		cfg := base
		cfg.Model = tc.model
		cfg.Routing = map[string]string{"research": tc.model}
		require.NoError(t, syncProjectAgents(tc.root, cfg))
		body, err := os.ReadFile(filepath.Join(tc.root, ".empryo", "agents", "codeforge-work.md"))
		require.NoError(t, err)
		header := strings.SplitN(string(body), "---", 3)
		var profile struct {
			Model   string
			Routing map[string]string
		}
		require.NoError(t, yaml.Unmarshal([]byte(header[1]), &profile))
		require.Equal(t, tc.model, profile.Model)
		require.Equal(t, tc.model, profile.Routing["research"])
	}
	body, err := os.ReadFile(filepath.Join(global, "agents", "codeforge-work.md"))
	require.NoError(t, err)
	require.Contains(t, string(body), "model: global/coder")
	body, err = os.ReadFile(native)
	require.NoError(t, err)
	require.Equal(t, `{"defaultModel":"keep/native"}`, string(body))
}

func TestSmallModelCatalogKeepsClearlyMarkedDefaults(t *testing.T) {
	for _, model := range []string{"only/custom-model", config.DefaultModel, config.DefaultReviewModel, config.DefaultSecondOpinion[0]} {
		var cfg config.Config
		seedModelChoices(&cfg, []string{model})
		require.NoError(t, config.Validate(cfg))
		require.NotEqual(t, cfg.Model, cfg.Review.Model)
	}
	options := modelOptions([]string{"available/model"}, "saved/model")
	require.Contains(t, options[len(options)-1].Label, "not currently listed")
}

func TestDuplicateJurorsAreRejected(t *testing.T) {
	require.ErrorIs(t, config.Validate(config.Config{Review: config.ReviewConfig{SecondOpinion: []string{"provider/reviewer", "provider/reviewer"}}}), config.ErrDuplicateModels)
}

func TestUnsupportedBackgroundRoutingIsExplicit(t *testing.T) {
	require.ErrorIs(t, config.Validate(config.Config{Model: "provider/model\nforgeMode: auto"}), config.ErrNotProviderModel)
	scope, err := modelConfigurationScope([]string{"--project"})
	require.NoError(t, err)
	require.Equal(t, "project", scope)
	_, err = modelConfigurationScope([]string{"--project", "--global"})
	require.ErrorIs(t, err, errModelConfigure)
	if !interactive() {
		require.ErrorIs(t, runModelConfigure(t.Context(), t.TempDir(), "/missing", nil), errModelConfigureTerminal)
	}
}
