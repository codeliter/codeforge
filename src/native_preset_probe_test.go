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

// Optional installed-runtime regression: isolated HOME/workspace, loopback only,
// and no prompt, model turn, or modification to the user's native settings.
func TestNativeConfigReadProbe(t *testing.T) {
	if os.Getenv("CODEFORGE_NATIVE_PRESET_PROBE") != "1" {
		t.Skip("optional installed-runtime integration probe")
	}
	bin, err := exec.LookPath("empryo")
	require.NoError(t, err)
	home, wd := t.TempDir(), t.TempDir()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "no model turns expected", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("HOME", home)
	t.Setenv("EMPRYO_PRESETS", "")
	t.Setenv("EMPRYO_NO_PROMPT", "1")
	t.Setenv("EMPRYO_TELEMETRY", "0")
	paths := []string{}
	originals := [][]byte{}
	for _, layer := range []struct{ root, model string }{{home, "global/backup"}, {wd, "project/backup"}} {
		dir := filepath.Join(layer.root, ".empryo")
		require.NoError(t, os.MkdirAll(dir, privateDir))
		data, err := json.Marshal(map[string]any{"liveTranslations": false, "liveSkills": false, "defaultModel": "cf-config-probe/unit", "providers": []any{map[string]any{"id": "cf-config-probe", "baseURL": server.URL + "/v1", "models": []string{"unit"}, "modelsAPI": false}}, "modelFallback": map[string][]string{"primary/unit": {layer.model}}})
		require.NoError(t, err)
		path := filepath.Join(dir, "config.json")
		require.NoError(t, os.WriteFile(path, data, privateFile))
		paths = append(paths, path)
		originals = append(originals, data)
	}
	snapshot, err := readNativeFallbacks(t.Context(), bin, wd)
	require.NoError(t, err)
	require.Equal(t, []string{"global/backup"}, snapshot.Global["primary/unit"])
	require.Equal(t, []string{"project/backup"}, snapshot.Project["primary/unit"])
	require.Equal(t, []string{"project/backup"}, snapshot.Effective["primary/unit"])
	require.Zero(t, calls.Load(), "reading config must not send a model request")
	for i, path := range paths {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, originals[i], data, "native config bytes must not change")
	}
}
