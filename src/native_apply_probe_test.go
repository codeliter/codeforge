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

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

func nativeApplyFixture(t *testing.T, projectOverride bool) (bin, home, root string, plan config.ModelPlan, calls *atomic.Int32) {
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
	plan = config.ModelPlan{Routes: []config.ModelRoute{{Role: "model", Primary: "cf-config-probe/unit", Fallbacks: []string{"cf-backup-probe/unit"}}}}
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

func nativeDefaultModel(t *testing.T, bin, workspace, scope string) string {
	t.Helper()
	var model string
	require.NoError(t, withNativeConfig(t.Context(), bin, workspace, func(request nativeConfigRequest, session string) error {
		var err error
		model, err = readNativeDefaultModel(request, session, scope)
		return err
	}))
	return model
}

func TestNativeDefaultModelApply(t *testing.T) {
	for _, scope := range []string{"project", "global"} {
		t.Run(scope, func(t *testing.T) {
			bin, home, root, plan, calls := nativeApplyFixture(t, false)
			workspace := root
			if scope == "global" {
				workspace = t.TempDir()
			}
			plan.Routes[0].Fallbacks = nil
			plan.Routes[0].Primary = "cf-backup-probe/unit"
			globalBefore, projectBefore := nativeFixtureBytes(t, home), nativeFixtureBytes(t, root)
			require.ErrorIs(t, applyModelPlan(t.Context(), bin, workspace, scope, plan, func(preview string) bool {
				require.Contains(t, preview, "defaultModel")
				require.Contains(t, preview, "cf-backup-probe/unit")
				return false
			}), errModelApplyCancelled)
			require.Equal(t, globalBefore, nativeFixtureBytes(t, home), "cancelling must not write")
			require.Equal(t, projectBefore, nativeFixtureBytes(t, root), "cancelling must not write")
			approvals := 0
			require.NoError(t, applyModelPlan(t.Context(), bin, workspace, scope, plan, func(string) bool { approvals++; return true }))
			require.Equal(t, 1, approvals)
			require.Equal(t, "cf-backup-probe/unit", nativeDefaultModel(t, bin, workspace, scope))
			if scope == "project" {
				require.Equal(t, globalBefore, nativeFixtureBytes(t, home), "project apply must not modify global settings")
			} else {
				require.Equal(t, projectBefore, nativeFixtureBytes(t, root), "global apply must not modify the project")
			}
			require.NoError(t, applyModelPlan(t.Context(), bin, workspace, scope, plan, func(string) bool { t.Fatal("matching settings must not prompt"); return false }))
			require.Zero(t, calls.Load())
		})
	}
}

func TestNativeModelApplyAsksOnceForFallbackAndDefaultModel(t *testing.T) {
	bin, _, root, plan, calls := nativeApplyFixture(t, false)
	plan.Routes[0].Primary, plan.Routes[0].Fallbacks = "cf-backup-probe/unit", []string{"cf-config-probe/unit"}
	approvals := 0
	require.NoError(t, applyModelPlan(t.Context(), bin, root, "project", plan, func(preview string) bool {
		approvals++
		require.Contains(t, preview, "modelFallback")
		require.Contains(t, preview, "defaultModel")
		return true
	}))
	require.Equal(t, 1, approvals)
	require.Equal(t, "cf-backup-probe/unit", nativeDefaultModel(t, bin, root, "project"))
	require.Zero(t, calls.Load())
}

func TestPlannedDefaultModel(t *testing.T) {
	require.Equal(t, "a/coder", plannedDefaultModel(config.ModelPlan{Routes: []config.ModelRoute{{Role: "expert", Primary: "x/y"}, {Role: "model", Primary: "a/coder"}}}))
	require.Equal(t, "b/coder", plannedDefaultModel(config.ModelPlan{Config: config.Config{Model: "b/coder"}}))
	require.Empty(t, plannedDefaultModel(config.ModelPlan{}))
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
