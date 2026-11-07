//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func hookCall(command string) string {
	raw, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": "/", "tool_input": map[string]string{"command": command}})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestHook_ReviewAsksTheUserBeforeAPRCommentGoesUp(t *testing.T) {
	t.Setenv("CODEFORGE_GUARD", "review")
	var out bytes.Buffer
	old := hookOut
	hookOut = &out
	t.Cleanup(func() { hookOut = old })

	var stderr strings.Builder
	code := runHook(t.Context(), t.TempDir(), strings.NewReader(hookCall("gh pr comment 42 --body 'Request changes. a.go:3 drops data'")), &stderr)
	require.Equal(t, 0, code, "an ask is not a refusal: Empryo shows the call to the user")
	require.Empty(t, stderr.String())

	// Empryo's hook answer uses camelCase keys, so it is read as a map rather than a tagged struct.
	var answer map[string]map[string]string
	require.NoError(t, json.Unmarshal(out.Bytes(), &answer), out.String())
	got := answer["hookSpecificOutput"]
	require.Equal(t, "PreToolUse", got["hookEventName"])
	require.Equal(t, "ask", got["permissionDecision"])
	require.Contains(t, got["permissionDecisionReason"], "asks you first")
}

func TestHook_ARunWithNobodyToApproveRefusesTheCommentInstead(t *testing.T) {
	t.Setenv("CODEFORGE_GUARD", "review")
	t.Setenv(noAskEnv, "1")
	var out bytes.Buffer
	old := hookOut
	hookOut = &out
	t.Cleanup(func() { hookOut = old })

	var stderr strings.Builder
	code := runHook(t.Context(), t.TempDir(), strings.NewReader(hookCall("gh pr comment 42 --body 'x'")), &stderr)
	require.Equal(t, hookExitBlock, code, "a headless run is never asked: it would depend on how the engine answers")
	require.Contains(t, stderr.String(), "Nobody is here to approve it")
	require.Empty(t, out.String())
}

func TestHook_ReviewStillRefusesEverythingElseThatWrites(t *testing.T) {
	t.Setenv("CODEFORGE_GUARD", "review")
	var out bytes.Buffer
	old := hookOut
	hookOut = &out
	t.Cleanup(func() { hookOut = old })

	for _, cmd := range []string{
		"gh pr comment 42 -b 'x' && rm a.txt",
		"gh pr comment 42 --body-file notes.md",
		"gh pr review 42 --approve -b ok",
		"gh pr merge 42",
		"git push",
	} {
		var stderr strings.Builder
		require.Equal(t, hookExitBlock, runHook(t.Context(), t.TempDir(), strings.NewReader(hookCall(cmd)), &stderr), cmd)
		require.NotEmpty(t, stderr.String(), cmd)
	}
	require.Empty(t, out.String(), "a refusal never writes an approval request")
}
