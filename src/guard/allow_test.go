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
		"git -c user.email=me@example.com push",
		"git config --local pull.rebase true",
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
		"git -c alias.co=checkout co main",
		"git config user.email me@example.com",
	} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}

	for _, cmd := range []string{
		"git -c alias.deploy='!terraform apply -auto-approve' deploy",
		"git -c alias.m='!/opt/homebrew/bin/gh pr merge 1' m",
		"git config alias.x '!codeforge allow merge'",
		"git config --local core.hooksPath /tmp/hooks",
		"git -c core.pager='sh ./x.sh' log",
		"git -ccore.sshCommand=x fetch",
		"git --config-env=core.pager=X log",
		"git config hook.x.command 'gh pr merge 1'",
	} {
		require.Equal(t, []string{RuleOpaque}, rules(Check(cmd, false)), cmd)
	}

	for _, cmd := range []string{"script -q /dev/null codeforge allow merge", "expect drive.exp", "tmux new -d 'codeforge allow infra'"} {
		require.Equal(t, []string{RuleSelf}, rules(Check(cmd, false)), cmd)
	}
	require.Equal(t, []string{RuleOpaque}, rules(Check(`python3 -c 'import pty; pty.spawn(["codeforge","allow","merge"])'`, false)))
}

func TestCheckTool_MessagesAreNotCommands(t *testing.T) {
	for _, tool := range []string{"final_response", "respond_to_steering", "memory", "link"} {
		got := CheckTool(input(t, tool, map[string]any{"message": "run gh pr merge 1, git push --force, terraform apply"}), false)
		require.Empty(t, rules(got), tool)
	}
}
