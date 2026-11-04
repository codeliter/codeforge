package src

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallHooks_KeepsUserHooks(t *testing.T) {
	dir := t.TempDir()
	user := `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"prettier"}]}],"Stop":[{"hooks":[{"type":"command","command":"notify"}]}]},"disableAllHooks":false}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(user), 0o600))

	require.NoError(t, installHooks(dir, "/usr/local/bin/codeforge"))
	require.NoError(t, installHooks(dir, "/usr/local/bin/codeforge"))

	data, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(data), "codeforge' hook"), "installing twice must not duplicate the guard")
	require.Contains(t, string(data), "prettier")
	require.Contains(t, string(data), "notify")

	var doc struct {
		Hooks map[string][]struct {
			Hooks []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	rules := doc.Hooks["PreToolUse"]
	last := rules[len(rules)-1].Hooks[0]
	require.Equal(t, true, last["failClosed"])
	require.Equal(t, "'/usr/local/bin/codeforge' hook", last["command"])
}

func TestRunHook(t *testing.T) {
	t.Setenv("CODEFORGE_GUARD", "")
	home := t.TempDir()
	var stderr strings.Builder

	for _, cmd := range []string{"git push --force", "git status", "git reset --hard", "gh pr merge 1", "gh pr comment 1 -b x", "gh pr create --fill", "gh api repos/{owner}/{repo}/pulls -f title=x", "terraform apply"} {
		in := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"` + cmd + `"}}`
		require.Equal(t, 0, runHook(t.Context(), home, strings.NewReader(in), &stderr), "outside review git and gh are not gated: "+cmd)
	}

	for _, cmd := range []string{"codeforge trust", "codeforge trust --models"} {
		in := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"` + cmd + `"}}`
		require.Equal(t, hookExitBlock, runHook(t.Context(), home, strings.NewReader(in), &stderr), "an agent cannot approve its own trust: "+cmd)
	}
	require.Contains(t, stderr.String(), "Only the user")
	require.Equal(t, hookExitBlock, runHook(t.Context(), home, strings.NewReader("not json"), &stderr), "fail closed on garbage")
}

func TestRunHook_ReviewModeIsReadOnly(t *testing.T) {
	t.Setenv("CODEFORGE_GUARD", "review")
	var stderr strings.Builder
	for _, in := range []string{
		`{"hook_event_name":"PreToolUse","tool_name":"edit_file","cwd":"/","tool_input":{"path":"a.go"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"git push"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"gh pr create --fill"}}`,
	} {
		require.Equal(t, hookExitBlock, runHook(t.Context(), t.TempDir(), strings.NewReader(in), &stderr), in)
	}
}

func TestRunHook_ReviewFailsClosed(t *testing.T) {
	t.Setenv("CODEFORGE_GUARD", "review")
	var stderr strings.Builder
	require.Equal(t, hookExitBlock, runHook(t.Context(), t.TempDir(), strings.NewReader("not json"), &stderr), "fail closed on garbage in review")

	trust := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"codeforge trust"}}`
	require.Equal(t, hookExitBlock, runHook(t.Context(), t.TempDir(), strings.NewReader(trust), &stderr))
	require.Contains(t, stderr.String(), "review mode is read-only")

	read := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"git status"}}`
	require.Equal(t, 0, runHook(t.Context(), t.TempDir(), strings.NewReader(read), &stderr), "review can still read")
}
