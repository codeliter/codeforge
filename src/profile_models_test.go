package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/codeliter/codeforge/assets"
	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/fsutil"
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
	require.Contains(t, cfg.Routing, "editing", "the reset is kept, so the job is not routed for it")
	require.Empty(t, cfg.Routing["editing"])
	require.NotContains(t, config.EffectiveRouting(cfg), "editing")
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

func TestProjectProfilesIsolateModelAndRoutingOverrides(t *testing.T) {
	global, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	native := filepath.Join(global, "config.json")
	require.NoError(t, os.WriteFile(native, []byte(`{"defaultModel":"keep/native"}`), fsutil.PrivateFile))
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

func TestProfileModelScalarsAreQuotedSafely(t *testing.T) {
	body, err := renderAgent([]byte("---\nname: test\nmodel: {{coder}}\n---\n"), config.Config{Model: "provider/model:"})
	require.NoError(t, err)
	var header struct {
		Name  string
		Model string
	}
	require.NoError(t, yaml.Unmarshal(body[4:len(body)-4], &header))
	require.Equal(t, "provider/model:", header.Model)
}

func TestConfigAndWorkspaceSymlinksAreNotOverwritten(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(other, filepath.Join(root, ".empryo")))
	require.ErrorIs(t, syncProjectAgents(root, config.Config{}), errProfileRootSymlink)
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "actual.yaml")
	require.NoError(t, os.WriteFile(target, []byte("keep this"), fsutil.PrivateFile))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "config.yaml")))
	require.ErrorIs(t, config.WriteYAML(dir, config.Config{Model: "provider/model"}), config.ErrConfigSymlink)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "keep this", string(data))
}

func TestOpusDefaultWithOnDemandFableExpert(t *testing.T) {
	cfg, err := config.LoadGlobal(t.TempDir())
	require.NoError(t, err)
	require.Equal(t, "copilot/claude-opus-5.5", cfg.Model)
	require.Equal(t, "copilot/claude-fable-5.2", cfg.Expert)
	require.Equal(t, "copilot/claude-sonnet-5.5", cfg.Init, "the persona is drafted on Sonnet")
	body, err := assets.Profile.ReadFile("agents/codeforge-expert.md")
	require.NoError(t, err)
	rendered, err := renderAgent(body, cfg)
	require.NoError(t, err)
	require.Contains(t, string(rendered), "model: copilot/claude-fable-5.2")
	require.Contains(t, string(rendered), "specific difficult task")
}
