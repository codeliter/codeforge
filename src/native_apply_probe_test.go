package src

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func nativeApplyFixture(t *testing.T, projectOverride bool) (bin, home, root string, plan ModelPlan, calls *atomic.Int32) {
	t.Helper()
	if os.Getenv("CODEFORGE_NATIVE_PRESET_PROBE") != "1" {
		t.Skip("optional isolated installed-runtime integration")
	}
	bin, err := exec.LookPath("empryo")
	require.NoError(t, err)
	home, root = t.TempDir(), t.TempDir()
	calls = &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "no model turns expected", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	t.Setenv("HOME", home)
	t.Setenv("EMPRYO_PRESETS", "")
	t.Setenv("EMPRYO_NO_PROMPT", "1")
	t.Setenv("EMPRYO_TELEMETRY", "0")
	global := map[string]any{"liveTranslations": false, "liveSkills": false, "defaultModel": "cf-config-probe/unit", "providers": []any{map[string]any{"id": "cf-config-probe", "baseURL": server.URL + "/v1", "models": []string{"unit"}, "modelsAPI": false}}, "modelFallback": map[string][]string{"cf-config-probe/unit": {"legacy/backup"}, "cf-backup-probe/unit": {"undeclared/hop"}, "unrelated/model": {"unrelated/backup"}}}
	project := map[string]any{"tabNaming": false}
	if projectOverride {
		project["modelFallback"] = map[string][]string{"project/model": {"project/backup"}}
	}
	for _, layer := range []struct {
		root string
		data map[string]any
	}{{home, global}, {root, project}} {
		dir := filepath.Join(layer.root, ".empryo")
		require.NoError(t, os.MkdirAll(dir, privateDir))
		data, err := json.Marshal(layer.data)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), data, privateFile))
	}
	plan = ModelPlan{Routes: []ModelRoute{{Role: "model", Primary: "cf-config-probe/unit", Fallback: "cf-backup-probe/unit"}}}
	return bin, home, root, plan, calls
}

func nativeFixtureBytes(t *testing.T, root string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".empryo", "config.json"))
	require.NoError(t, err)
	return data
}

func TestNativeModelApplyProject(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "inherits global"
		if existing {
			name = "existing project override"
		}
		t.Run(name, func(t *testing.T) {
			bin, home, root, plan, calls := nativeApplyFixture(t, existing)
			globalBefore := nativeFixtureBytes(t, home)
			approvals := 0
			require.NoError(t, applyModelPlan(t.Context(), bin, root, "project", plan, func(preview string) bool {
				approvals++
				require.Contains(t, preview, "cf-config-probe/unit")
				require.Contains(t, preview, "cf-backup-probe/unit")
				return true
			}))
			require.Equal(t, 1, approvals)
			require.Equal(t, globalBefore, nativeFixtureBytes(t, home), "project apply must not modify global settings")
			native, err := readNativeFallbacks(t.Context(), bin, root)
			require.NoError(t, err)
			require.Equal(t, []string{"cf-backup-probe/unit"}, native.Project["cf-config-probe/unit"])
			if existing {
				require.Equal(t, []string{"project/backup"}, native.Project["project/model"])
			} else {
				require.Equal(t, []string{"unrelated/backup"}, native.Project["unrelated/model"])
			}
			var project map[string]any
			require.NoError(t, json.Unmarshal(nativeFixtureBytes(t, root), &project))
			require.Equal(t, false, project["tabNaming"])
			projectBefore := nativeFixtureBytes(t, root)
			require.NoError(t, applyModelPlan(t.Context(), bin, root, "project", plan, func(string) bool { t.Fatal("matching settings must not prompt for a rewrite"); return false }))
			require.Equal(t, projectBefore, nativeFixtureBytes(t, root))
			require.Zero(t, calls.Load())
		})
	}
}

func TestNativeModelApplyGlobalKeepsRealProjectOverrides(t *testing.T) {
	bin, _, root, plan, calls := nativeApplyFixture(t, true)
	projectBefore := nativeFixtureBytes(t, root)
	neutral := t.TempDir()
	require.NoError(t, applyModelPlan(t.Context(), bin, neutral, "global", plan, func(string) bool { return true }))
	require.Equal(t, projectBefore, nativeFixtureBytes(t, root), "global application must not clear the real project's fallback key")
	native, err := readNativeFallbacks(t.Context(), bin, neutral)
	require.NoError(t, err)
	require.Equal(t, []string{"cf-backup-probe/unit"}, native.Global["cf-config-probe/unit"])
	require.Equal(t, []string{"unrelated/backup"}, native.Global["unrelated/model"])
	require.Empty(t, native.Global["cf-backup-probe/unit"], "undeclared backup hops are explicitly cleared")
	require.Zero(t, calls.Load())
}

func TestNativeModelApplyCancellationAndStalePreview(t *testing.T) {
	bin, home, root, plan, calls := nativeApplyFixture(t, true)
	globalBefore, projectBefore := nativeFixtureBytes(t, home), nativeFixtureBytes(t, root)
	require.ErrorIs(t, applyModelPlan(t.Context(), bin, root, "project", plan, func(string) bool { return false }), errModelApplyCancelled)
	require.Equal(t, globalBefore, nativeFixtureBytes(t, home))
	require.Equal(t, projectBefore, nativeFixtureBytes(t, root))
	var external []byte
	err := applyModelPlan(t.Context(), bin, root, "project", plan, func(string) bool {
		var config map[string]any
		require.NoError(t, json.Unmarshal(projectBefore, &config))
		config["modelFallback"] = map[string][]string{"another/model": {"another/backup"}}
		var err error
		external, err = json.Marshal(config)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".empryo", "config.json"), external, privateFile))
		return true
	})
	require.ErrorIs(t, err, errNativeConfigChanged)
	require.Equal(t, external, nativeFixtureBytes(t, root), "never overwrite a change made after the preview")
	require.Equal(t, globalBefore, nativeFixtureBytes(t, home))
	require.Zero(t, calls.Load())
}
