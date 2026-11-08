package src

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func completionScript(t *testing.T, shell string) string {
	t.Helper()
	var b strings.Builder
	require.NoError(t, runCompletion(&b, []string{shell}))
	path := filepath.Join(t.TempDir(), "codeforge."+shell)
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

func TestCompletion_ArgumentsAreChecked(t *testing.T) {
	var b strings.Builder
	require.ErrorIs(t, runCompletion(&b, nil), errCompletionShell)
	require.ErrorIs(t, runCompletion(&b, []string{"powershell"}), errCompletionShell)
	require.Empty(t, b.String())
}

// bashComplete returns what bash completes for the words, the last one being the word under the cursor.
func bashComplete(t *testing.T, script string, words ...string) []string {
	t.Helper()
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = "'" + w + "'"
	}
	cmd := exec.CommandContext(t.Context(), "bash", "--norc", "-c",
		"source '"+script+"'; COMP_WORDS=(codeforge "+strings.Join(quoted, " ")+"); COMP_CWORD="+
			strconv.Itoa(len(words))+"; _codeforge; printf '%s\\n' \"${COMPREPLY[@]}\"")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.Fields(string(out))
}

func TestCompletion_Bash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	script := completionScript(t, "bash")
	require.Equal(t, []string{"repair", "respond", "review"}, sortedOf(bashComplete(t, script, "re")))
	require.Equal(t, []string{"apply"}, bashComplete(t, script, "models", "a"))
	require.Equal(t, []string{"oss", "pragmatic", "regulated", "startup"}, bashComplete(t, script, "init", "--persona", ""))
	require.Equal(t, []string{"--comment", "--comment=yes"}, sortedOf(bashComplete(t, script, "review", "--com")))
	require.Empty(t, bashComplete(t, script, "review", "--repo", ""), "a value that cannot be listed completes nothing")
}

func sortedOf(s []string) []string { slices.Sort(s); return s }

func TestCompletion_ScriptsParse(t *testing.T) {
	for shell, check := range map[string][]string{"zsh": {"zsh", "-n"}, "bash": {"bash", "-n"}, "fish": {"fish", "--no-execute"}} {
		if _, err := exec.LookPath(check[0]); err != nil {
			continue
		}
		out, err := exec.CommandContext(t.Context(), check[0], append(check[1:], completionScript(t, shell))...).CombinedOutput()
		require.NoError(t, err, "%s: %s", shell, out)
	}
}

func TestCompletion_ZshCompletesInAShell(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	script := completionScript(t, "zsh")
	// Stand-ins for the completion system: record what would be offered for "codeforge models <tab>".
	probe := `_describe() { print -l -- "${(@P)4}"; }; compadd() { shift; print -l -- "$@"; }; _message() { :; }; compdef() { :; }
source '` + script + `'
words=(codeforge models ''); CURRENT=3; _codeforge
words=(codeforge init --persona ''); CURRENT=4; _codeforge
words=(codeforge ''); CURRENT=2; _codeforge`
	out, err := exec.CommandContext(t.Context(), "zsh", "-f", "-c", probe).CombinedOutput()
	require.NoError(t, err, string(out))
	got := string(out)
	require.Contains(t, got, "configure:Choose models interactively")
	require.Contains(t, got, "pragmatic")
	require.Contains(t, got, "review:Read-only review of a PR, or of your local changes")
	require.Contains(t, got, "completion:Print the shell completion script")
}

func TestCompletion_EveryUserCommandIsCompleted(t *testing.T) {
	var listed []string
	for _, c := range completionTree {
		listed = append(listed, c.cmd.name)
	}
	// Aliases, retired words and the hooks Empryo and git call are not offered.
	hidden := []string{"start", "setup", "providers", "allow", "reinit", "hook", "pre-push", "gh-shim", "session-end", "observe"}
	for _, cmd := range RegisteredCommands() {
		if !slices.Contains(hidden, cmd) {
			require.Contains(t, listed, cmd)
		}
	}
}
