package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func withVersion(t *testing.T, v string) {
	t.Helper()
	old := Version
	Version = v
	t.Cleanup(func() { Version = old })
}

func TestAutoRepairVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv(launchMarker, "")
	withVersion(t, "dev")
	require.Empty(t, autoRepairVersion(home), "development builds never repair on their own")
	withVersion(t, "0.5.3")
	require.Equal(t, "0.5.3", autoRepairVersion(home), "no stamp means the installed files predate this version")
	require.NoError(t, writeFile(configDir(home), repairStampFile, []byte("0.5.2\n"), privateFile))
	require.Equal(t, "0.5.3", autoRepairVersion(home), "an older stamp repairs")
	require.NoError(t, writeFile(configDir(home), repairStampFile, []byte("0.5.3\n"), privateFile))
	require.Empty(t, autoRepairVersion(home), "the same version does not repair again")
	t.Setenv(launchMarker, "1")
	withVersion(t, "0.5.4")
	require.Empty(t, autoRepairVersion(home), "agent shells inside a session never rewrite the guard")
}

func TestAutoRepairRebuildsOnceAndKeepsUserData(t *testing.T) {
	home, wd := t.TempDir(), t.TempDir()
	t.Setenv(launchMarker, "")
	withVersion(t, "0.5.3")
	global, err := savePersona(home, []byte("# Mine\nKeep this.\n"))
	require.NoError(t, err)
	empryoDir := filepath.Join(home, ".empryo")
	hooks := filepath.Join(empryoDir, "hooks.json")
	require.NoError(t, os.MkdirAll(empryoDir, privateDir))
	require.NoError(t, os.WriteFile(hooks, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"command":"'/tmp/cf' hook","failClosed":true},{"command":"notify-user"}]}]}}`), privateFile))
	var out strings.Builder
	require.NoError(t, autoRepair(home, wd, &out))
	require.Contains(t, out.String(), "updated to 0.5.3")
	data, err := os.ReadFile(hooks)
	require.NoError(t, err)
	require.Contains(t, string(data), "notify-user", "user hooks are kept")
	require.Contains(t, string(data), filepath.Join(empryoDir, "hooks", "cf"), "the guard points at the stable hook binary")
	shim, err := os.ReadFile(filepath.Join(empryoDir, "bin", "gh"))
	require.NoError(t, err)
	require.Contains(t, string(shim), "gh-shim")
	stamp, err := os.ReadFile(filepath.Join(configDir(home), repairStampFile))
	require.NoError(t, err)
	require.Equal(t, "0.5.3\n", string(stamp))
	persona, err := os.ReadFile(global)
	require.NoError(t, err)
	require.Contains(t, string(persona), "Keep this.")
	out.Reset()
	require.NoError(t, autoRepair(home, wd, &out))
	require.Empty(t, out.String(), "a second start with the same version does nothing")
}
