package guard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtectedPath(t *testing.T) {
	home := "/Users/me"
	tests := []struct {
		path string
		want bool
	}{
		{"~/.codeforge/state.json", true},
		{"$HOME/.codeforge/config.json", true},
		{"/Users/me/.codeforge", true},
		{"/Users/me/.empryo/hooks.json", true},
		{"/Users/me/.empryo/config.json", true},
		{".empryo/config.json", true},
		{".claude/settings.local.json", true},
		{".github/hooks/x.json", true},
		{"/Users/me/.empryo/agents/codeforge-ticket.md", false},
		{"src/config.json", false},
		{"README.md", false},
		{"/Users/me/.codeforgex/a", false},
		{"/Users/me/.empryo/bin/gh", true},
		{".git/config", true},
		{"/repo/.git/hooks/pre-push", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, protectedPath(tt.path, "/repo", home))
		})
	}
}

func TestCheckTool_Protected(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	state := filepath.Join(home, ".codeforge", "state.json")

	in := input(t, "Bash", map[string]any{"command": "echo '{}' > " + state})
	in.Paths = []string{state}
	require.Equal(t, []string{RuleSelf}, rules(CheckTool(in, false)), "shell write to state is blocked outside review mode")

	in = input(t, "edit_file", map[string]any{"path": "~/.empryo/hooks.json"})
	in.Paths = []string{"~/.empryo/hooks.json"}
	require.Equal(t, []string{RuleSelf}, rules(CheckTool(in, false)))

	in = input(t, "read", map[string]any{"path": state})
	in.Paths = []string{state}
	require.Empty(t, rules(CheckTool(in, false)), "reading is fine")

	in = input(t, "settings", map[string]any{"action": "set", "key": "disableAllHooks", "value": "true"})
	require.Equal(t, []string{RuleSelf}, rules(CheckTool(in, false)))
}

func TestCheckTool_SettingsReadsMayNameHooks(t *testing.T) {
	for _, args := range []map[string]any{
		{"action": "get", "key": "hooks"},
		{"action": "list", "query": "hook"},
		{"action": "set", "key": "defaultModel", "value": "anthropic/claude-opus-5.5"},
	} {
		require.Empty(t, rules(CheckTool(input(t, "settings", args), false)), args)
		require.Empty(t, rules(CheckTool(input(t, "run_tool", map[string]any{"name": "settings", "args": args}), false)), args)
	}

	for _, args := range []map[string]any{
		{"action": "set", "key": "disableAllHooks", "value": "true"},
		{"action": "unset", "key": "hooks"},
		{"key": "hooks", "value": "{}"},
	} {
		require.Equal(t, []string{RuleSelf}, rules(CheckTool(input(t, "settings", args), false)), args)
		require.Equal(t, []string{RuleSelf}, rules(CheckTool(input(t, "run_tool", map[string]any{"name": "settings", "args": args}), false)), args)
	}

	in := input(t, "run_tool", map[string]any{"name": "settings", "describe": "", "note": "hooks"})
	require.Equal(t, []string{RuleSelf}, rules(CheckTool(in, false)), "no action fails closed")

	for _, code := range []string{
		`await api.tool('settings', {action: 'set', key: 'disableAllHooks', value: 'true'})`,
		`await api.tool("run_tool", {name: "settings", args: {action: "unset", key: "hooks"}})`,
	} {
		require.Equal(t, []string{RuleSelf}, rules(CheckTool(input(t, "code_script", map[string]any{"code": code}), false)), code)
	}
	require.Empty(t, rules(CheckTool(input(t, "code_script", map[string]any{"code": `await api.tool('settings', {action: 'set', key: 'defaultModel', value: 'x'})`}), false)))
}

func TestCheckTool_ProtectsEnforcementFolders(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	for _, p := range []string{
		filepath.Join(home, ".empryo", "bin"), filepath.Join(home, ".empryo"), "~/.empryo/bin", filepath.Join(home, ".codeforge"),
	} {
		in := input(t, "Bash", map[string]any{"command": "mv " + p + " /tmp/x"})
		in.Paths = []string{p, "/tmp/x"}
		require.Equal(t, []string{RuleSelf}, rules(CheckTool(in, false)), p)
	}

	for _, p := range []string{"/repo/.empryo", "/repo/.claude", "/repo/.git/hooks", "/repo/.github/hooks", "/repo/.github"} {
		in := input(t, "Bash", map[string]any{"command": "mv " + p + " /tmp/x"})
		in.Paths = []string{p}
		require.Equal(t, []string{RuleSelf}, rules(CheckTool(in, false)), p)
	}

	in := input(t, "Bash", map[string]any{"command": "git status"})
	in.Paths = []string{"/repo/.git"}
	require.Empty(t, rules(CheckTool(in, false)), "the .git folder itself is not off limits to git")

	in = input(t, "Bash", map[string]any{"command": "mv /repo/.git /tmp/g"})
	in.Paths = []string{"/repo/.git", "/tmp/g"}
	require.Equal(t, []string{RuleSelf}, rules(CheckTool(in, false)), "moving .git away would expose its hooks")

	in = input(t, "Bash", map[string]any{"command": "ls ~/.empryo/agents"})
	in.Paths = []string{filepath.Join(home, ".empryo", "agents")}
	require.Empty(t, rules(CheckTool(in, false)), "an unrelated subfolder is fine")
}
