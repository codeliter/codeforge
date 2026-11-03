package src

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestConfigurationLayersAndEnvironment(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	global := Config{Model: "global/coder", Init: "global/writer", Review: ReviewConfig{Model: "global/reviewer"}, Routing: map[string]string{"research": "global/reader", "editing": "global/editor"}}
	require.NoError(t, writeConfigFile(home, global))
	require.NoError(t, writeConfigYAML(filepath.Join(project, ".codeforge"), Config{Model: "project/coder", Routing: map[string]string{"research": "project/reader", "editing": ""}}))
	require.NoError(t, approveProjectConfig(home, project))
	cfg, err := loadConfigAt(home, project)
	require.NoError(t, err)
	require.Equal(t, "project/coder", cfg.Model)
	require.Equal(t, "global/writer", cfg.Init)
	require.Equal(t, "global/reviewer", cfg.Review.Model)
	require.Equal(t, "project/reader", cfg.Routing["research"])
	require.NotContains(t, cfg.Routing, "editing")
	t.Setenv("CODEFORGE_MODEL", "environment/coder")
	t.Setenv("CODEFORGE_INIT", "environment/writer")
	cfg, err = loadConfigAt(home, project)
	require.NoError(t, err)
	require.Equal(t, "environment/coder", cfg.Model)
	require.Equal(t, "environment/writer", cfg.Init)
	args, err := taskArgs(t.Context(), "/missing", cfg, []string{"--effort", "low", "--model", "cli/coder", "task"})
	require.NoError(t, err)
	require.Equal(t, "cli/coder", flagValue(args, "--model"))
	preserved, err := readConfigFile(home)
	require.NoError(t, err)
	require.Equal(t, "global/coder", preserved.Model)
	require.Equal(t, "global/editor", preserved.Routing["editing"])
}

func TestYAMLValidationDoesNotFallBackToLegacy(t *testing.T) {
	for _, body := range []string{"unknown: true\n", "model: [not, a, string]\n", "model: a/b\nmodel: c/d\n", "model: a/b\n---\nmodel: c/d\n"} {
		t.Run(body, func(t *testing.T) {
			home := t.TempDir()
			writeConfig(t, home, `{"model":"legacy/coder"}`)
			require.NoError(t, os.WriteFile(filepath.Join(configDir(home), "config.yaml"), []byte(body), privateFile))
			_, err := readConfigFile(home)
			require.Error(t, err)
		})
	}
}

func TestLegacyJSONMigrationAndPrivateYAML(t *testing.T) {
	home := t.TempDir()
	legacy := `{"model":"old/coder","review":{"model":"old/reviewer"}}`
	writeConfig(t, home, legacy)
	cfg, err := readConfigFile(home)
	require.NoError(t, err)
	cfg.Model = "new/coder"
	require.NoError(t, writeConfigFile(home, cfg))
	got, err := readConfigFile(home)
	require.NoError(t, err)
	require.Equal(t, "new/coder", got.Model)
	data, err := os.ReadFile(filepath.Join(configDir(home), "config.json"))
	require.NoError(t, err)
	require.Equal(t, legacy, string(data))
	info, err := os.Stat(filepath.Join(configDir(home), "config.yaml"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(privateFile), info.Mode().Perm())
}

func TestModelConfigurationCancellationAndValidationKeepFile(t *testing.T) {
	dir := t.TempDir()
	before := Config{Model: "saved/coder", Review: ReviewConfig{Model: "saved/reviewer"}}
	require.NoError(t, writeConfigYAML(dir, before))
	err := configureModelFile(dir, before, []string{"new/coder"}, func(cfg *Config, _ []string) error { cfg.Model = "new/coder"; return context.Canceled })
	require.ErrorIs(t, err, context.Canceled)
	err = configureModelFile(dir, before, nil, func(cfg *Config, _ []string) error { cfg.Review.Model = cfg.Model; return nil })
	require.ErrorIs(t, err, errReviewerIsCoder)
	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	var got Config
	require.NoError(t, yaml.Unmarshal(data, &got))
	require.Equal(t, before, got)
}

func TestProjectProfilesIsolateModelAndRoutingOverrides(t *testing.T) {
	global, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	native := filepath.Join(global, "config.json")
	require.NoError(t, os.WriteFile(native, []byte(`{"defaultModel":"keep/native"}`), privateFile))
	base := Config{Model: "global/coder", Review: ReviewConfig{Model: "global/reviewer"}}
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
	for _, model := range []string{"only/custom-model", defaultModel, defaultReviewModel, defaultSecondOpinion[0]} {
		var cfg Config
		seedModelChoices(&cfg, []string{model})
		require.NoError(t, validate(cfg))
		require.NotEqual(t, cfg.Model, cfg.Review.Model)
	}
	options := modelOptions([]string{"available/model"}, "saved/model")
	require.Contains(t, options[len(options)-1].Key, "not currently listed")
}

func TestDuplicateJurorsAreRejected(t *testing.T) {
	require.ErrorIs(t, validate(Config{Review: ReviewConfig{SecondOpinion: []string{"provider/reviewer", "provider/reviewer"}}}), errDuplicateModels)
}

func TestUnsupportedBackgroundRoutingIsExplicit(t *testing.T) {
	require.ErrorIs(t, validateRouting(map[string]string{"namer": "provider/model"}), errRoutingJob)
	require.ErrorIs(t, validate(Config{Model: "provider/model\nforgeMode: auto"}), errNotProviderModel)
	scope, err := modelConfigurationScope([]string{"--project"})
	require.NoError(t, err)
	require.Equal(t, "project", scope)
	_, err = modelConfigurationScope([]string{"--project", "--global"})
	require.ErrorIs(t, err, errModelConfigure)
	if !interactive() {
		require.ErrorIs(t, runModelConfigure(t.Context(), t.TempDir(), "/missing", nil), errModelConfigureTerminal)
	}
}
