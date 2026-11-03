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

func TestRepairLegacyOptions(t *testing.T) {
	for _, args := range [][]string{{"repair", "--legacy-hook"}, {"repair", "--legacy-hook", "relative"}, {"repair", "--unknown"}} {
		_, err := parseRepairArgs(args)
		require.ErrorIs(t, err, errRepairUsage)
	}
	opts, err := parseRepairArgs([]string{"reinit", "--legacy-hook", "/tmp/old-bin"})
	require.NoError(t, err)
	require.True(t, opts.enabled)
	require.Equal(t, []string{"/tmp/old-bin"}, opts.legacyHooks)
}

func TestLegacyHookMigrationIsExplicitAndExact(t *testing.T) {
	hook := map[string]any{"command": "'/tmp/old-bin' hook", "failClosed": true}
	require.False(t, isOurHook(hook, hookCommand), "unknown security hooks must not be deleted automatically")
	require.True(t, isOurHook(hook, hookCommand, "/tmp/old-bin"))
	for _, command := range []string{"echo /tmp/old-bin", "'/tmp/old-bin-extra' hook", "'/tmp/old-bin' other-command", "'/tmp/old-bin' hook | notify"} {
		require.False(t, matchesLegacyHook(command, hookCommand, []string{"/tmp/old-bin"}), command)
	}
}

func TestRepairMultipleLegacyHooks(t *testing.T) {
	home, wd := t.TempDir(), t.TempDir()
	dir := filepath.Join(home, ".empryo")
	require.NoError(t, os.MkdirAll(dir, privateDir))
	p := filepath.Join(dir, "hooks.json")
	doc := `{"hooks":{"PreToolUse":[{"hooks":[{"command":"'/tmp/old-bin' hook","failClosed":true},{"command":"'/tmp/new-bin' hook","failClosed":true},{"command":"notify-user"}]}],"SessionEnd":[{"hooks":[{"command":"'/tmp/new-bin' session-end"}]}]}}`
	require.NoError(t, os.WriteFile(p, []byte(doc), privateFile))
	raw, err := json.Marshal([]string{"repair", "--legacy-hook", "/tmp/old-bin", "--legacy-hook", "/tmp/new-bin"})
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestModesCLI$")
	cmd.Dir = wd
	cmd.Env = append(os.Environ(), "HOME="+home, "CODEFORGE_CLI_ARGS="+string(raw))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NotContains(t, string(data), "/tmp/old-bin")
	require.NotContains(t, string(data), "/tmp/new-bin")
	require.Contains(t, string(data), "notify-user")
	require.Equal(t, 1, strings.Count(string(data), "cf' hook"))
	require.Equal(t, 1, strings.Count(string(data), "cf' session-end"))
	require.Contains(t, string(data), `"failClosed": true`)
}

func TestRepairExplicitLegacyHook(t *testing.T) {
	for _, oldCommand := range []string{"'/tmp/old-bin' hook", "/tmp/old-bin", "exec '/tmp/old-bin' hook"} {
		t.Run(oldCommand, func(t *testing.T) {
			home, wd := t.TempDir(), t.TempDir()
			dir := filepath.Join(home, ".empryo")
			require.NoError(t, os.MkdirAll(dir, privateDir))
			legacy := map[string]any{"command": oldCommand, "failClosed": true}
			other := map[string]any{"command": "'/tmp/other-security-tool' hook", "failClosed": true}
			doc := map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"hooks": []any{legacy, other}}}}}
			data, err := json.Marshal(doc)
			require.NoError(t, err)
			p := filepath.Join(dir, "hooks.json")
			require.NoError(t, os.WriteFile(p, data, privateFile))
			raw, err := json.Marshal([]string{"repair", "--legacy-hook", "/tmp/old-bin"})
			require.NoError(t, err)
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestModesCLI$")
			cmd.Dir = wd
			cmd.Env = append(os.Environ(), "HOME="+home, "CODEFORGE_CLI_ARGS="+string(raw))
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
			data, err = os.ReadFile(p)
			require.NoError(t, err)
			require.NotContains(t, string(data), "/tmp/old-bin")
			require.Contains(t, string(data), "/tmp/other-security-tool")
			require.Contains(t, string(data), `"failClosed": true`)
			require.Equal(t, 1, strings.Count(string(data), "cf' hook"))
		})
	}
}
