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

func TestCheck_MergeCommentAndInfraNeedNoApproval(t *testing.T) {
	for _, cmd := range []string{
		"gh pr merge 1", "gh pr merge 1 --squash --delete-branch", "gh pr comment 1 -b x", "gh pr review 1 --approve",
		"gh pr close 1", "gh pr edit 1 --title x", "gh -R owner/b pr merge 1", "gh pr merge https://github.com/o/b/pull/1",
		"gh api repos/{owner}/{repo}/issues/1/comments -f body=x", "gh api -X PUT repos/{owner}/{repo}/pulls/1/merge",
		"gh api repos/o/b/pulls/1/comments/2/replies -f body=x", "gh api repos/{owner}/{repo}/pulls",
		"terraform apply", "tofu destroy -auto-approve", "terragrunt state rm x", "kubectl apply -f x.yaml",
		"kubectl rollout restart deploy/x", `sh -c 'terraform apply'`, `python3 -c 'import os; os.system("kubectl delete pod x")'`,
	} {
		got := Check(cmd, false)
		require.Empty(t, rules(got), cmd)
		require.Empty(t, got.Gate, cmd)
	}
	for _, cmd := range []string{"gh pr merge 1", "gh pr comment 1 -b x", "gh api repos/{owner}/{repo}/issues/1/comments -f body=x", "terraform apply", "kubectl delete pod x"} {
		require.Equal(t, []string{RuleReadOnly}, rules(Check(cmd, true)), "review mode stays read-only: "+cmd)
	}
}

func TestCheck_GHAPICannotOpenPRAroundGate(t *testing.T) {
	for _, cmd := range []string{
		"gh api repos/{owner}/{repo}/pulls -f title=x -f head=b -f base=main",
		"gh api repos/{owner}/{repo}/pulls -F title=x -F head=b",
		"gh api repos/{owner}/{repo}/pulls --input body.json",
		"gh api --method=POST repos/{owner}/{repo}/pulls",
		"gh api graphql --input mutation.json",
		"gh api -X POST /repos/{owner}/{repo}/pulls/ --input body.json",
		"gh api 'repos/{owner}/{repo}/pulls?x=1' -f title=x",
		"gh api repos/o/b/PULLS -f title=x",
		"gh api repos/{owner}/{repo}/x/../pulls -f title=x",
		"gh api repos/{owner}/{repo}//pulls -f title=x",
		"gh api repos/{owner}/{repo}/pull%73 -f title=x",
		"gh api repos/o%2Fb/pulls -f title=x",
		"gh api repos/{owner}/{repo}/../../b/pulls -f title=x",
		"gh api https://api.github.com/repos/o/b/pulls -f title=x",
		"gh api /graphql -f query='mutation{createPullRequest(input:{}){clientMutationId}}'",
		"gh api user/repos -f name=x",
	} {
		require.Equal(t, []string{RulePushScope}, rules(Check(cmd, false)), cmd)
	}
	for _, cmd := range []string{
		"gh api -iq repos/o/b/issues/1 repos/o/b/pulls -f title=x -f head=b",
		"gh api -if title=x -if head=b repos/{owner}/{repo}/pulls",
		"gh api -iXPOST repos/{owner}/{repo}/pulls",
		"gh api --preview x repos/{owner}/{repo}/pulls -f title=x",
		"gh api -- repos/{owner}/{repo}/pulls -f title=x",
		"gh api repos/{owner}/{repo}/%zz -f x=y",
		"gh api",
	} {
		require.Equal(t, []string{RuleOpaque}, rules(Check(cmd, false)), cmd)
	}
	for _, cmd := range []string{
		"gh api -XPOST repos/{owner}/{repo}/issues/1/comments -fbody=x",
		"gh api --method=POST repos/{owner}/{repo}/issues/1/comments --raw-field body=x",
		"gh api -i --paginate repos/{owner}/{repo}/pulls -q .[].number",
		"gh api --hostname ghe.example.com -H 'Accept: x' repos/{owner}/{repo}/pulls",
	} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}
}

func TestCheck_GateAndSelfStillGuarded(t *testing.T) {
	require.Equal(t, GateOpenPR, Check("gh pr create --fill", false).Gate)
	require.Equal(t, GateOpenPR, Check("gh pr ready 1", false).Gate)
	require.Equal(t, []string{RuleSelf}, rules(Check("codeforge allow open-pr", false)))
	for _, cmd := range []string{"codeforge models", "codeforge models recommend"} {
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
		"git config alias.x '!codeforge allow merge'",
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
