package src

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/guard"
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
	home := t.TempDir()
	in := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"git push --force"}}`

	var stderr strings.Builder
	require.Equal(t, hookExitBlock, runHook(t.Context(), home, strings.NewReader(in), &stderr))
	require.Contains(t, stderr.String(), "force-push")

	stderr.Reset()
	ok := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"git status"}}`
	require.Equal(t, 0, runHook(t.Context(), home, strings.NewReader(ok), &stderr))

	require.Equal(t, hookExitBlock, runHook(t.Context(), home, strings.NewReader("not json"), &stderr), "fail closed on garbage")
}

func TestRunHook_ReviewModeIsReadOnly(t *testing.T) {
	t.Setenv("CODEFORGE_GUARD", "review")
	in := `{"hook_event_name":"PreToolUse","tool_name":"edit_file","cwd":"/","tool_input":{"path":"a.go"}}`

	var stderr strings.Builder
	require.Equal(t, hookExitBlock, runHook(t.Context(), t.TempDir(), strings.NewReader(in), &stderr))
}

func TestRunHook_DoesNotSpendApprovals(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, runAllowAt(home, "/", "merge"))

	in := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"gh pr merge 1"}}`
	var stderr strings.Builder
	require.Equal(t, 0, runHook(t.Context(), home, strings.NewReader(in), &stderr))
	require.Equal(t, 0, runHook(t.Context(), home, strings.NewReader(in), &stderr), "still approved for the gh shim to spend")
}

func runAllowAt(home, repo, rule string) error {
	st, err := guard.Load(stateDir(home))
	if err != nil {
		return fmt.Errorf("loading: %w", err)
	}
	st.Allow(repo, rule, time.Now())
	if err := st.Save(stateDir(home)); err != nil {
		return fmt.Errorf("saving: %w", err)
	}
	return nil
}

func TestRunHook_SpendsHookOnlyApprovals(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "init", "-q")
	require.NoError(t, cmd.Run())
	root, err := guard.RepoRoot(t.Context(), repo)
	require.NoError(t, err)

	for _, tt := range []struct{ rule, command string }{
		{"whole-tree", "git reset --hard"},
		{"infra", "terraform apply"},
		{"no-verify", "git commit --no-verify -m x"},
		{"delete-branch", "git branch -D old"},
	} {
		require.NoError(t, runAllowAt(home, root, tt.rule))
		in := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"` + root + `","tool_input":{"command":"` + tt.command + `"}}`

		var stderr strings.Builder
		require.Equal(t, 0, runHook(t.Context(), home, strings.NewReader(in), &stderr), tt.rule)
		require.Equal(t, hookExitBlock, runHook(t.Context(), home, strings.NewReader(in), &stderr), tt.rule+" is spent after one use")
	}
}
