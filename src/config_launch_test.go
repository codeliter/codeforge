package src

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestExampleModelConfigurationIsValid(t *testing.T) {
	data, err := os.ReadFile("../config.example.yaml")
	require.NoError(t, err)
	var cfg Config
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	require.NoError(t, validate(cfg))
}

func TestCLIUsesProjectModelWithoutRewritingNativeSettings(t *testing.T) {
	home, wd, bins := t.TempDir(), t.TempDir(), t.TempDir()
	out, err := exec.CommandContext(t.Context(), "git", "init", "-q", wd).CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, writeConfigFile(home, Config{Model: "global/coder", Review: ReviewConfig{Model: "global/reviewer"}}))
	require.NoError(t, writeConfigYAML(filepath.Join(wd, ".codeforge"), Config{Model: "project/coder", Routing: map[string]string{"research": "project/reader"}}))
	require.NoError(t, approveProjectConfig(home, wd))
	empryoDir := filepath.Join(home, ".empryo")
	require.NoError(t, os.MkdirAll(empryoDir, privateDir))
	nativePath := filepath.Join(empryoDir, "config.json")
	native := `{"defaultModel":"native/model","taskRouter":{"spark":"native/helper"}}`
	require.NoError(t, os.WriteFile(nativePath, []byte(native), privateFile))
	require.NoError(t, os.WriteFile(filepath.Join(bins, "empryo"), []byte(modeCLIEngine), execPerm))
	raw, err := json.Marshal([]string{"work", "fixture task", "--effort", "low", "--session-id", "layer-session"})
	require.NoError(t, err)
	capture := filepath.Join(home, "launches")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestModesCLI$")
	cmd.Dir = wd
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bins+string(os.PathListSeparator)+os.Getenv("PATH"), "CODEFORGE_CLI_ARGS="+string(raw), "CODEFORGE_CLI_CAPTURE="+capture, "CODEFORGE_CLI_FAIL=0", "CODEFORGE_MODEL=", "CODEFORGE_REVIEW_MODEL=", "CODEFORGE_REVIEW_SECOND_OPINION=", "CODEFORGE_JUDGES=", "CODEFORGE_INIT=")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	data, err := os.ReadFile(capture)
	require.NoError(t, err)
	require.Contains(t, string(data), "--model\nproject/coder\n")
	data, err = os.ReadFile(nativePath)
	require.NoError(t, err)
	require.Equal(t, native, string(data))
	data, err = os.ReadFile(filepath.Join(wd, ".empryo", "agents", "codeforge-work.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "research: project/reader")
}
