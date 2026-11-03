package src

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallHooksReplacesLegacyTemporaryBinary(t *testing.T) {
	dir := t.TempDir()
	old := `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"'/tmp/cf' hook","failClosed":true},{"type":"command","command":"notify-user"}]}],"SessionEnd":[{"hooks":[{"type":"command","command":"'/tmp/cf' session-end"}]}]}}`
	p := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(p, []byte(old), privateFile))
	self := filepath.Join(dir, "hooks", "cf")
	require.NoError(t, installHooks(dir, self))
	require.NoError(t, installHooks(dir, self))
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NotContains(t, string(data), "/tmp/cf")
	require.Contains(t, string(data), "notify-user")
	require.Equal(t, 1, strings.Count(string(data), "cf' hook"))
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	hooks, ok := doc["hooks"].(map[string]any)
	require.True(t, ok)
	rules, ok := hooks["PreToolUse"].([]any)
	require.True(t, ok)
	rule, ok := rules[len(rules)-1].(map[string]any)
	require.True(t, ok)
	list, ok := rule["hooks"].([]any)
	require.True(t, ok)
	guard, ok := list[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, guard["failClosed"])
}
