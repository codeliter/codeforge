package guard

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheck_LearningWritesAreUserOnly(t *testing.T) {
	for _, cmd := range []string{"codeforge learn list", "codeforge learn list --all", "codeforge learn propose", "codeforge learn habits"} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}
	for _, cmd := range []string{
		"codeforge learn",
		"codeforge learn correct use table tests",
		"codeforge learn correct --session s1 'use table tests'",
		"codeforge learn outcome -- go test ./...",
		"codeforge learn outcome -- gh pr create --fill",
		"codeforge learn revert 3",
		"codeforge learn approve 3",
		"/opt/homebrew/bin/codeforge learn correct x",
		"env codeforge learn correct x",
		"go test ./... && codeforge learn outcome -- sh -c 'gh pr merge 1'",
	} {
		require.Equal(t, []string{RuleSelf}, rules(Check(cmd, false)), cmd)
	}
	got := CheckTool(input(t, "code_script", map[string]any{"code": `await api.run("codeforge learn correct x")`}), false)
	require.Equal(t, []string{RuleSelf}, rules(got))
}

func TestCheck_AgentCannotFeedTheObserver(t *testing.T) {
	for _, cmd := range []string{
		"codeforge observe",
		"echo '{}' | codeforge observe",
		"/opt/homebrew/bin/codeforge observe < payload.json",
		"env CODEFORGE_LAUNCH=1 codeforge observe",
	} {
		require.Equal(t, []string{RuleSelf}, rules(Check(cmd, false)), cmd)
	}
}
