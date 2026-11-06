package src

import (
	"bytes"
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

	for _, cmd := range []string{"codeforge trust", "scripts/sync.py", "git commit -m \"sync \\`scripts/sync.py\\` and \\`/v1/models\\`\""} {
		raw, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": "/", "tool_input": map[string]any{"command": cmd}})
		require.NoError(t, err)
		require.Equal(t, 0, runHook(t.Context(), home, bytes.NewReader(raw), &stderr), "outside review only learning writes are gated: "+cmd)
	}
	require.Equal(t, 0, runHook(t.Context(), home, strings.NewReader("not json"), &stderr), "outside review even garbage passes")

	for _, cmd := range []string{"codeforge observe", "echo '{}' | codeforge observe", "codeforge learn correct x"} {
		in := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"` + cmd + `"}}`
		require.Equal(t, hookExitBlock, runHook(t.Context(), home, strings.NewReader(in), &stderr), "an agent cannot forge learning evidence: "+cmd)
	}
	require.Contains(t, stderr.String(), "learning evidence")

	for _, cmd := range []string{
		"git commit -m \"document \\`codeforge observe\\`\"",
		"git commit -m 'document `codeforge observe` and $(codeforge learn correct x)'",
		"git commit -m \"codeforge observe \\\\$(x)\"",
		"git commit -m \"document codeforge observe on $(date +%F)\"",
		"git commit -m \"document $(printf codeforge)\"",
		"git commit -m \"document codeforge observe via $(printf codeforge)\"",
		"v=$(date); codeforge status",
		"git commit -m \"it's codeforge observe $(date)\"",
	} {
		raw, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": "/", "tool_input": map[string]any{"command": cmd}})
		require.NoError(t, err)
		require.Equal(t, 0, runHook(t.Context(), home, bytes.NewReader(raw), &stderr), "literal text is not a call: "+cmd)
	}

	for _, cmd := range []string{"/usr/bin/curl --version", "/usr/bin/ssh -V"} {
		raw, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": "/", "tool_input": map[string]any{"command": cmd}})
		require.NoError(t, err)
		require.Equal(t, 0, runHook(t.Context(), home, bytes.NewReader(raw), &stderr), "native executables are not scripts: "+cmd)
	}

	protectedIn := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/","tool_input":{"command":"codeforge observe < ~/.codeforge/payload.json"},"tool_paths":["~/.codeforge/payload.json"]}`
	require.Equal(t, hookExitBlock, runHook(t.Context(), home, strings.NewReader(protectedIn), &stderr), "a protected path must not hide the observer call")

	for _, cmd := range []string{
		"git -c alias.forge='!codeforge observe < payload.json' forge",
		"codeforge observe <(cat payload.json)",
		"$(echo codeforge) observe < payload.json",
		"o=observe; codeforge $o < payload.json",
		"l=learn; codeforge $l correct x",
		"o=observe; codeforge ${o} < payload.json",
		"o=observe; codeforge \"$o\" < payload.json",
		"codeforge $(echo observe) < payload.json",
		"codeforge \"$(echo observe)\" < payload.json",
		"echo hi; $(echo codeforge) observe < payload.json",
		"sudo `echo codeforge` observe",
		"code$(echo forge) observe",
		"echo \"don't\"; $(echo codeforge) observe < p.json",
		"git commit -m \"it's fine\" && code$(echo forge) observe < p.json",
		"codeforge \\\nobserve < payload.json",
		"tmux new -d 'codeforge observe < payload.json'",
		"script -q /dev/null sh -c 'codeforge learn correct x'",
		`python3 -c 'import os; os.system("codeforge " + "ob" + "serve")'`,
	} {
		raw, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": "/", "tool_input": map[string]any{"command": cmd}})
		require.NoError(t, err)
		require.Equal(t, hookExitBlock, runHook(t.Context(), home, bytes.NewReader(raw), &stderr), "forged evidence through the hook: "+cmd)
	}
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
