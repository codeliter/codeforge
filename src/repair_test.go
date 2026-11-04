package src

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepairRestoresManagedFilesWithoutChangingUserData(t *testing.T) {
	home, wd, bins := t.TempDir(), t.TempDir(), t.TempDir()
	global, err := savePersona(home, []byte("# My global persona\nKeep this.\n"))
	require.NoError(t, err)
	local := writeRepoPersona(t, wd, "# My local persona\nKeep this too.\n")
	brokenConfig := filepath.Join(wd, ".codeforge", "config.yaml")
	require.NoError(t, os.WriteFile(brokenConfig, []byte("unknown: true\n---\ninvalid: [\n"), privateFile))
	empryoDir := filepath.Join(home, ".empryo")
	hooks := filepath.Join(empryoDir, "hooks.json")
	require.NoError(t, os.WriteFile(hooks, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"command":"'/tmp/cf' hook","failClosed":true},{"command":"notify-user"}]}]}}`), privateFile))
	marker := filepath.Join(home, "engine-called")
	require.NoError(t, os.WriteFile(filepath.Join(bins, "empryo"), []byte("#!/bin/sh\ntouch \"$CODEFORGE_REPAIR_ENGINE_CALLED\"\nexit 99\n"), execPerm))
	shimPath := filepath.Join(empryoDir, "bin", "gh")
	for _, command := range []string{"repair", "reinit"} {
		t.Run(command, func(t *testing.T) {
			require.NoError(t, os.MkdirAll(filepath.Dir(shimPath), privateDir))
			require.NoError(t, os.WriteFile(shimPath, []byte("#!/bin/sh\n"+shimMagic+"\n"), execPerm))
			raw, err := json.Marshal([]string{command})
			require.NoError(t, err)
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestModesCLI$")
			cmd.Dir = wd
			cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bins+string(os.PathListSeparator)+os.Getenv("PATH"), "CODEFORGE_CLI_ARGS="+string(raw), "CODEFORGE_REPAIR_ENGINE_CALLED="+marker)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
			require.Contains(t, string(out), "Personas, sessions and logins were kept.")
			data, err := os.ReadFile(hooks)
			require.NoError(t, err)
			require.NotContains(t, string(data), "/tmp/cf")
			require.Contains(t, string(data), "notify-user")
			require.Equal(t, 1, strings.Count(string(data), "cf' hook"))
			info, err := os.Stat(filepath.Join(empryoDir, "hooks", "cf"))
			require.NoError(t, err)
			require.Equal(t, os.FileMode(privateExec), info.Mode().Perm())
			require.NoFileExists(t, shimPath, "the gh shim is removed")
		})
	}
	data, err := os.ReadFile(global)
	require.NoError(t, err)
	require.Contains(t, string(data), "Keep this.")
	data, err = os.ReadFile(local)
	require.NoError(t, err)
	require.Contains(t, string(data), "Keep this too.")
	_, err = os.Stat(marker)
	require.True(t, os.IsNotExist(err), "repair must not call a model or sign in")
}
