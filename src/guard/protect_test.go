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
		{".git/config", false},
		{"/repo/.git/hooks/pre-push", false},
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

	for _, p := range []string{"/repo/.empryo", "/repo/.claude", "/repo/.github/hooks", "/repo/.github"} {
		in := input(t, "Bash", map[string]any{"command": "mv " + p + " /tmp/x"})
		in.Paths = []string{p}
		require.Equal(t, []string{RuleSelf}, rules(CheckTool(in, false)), p)
	}

	in := input(t, "Bash", map[string]any{"command": "rm -rf /repo/.git/hooks"})
	in.Paths = []string{"/repo/.git/hooks"}
	require.Empty(t, rules(CheckTool(in, false)), "git's own folders are the user's to manage")

	in = input(t, "Bash", map[string]any{"command": "ls ~/.empryo/agents"})
	in.Paths = []string{filepath.Join(home, ".empryo", "agents")}
	require.Empty(t, rules(CheckTool(in, false)), "an unrelated subfolder is fine")
}
