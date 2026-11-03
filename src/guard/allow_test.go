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
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}
}

func TestCheck_GitIsReadOnlyInReview(t *testing.T) {
	for _, cmd := range []string{"git push origin main", "git commit -m x", "git checkout main", "git -c core.pager=sh log"} {
		require.Equal(t, []string{RuleReadOnly}, rules(Check(cmd, true)), cmd)
	}
	require.Empty(t, rules(Check("git log --oneline", true)))
}

func TestCheck_MergeCommentAndInfraNeedNoApproval(t *testing.T) {
	for _, cmd := range []string{
		"gh pr merge 1", "gh pr merge 1 --squash --delete-branch", "gh pr comment 1 -b x", "gh pr review 1 --approve",
		"gh pr close 1", "gh pr edit 1 --title x", "gh -R owner/b pr merge 1", "gh pr merge https://github.com/o/b/pull/1",
		"gh api repos/{owner}/{repo}/issues/1/comments -f body=x", "gh api -X PUT repos/{owner}/{repo}/pulls/1/merge",
		"gh api repos/o/b/pulls/1/comments/2/replies -f body=x", "gh api repos/{owner}/{repo}/pulls",
		"terraform apply", "tofu destroy -auto-approve", "terragrunt state rm x", "kubectl apply -f x.yaml",
		"kubectl rollout restart deploy/x", `sh -c 'terraform apply'`, `python3 -c 'import os; os.system("kubectl delete pod x")'`,
	} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}
	for _, cmd := range []string{"gh pr merge 1", "gh pr comment 1 -b x", "gh api repos/{owner}/{repo}/issues/1/comments -f body=x", "terraform apply", "kubectl delete pod x"} {
		require.Equal(t, []string{RuleReadOnly}, rules(Check(cmd, true)), "review mode stays read-only: "+cmd)
	}
}

func TestCheck_OpeningPRsNeedsNoGate(t *testing.T) {
	for _, cmd := range []string{
		"gh pr create --fill", "gh pr ready 1", "gh pr create --head other --repo owner/x", "gh -Rowner/repo pr create --fill",
		"gh pr create -Hother", "echo x > a && gh pr create --fill", "/opt/homebrew/bin/gh pr create --fill",
		"gh api repos/{owner}/{repo}/pulls -f title=x -f head=b -f base=main",
		"gh api -X POST /repos/{owner}/{repo}/pulls/ --input body.json",
		"gh api graphql -f query='mutation{createPullRequest(input:{}){clientMutationId}}'",
		"gh api user/repos -f name=x", "gh api -if title=x repos/{owner}/{repo}/pulls",
	} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}
	for _, cmd := range []string{"gh pr create --fill", "gh api repos/{owner}/{repo}/pulls -f title=x"} {
		require.Equal(t, []string{RuleReadOnly}, rules(Check(cmd, true)), "review mode stays read-only: "+cmd)
	}
}

func TestCheck_SelfStillGuarded(t *testing.T) {
	for _, cmd := range []string{"codeforge trust", "codeforge init", "codeforge repair", "codeforge reinit", "codeforge learn correct x"} {
		require.Equal(t, []string{RuleSelf}, rules(Check(cmd, false)), cmd)
	}
	for _, cmd := range []string{"codeforge models", "codeforge models recommend", "codeforge gate", "codeforge learn list"} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}
	for _, cmd := range []string{"codeforge models apply", "codeforge models fill", "codeforge models set model x/y", "codeforge models nope"} {
		require.Equal(t, []string{RuleSelf}, rules(Check(cmd, false)), cmd)
	}
	for _, cmd := range []string{
		"git add assets/agents/codeforge-ticket.md",
		"git diff -- src/guard/gh.go",
		"git commit -m 'Handle gh pr merge errors'",
		"git -c user.name=me commit -m x",
		"git config user.email me@example.com",
	} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}

	for _, cmd := range []string{
		"git -c alias.deploy='!terraform apply -auto-approve' deploy",
		"git -c alias.m='!/opt/homebrew/bin/gh pr merge 1' m",
		"git config alias.x '!codeforge trust'",
		"git config --local core.hooksPath /tmp/hooks",
		"git -c core.pager='sh ./x.sh' log",
		"git -ccore.sshCommand=x fetch",
		"git --config-env=core.pager=X log",
		"git config hook.x.command 'gh pr merge 1'",
		"git config set alias.deploy '!terraform apply -auto-approve'",
		"git config --add alias.x '!sh x.sh'",
		"git config --edit",
		"git config --file .git/config alias.x '!x'",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.deploy GIT_CONFIG_VALUE_0='!./deploy.sh' git deploy",
		"export GIT_CONFIG_PARAMETERS=x",
		"env GIT_SSH_COMMAND=./x git fetch",
		"git config core.hooksPath get",
		"git ls-remote --upload-pack='terraform apply -auto-approve; #' .",
		"git rebase -x 'gh pr merge 1' main",
		"git push --receive-pack=./x origin",
		"git difftool --extcmd=./x",
		"git rebase -x'terraform apply -auto-approve' HEAD~1",
		"git -C . rebase -x 'gh pr merge 1' main",
		"git submodule foreach 'terraform apply -auto-approve'",
		"git bisect run ./x.sh",
	} {
		require.Equal(t, []string{RuleOpaque}, rules(Check(cmd, false)), cmd)
	}
	for _, cmd := range []string{
		"git config get user.email", "git config --list", "git config set user.email me@x.com", "git config alias.co",
		"git push -u origin main", "git add -u", "git commit -m 'use -x flags'",
	} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}

	for _, cmd := range []string{"script -q /dev/null codeforge trust", "expect drive.exp", "tmux new -d 'codeforge trust'"} {
		require.Equal(t, []string{RuleSelf}, rules(Check(cmd, false)), cmd)
	}
	require.Equal(t, []string{RuleOpaque}, rules(Check(`python3 -c 'import pty; pty.spawn(["codeforge","trust"])'`, false)))
}

func TestCheckTool_MessagesAreNotCommands(t *testing.T) {
	for _, tool := range []string{"final_response", "respond_to_steering", "memory", "link"} {
		got := CheckTool(input(t, tool, map[string]any{"message": "run gh pr merge 1, git push --force, terraform apply"}), false)
		require.Empty(t, rules(got), tool)
	}
}
