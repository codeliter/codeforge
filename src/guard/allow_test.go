//nolint:err113 // dynamic error messages are okay in tests
package guard

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheck_GitIsAllowedOutsideReview(t *testing.T) {
	for _, cmd := range []string{
		"git push origin main",
		"git push --force origin main",
		"git push origin --delete old",
		"git push --no-verify",
		"git commit -am 'x'",
		"git add -A",
		"git reset --hard HEAD~1",
		"git stash",
		"git branch -D old",
		"git -c core.hooksPath=/dev/null push",
		"git config --local hook.x.command true",
		"cd ../other && git push",
		`sh -c 'git push --force'`,
		"git push origin main 2>&1 | tail -1",
	} {
		got := Check(cmd, false)
		require.Empty(t, rules(got), cmd)
		require.Empty(t, got.Gate, cmd)
	}
}

func TestCheck_GitIsReadOnlyInReview(t *testing.T) {
	for _, cmd := range []string{"git push origin main", "git commit -m x", "git checkout main", "git -c core.pager=sh log"} {
		require.Equal(t, []string{RuleReadOnly}, rules(Check(cmd, true)), cmd)
	}
	require.Empty(t, rules(Check("git log --oneline", true)))
}

func TestCheck_GHAndInfraStillGuarded(t *testing.T) {
	require.Equal(t, []string{RuleMerge}, rules(Check("gh pr merge 1", false)))
	require.Equal(t, GateOpenPR, Check("gh pr create --fill", false).Gate)
	require.Equal(t, []string{RuleInfra}, rules(Check("terraform apply", false)))
	require.Equal(t, []string{RuleSelf}, rules(Check("codeforge allow merge", false)))
	for _, cmd := range []string{
		"git add assets/agents/codeforge-ticket.md",
		"git diff -- src/guard/gh.go",
		"git commit -m 'Handle gh pr merge errors'",
		"git -c user.name=me commit -m x",
	} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}
}

func TestCheckTool_MessagesAreNotCommands(t *testing.T) {
	for _, tool := range []string{"final_response", "respond_to_steering", "memory", "link"} {
		got := CheckTool(input(t, tool, map[string]any{"message": "run gh pr merge 1, git push --force, terraform apply"}), false)
		require.Empty(t, rules(got), tool)
	}
}
