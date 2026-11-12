package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

func withVersion(t *testing.T, v string) {
	t.Helper()
	t.Setenv("CODEFORGE_GUARD", "") // a review session running the tests must not switch repair off
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
	require.NoError(t, writeFile(config.Dir(home), repairStampFile, []byte("0.5.2\n"), privateFile))
	require.Equal(t, "0.5.3", autoRepairVersion(home), "an older stamp repairs")
	require.NoError(t, writeFile(config.Dir(home), repairStampFile, []byte("0.5.3\n"), privateFile))
	require.Empty(t, autoRepairVersion(home), "the same version does not repair again")
	t.Setenv(launchMarker, "1")
	withVersion(t, "0.5.4")
	require.Empty(t, autoRepairVersion(home), "agent shells inside a session never rewrite the guard")

	t.Setenv(launchMarker, "")
	t.Setenv("CODEFORGE_GUARD", "review")
	require.Empty(t, autoRepairVersion(home), "a review session never repairs, even without the launch marker")
}

// TestPrintingCommandsDoNotRepairInAReleaseBuild runs the forms review mode allows through Run in a release build whose
// repair is due and whose launch markers are cleared (an inline CODEFORGE_GUARD= CODEFORGE_LAUNCH= does that): they must
// leave the stamp, the managed files and the trust ledger alone.
func TestPrintingCommandsDoNotRepairInAReleaseBuild(t *testing.T) {
	for _, args := range [][]string{
		{"models"}, {"models", "recommend"}, {"provider"}, {"provider", "show"}, {"provider", "list"},
		{"providers"}, {"providers", "show"}, {"providers", "list"},
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv(launchMarker, "")
		t.Setenv("CODEFORGE_GUARD", "")
		t.Chdir(t.TempDir())
		withVersion(t, "0.5.3")
		require.NoError(t, writeFile(config.Dir(home), repairStampFile, []byte("0.5.2\n"), privateFile))
		require.NotEmpty(t, autoRepairVersion(home), "a repair is due, so only the command can be what skips it")

		t.Setenv("PATH", t.TempDir()) // no Empryo anywhere, so the command stops there and nothing else can write
		require.ErrorIs(t, Run(t.Context(), args), errEmpryoNotFound)
		stamp, err := os.ReadFile(filepath.Join(config.Dir(home), repairStampFile))
		require.NoError(t, err)
		require.Equal(t, "0.5.2\n", string(stamp), "%v: the repair stamp did not move", args)
		require.NoDirExists(t, filepath.Join(home, ".empryo"), "%v: no managed hooks, profiles or skills were installed", args)
		require.NoFileExists(t, filepath.Join(config.Dir(home), "trusted-personas.json"), "%v: no trust ledger", args)
	}
}

func TestAutoRepairRebuildsOnceAndKeepsUserData(t *testing.T) {
	home, wd := t.TempDir(), t.TempDir()
	t.Setenv(launchMarker, "")
	withVersion(t, "0.5.3")
	_, err := savePersona(home, []byte("# Mine\nKeep this.\n"))
	require.NoError(t, err)
	empryoDir := filepath.Join(home, ".empryo")
	hooks := filepath.Join(empryoDir, "hooks.json")
	require.NoError(t, os.MkdirAll(empryoDir, privateDir))
	require.NoError(t, os.MkdirAll(filepath.Join(empryoDir, "bin"), privateDir))
	shimPath := filepath.Join(empryoDir, "bin", "gh")
	require.NoError(t, os.WriteFile(shimPath, []byte("#!/bin/sh\n"+shimMagic+"\nexec codeforge gh-shim\n"), execPerm))
	require.NoError(t, os.WriteFile(hooks, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"command":"'/tmp/cf' hook","failClosed":true},{"command":"notify-user"}]}]}}`), privateFile))
	var out strings.Builder
	require.NoError(t, autoRepair(home, wd, &out))
	require.Contains(t, out.String(), "updated to 0.5.3")
	data, err := os.ReadFile(hooks)
	require.NoError(t, err)
	require.Contains(t, string(data), "notify-user", "user hooks are kept")
	require.Contains(t, string(data), filepath.Join(empryoDir, "hooks", "cf"), "the guard points at the stable hook binary")
	require.NoFileExists(t, shimPath, "the gh shim is removed")
	stamp, err := os.ReadFile(filepath.Join(config.Dir(home), repairStampFile))
	require.NoError(t, err)
	require.Equal(t, "0.5.3\n", string(stamp))
	persona, err := readPersona(home)
	require.NoError(t, err)
	require.Contains(t, string(persona), "Keep this.")
	out.Reset()
	require.NoError(t, autoRepair(home, wd, &out))
	require.Empty(t, out.String(), "a second start with the same version does nothing")
}
