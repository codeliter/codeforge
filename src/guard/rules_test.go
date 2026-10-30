package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func rules(r Result) []string {
	var out []string
	for _, f := range r.Blocked {
		out = append(out, f.Rule)
	}
	return out
}

func TestCheck(t *testing.T) {
	tests := []struct {
		cmd   string
		rules []string
		gate  string
	}{
		{"git status", nil, ""},
		{"git push origin feat", nil, GatePush},
		{"cd repo && git push -u origin feat", nil, GatePush},
		{"git push --force-with-lease", nil, GatePush},
		{"git push --force", []string{RuleForcePush}, ""},
		{"git push -fu origin feat", []string{RuleForcePush}, ""},
		{"git push origin +feat", []string{RuleForcePush}, ""},
		{"git push origin --delete feat", []string{RuleDeleteBranch}, ""},
		{"git push origin :feat", []string{RuleDeleteBranch}, ""},
		{"git push -d origin main", []string{RuleDeleteBranch}, ""},
		{"git push -fd origin x", []string{RuleDeleteBranch}, ""},
		{"git -C /repo -c user.name=x push --no-verify", []string{RuleNoVerify, RulePushScope}, GatePush},
		{"git -C /repo push --no-verify", []string{RuleNoVerify}, GatePush},
		{"git commit -nm 'x'", []string{RuleNoVerify}, ""},
		{"git commit -m 'note: -n is fine in a message'", nil, ""},
		{"git commit -m -n", nil, ""},
		{`git commit -m "use --no-verify carefully"`, nil, ""},
		{"git commit -am 'x'", []string{RuleWholeTree}, ""},
		{"git add -A", []string{RuleWholeTree}, ""},
		{"git add src/a.go", nil, ""},
		{"git stash", []string{RuleWholeTree}, ""},
		{"git stash list", nil, ""},
		{"git reset --hard HEAD~1", []string{RuleWholeTree}, ""},
		{"git reset HEAD a.go", nil, ""},
		{"git clean -fd", []string{RuleWholeTree}, ""},
		{"git checkout -- .", []string{RuleWholeTree}, ""},
		{"git checkout -b feat", nil, ""},
		{"git branch -D old", []string{RuleDeleteBranch}, ""},
		{"gh pr create --fill", nil, GateOpenPR},
		{"gh pr merge 12 --squash", []string{RuleMerge}, ""},
		{"gh pr review 12 --approve", []string{RulePRComment}, ""},
		{"gh pr comment 12 -b hi", []string{RulePRComment}, ""},
		{"gh pr view 12", nil, ""},
		{"gh api -X POST repos/{owner}/{repo}/pulls/1/comments", []string{RulePRComment}, ""},
		{"gh api -X POST repos/o/r/pulls/1/comments", []string{RulePushScope}, ""},
		{"gh api repos/o/r/pulls/1/comments", nil, ""},
		{"gh api --method=POST repos/{owner}/{repo}/issues/1/comments", []string{RulePRComment}, ""},
		{"gh api -XPOST repos/{owner}/{repo}/issues/1/comments", []string{RulePRComment}, ""},
		{"gh api repos/{owner}/{repo}/issues/1/comments -f body=hi", []string{RulePRComment}, ""},
		{"gh api repos/{owner}/{repo}/issues/1/comments --raw-field=body=hi", []string{RulePRComment}, ""},
		{"gh api repos/{owner}/{repo}/issues/1/comments -fbody=hi", []string{RulePRComment}, ""},
		{"gh api repos/{owner}/{repo}/issues/1/comments -Fbody=hi", []string{RulePRComment}, ""},
		{"gh api -X GET search/issues -f q=is:pr", nil, ""},
		{"kubectl rollout status deploy/x", nil, ""},
		{"kubectl rollout restart deploy/x", []string{RuleInfra}, ""},
		{"terraform plan", nil, ""},
		{"terraform apply -auto-approve", []string{RuleInfra}, ""},
		{"AWS_PROFILE=prod terraform destroy", []string{RuleInfra}, ""},
		{"kubectl get pods", nil, ""},
		{"kubectl delete pod x", []string{RuleInfra}, ""},
		{"echo hi | /usr/bin/git push", nil, GatePush},
		{"git push 2>&1 | tee log", nil, GatePush},
		{"git push &>/dev/null", nil, GatePush},
		{"sleep 1 & git push --force", []string{RuleForcePush}, ""},
		{`sh -c "git push --force"`, []string{RuleForcePush}, ""},
		{`bash -c 'cd x && git push'`, nil, GatePush},
		{`bash -lc 'git push -f'`, []string{RuleForcePush}, ""},
		{"codeforge allow force-push", []string{RuleSelf}, ""},
		{"codeforge gate", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := Check(tt.cmd, false)
			require.Equal(t, tt.rules, rules(got))
			require.Equal(t, tt.gate, got.Gate)
		})
	}
}

func TestCheck_ReadOnly(t *testing.T) {
	blocked := []string{
		"git commit -m x", "git checkout main", "rm -rf build", "sed -i s/a/b/ f.go",
		"echo x > notes.md", "cat a >> b", "git branch new", "mkdir x",
		`python3 -c 'open("README.md","w").write("x")'`, "node -e 'require(\"fs\").rmSync(\"a\")'",
		"sed -n 'w victim.txt' source.txt", "awk '{print > \"x\"}' a",
		"./deploy.sh", "unknowncmd --flag", "find . -delete", "gofmt -w .", "go build -o bin/x .",
		"echo hi 2 >out.txt", "gh pr comment 1 -b x", "make install", "go test ./...",
	}
	for _, cmd := range blocked {
		require.Equal(t, []string{RuleReadOnly}, rules(Check(cmd, true)), cmd)
	}

	allowed := []string{
		"git diff main", "git log --oneline", "git show HEAD:a.go", "rg foo 2>/dev/null",
		"go vet ./... > /dev/null", "ls -la", "git branch", "cat a.go 2>&1",
		`echo "a > b"`, "go vet ./...", "gh pr view 3", "git branch --show-current",
	}
	for _, cmd := range allowed {
		require.Empty(t, rules(Check(cmd, true)), cmd)
	}
}

func input(t *testing.T, tool string, args any) Input {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return Input{Event: "PreToolUse", Tool: tool, Args: raw}
}

func TestCheckTool(t *testing.T) {
	got := CheckTool(input(t, "Git", map[string]any{"action": "push", "flags": []string{"--force"}}), false)
	require.Equal(t, []string{RuleForcePush}, rules(got))

	got = CheckTool(input(t, "Git", map[string]any{"action": "push"}), false)
	require.Equal(t, GatePush, got.Gate)

	got = CheckTool(input(t, "Git", map[string]any{"action": "stage"}), false)
	require.Equal(t, []string{RuleWholeTree}, rules(got))

	got = CheckTool(input(t, "Git", map[string]any{"action": "stage", "files": []string{"a.go"}}), false)
	require.Empty(t, rules(got))

	got = CheckTool(input(t, "Git", map[string]any{"action": "stash", "sub_action": "push"}), false)
	require.Equal(t, []string{RuleWholeTree}, rules(got))

	got = CheckTool(input(t, "Bash", map[string]any{"command": "gh pr merge 3"}), false)
	require.Equal(t, []string{RuleMerge}, rules(got))

	got = CheckTool(input(t, "code_script", map[string]any{"code": `await api.run("git push --force")`}), false)
	require.Equal(t, []string{RuleForcePush}, rules(got))

	got = CheckTool(input(t, "code_script", map[string]any{"code": `await api.run(['git','push','--force'].join(' '))`}), false)
	require.Equal(t, []string{RuleOpaque}, rules(got), "computed command")

	got = CheckTool(input(t, "code_script", map[string]any{"code": "const b='x'; await api.run(`git push origin ${b}`)"}), false)
	require.Equal(t, []string{RuleOpaque}, rules(got), "template with substitution")

	got = CheckTool(input(t, "code_script", map[string]any{"code": `await api.run("git " + "push --force")`}), false)
	require.Equal(t, []string{RuleOpaque}, rules(got), "concatenation")

	got = CheckTool(input(t, "code_script", map[string]any{"code": `await api.tool('shell', {command: "gh pr merge 1"})`}), false)
	require.Equal(t, []string{RuleMerge}, rules(got))

	got = CheckTool(input(t, "code_script", map[string]any{"code": `const r = await api.run('go test ./...'); return r`}), false)
	require.Empty(t, rules(got))

	got = CheckTool(input(t, "code_script", map[string]any{"code": `return api.read('README.md')`}), false)
	require.Empty(t, rules(got))

	got = CheckTool(input(t, "edit_file", map[string]any{"path": "README.md", "newString": "run git push --force to break things"}), false)
	require.Empty(t, rules(got), "text that mentions git is not a command")

	got = CheckTool(input(t, "edit_file", map[string]any{"path": "a.go"}), true)
	require.Equal(t, []string{RuleReadOnly}, rules(got))

	got = CheckTool(input(t, "read", map[string]any{"path": "a.go"}), true)
	require.Empty(t, rules(got))
}

func TestCheck_InterpreterHidesCommand(t *testing.T) {
	for _, cmd := range []string{
		`python3 -c 'import os; os.system("git push --force")'`,
		`python3 -c 'import subprocess; subprocess.run(["git","push","--force"])'`,
		`node -e 'require("child_process").execSync("gh pr merge 1")'`,
	} {
		require.Equal(t, []string{RuleOpaque}, rules(Check(cmd, false)), cmd)
	}
	require.Empty(t, rules(Check(`python3 -c 'print(1+1)'`, false)))
}

func TestCheck_NestedShellKeepsPushMetadata(t *testing.T) {
	r := Check(`sh -c 'git push origin other'`, false)
	require.Equal(t, []string{"other"}, r.PushRefs)

	r = Check(`bash -c 'git commit -m x && git push'`, false)
	require.True(t, r.OtherWrites)
}

func TestCheck_PushConfigOverride(t *testing.T) {
	require.Equal(t, []string{RulePushScope}, rules(Check("git -c push.default=matching push origin", false)))
	require.Empty(t, rules(Check("git -C sub push origin", false)))
}

func TestCheck_PushRefsAndMixedWrites(t *testing.T) {
	require.Equal(t, []string{"--all"}, Check("git push --all origin", false).PushRefs)
	require.Equal(t, []string{"other"}, Check("git push origin other", false).PushRefs)
	require.Empty(t, Check("git push -u origin", false).PushRefs)
	require.Equal(t, []string{"main"}, Check("git push origin main 2>&1 | tail -1", false).PushRefs, "a redirect is not a refspec")
	require.Equal(t, []string{"main"}, Check("git push origin main 2>&1", false).PushRefs)
	require.Equal(t, []string{"main"}, Check("git push origin main > /tmp/log", false).PushRefs)
	require.Equal(t, []string{"main"}, Check("git push origin main 2>& 1", false).PushRefs, "standalone operator and its target")
	require.Equal(t, []string{">topic"}, Check("git push origin '>topic'", false).PushRefs, "a quoted refspec is an argument")
	require.Equal(t, []string{">topic"}, Check(`git push origin \>topic`, false).PushRefs, "an escaped refspec is an argument")
	require.Contains(t, rules(Check("git >/dev/null -c core.hooksPath=/dev/null -c push.default=matching push origin", false)), RuleSelf,
		"a redirect among git's options cannot hide a hook override")
	require.Contains(t, rules(Check("git 2>&1 -c push.default=matching push origin", false)), RulePushScope)
	require.Equal(t, []string{RuleNoVerify}, rules(Check(`git -C '2'>/dev/null push --no-verify`, false)),
		"a quoted 2 is the -C value, not a file descriptor, so --no-verify is still seen")
	require.Equal(t, []string{"2"}, Check(`git -C '2'>/dev/null push --no-verify`, false).Dirs)
	require.Equal(t, []string{"/b"}, Check(`git -C /b>/dev/null push`, false).Dirs)
	require.True(t, Check("> tracked.go", false).OtherWrites, "a bare redirection truncates a file")
	require.Equal(t, []string{RuleReadOnly}, rules(Check("> tracked.go", true)))
	require.Empty(t, rules(Check("ls 2>&1 >/dev/null", true)))
	require.Empty(t, rules(Check("ls 2>& 1", true)), "spaced descriptor duplication")
	require.Empty(t, rules(Check("ls 2>&-", true)), "closing a descriptor")
	require.Empty(t, rules(Check("ls 3>&1-", true)), "moving a descriptor")
	require.Equal(t, []string{RuleReadOnly}, rules(Check("echo x >&1abc", true)), ">&1abc writes the file 1abc")
	require.Equal(t, []string{RuleReadOnly}, rules(Check("echo x &> out.log", true)))
	require.Contains(t, rules(Check("git 2>&1>status -c core.hooksPath=/dev/null push origin main", false)), RuleSelf,
		"chained redirects cannot turn a push into git status")
	require.Equal(t, GatePush, Check("git 2>&1>status push origin main", false).Gate)
	require.Equal(t, []string{"main"}, Check("git push origin main 2>&1>/dev/null", false).PushRefs)
	require.Equal(t, []string{RuleNoVerify}, rules(Check("nice -n 2&>/dev/null git push --no-verify", false)),
		"&> takes no descriptor prefix, so 2 stays nice's priority")
	require.Equal(t, GatePush, Check("nice -n 2&>>/tmp/log git push", false).Gate)
	require.False(t, Check("git push origin main 2>&1 | tail -1", false).OtherWrites, "tail after a push is not a write")
	require.True(t, Check("echo x > a.go && git add a.go && git commit -m x && git push", false).OtherWrites)
	require.False(t, Check("git push origin feat", false).OtherWrites)
}

func TestCheck_Dirs(t *testing.T) {
	require.Equal(t, []string{"/other"}, Check("git -C /other push", false).Dirs)
	require.Equal(t, []string{"../b"}, Check("cd ../b && git push", false).Dirs)
	require.Equal(t, []string{"../b/sub"}, Check("cd ../b && git -C sub push", false).Dirs)
	require.Equal(t, []string{""}, Check("git push", false).Dirs)
}

func TestCheck_Wrappers(t *testing.T) {
	tests := []struct {
		cmd   string
		rules []string
		gate  string
	}{
		{"env -u UNUSED git push --force", []string{RuleForcePush}, ""},
		{"sudo -u root git push --force", []string{RuleForcePush}, ""},
		{"command -p git push --force", []string{RuleForcePush}, ""},
		{"timeout 30 gh pr merge 1", []string{RuleMerge}, ""},
		{"nice -n 5 git push", nil, GatePush},
		{"env -C /other git push", []string{RuleOpaque}, ""},
		{"env -S 'git push --force'", []string{RuleOpaque}, ""},
		{"echo main | xargs git push origin", []string{RuleOpaque}, ""},
		{"GIT_DIR=/other/.git git push", []string{RuleOpaque}, ""},
		{"export GIT_WORK_TREE=/other; git push", []string{RuleOpaque}, GatePush},
		{"git --git-dir=/B/.git --work-tree=/B push origin HEAD", []string{RuleOpaque}, ""},
		{"git --work-tree /B push", []string{RuleOpaque}, ""},
		{"FOO=1 git status", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := Check(tt.cmd, false)
			require.Equal(t, tt.rules, rules(got))
			require.Equal(t, tt.gate, got.Gate)
		})
	}
}

func TestCheck_ReadOnlyArgumentForms(t *testing.T) {
	blocked := []string{
		"git remote remove origin", "git remote add x url", "sort -o victim.txt source.txt",
		"diff --output=x a b", "git -c core.pager=sh log", "git symbolic-ref HEAD refs/heads/x",
		"git log --output=x", "go test ./...", "make test", "npm test", "env -C /x ls",
		"go env -w GOPROXY=https://example.com", "go vet -vettool=./helper ./...", "go vet -toolexec x ./...",
	}
	for _, cmd := range blocked {
		require.Equal(t, []string{RuleReadOnly}, rules(Check(cmd, true)), cmd)
	}

	allowed := []string{"git remote -v", "git remote get-url origin", "sort a.txt", "git symbolic-ref HEAD", "go vet ./...", "go env GOPATH"}
	for _, cmd := range allowed {
		require.Empty(t, rules(Check(cmd, true)), cmd)
	}
}

func TestCheck_GitAliasesAndOverrides(t *testing.T) {
	require.Equal(t, []string{RuleSelf}, rules(Check("git -c alias.publish='push --force' publish", false)))
	require.Equal(t, []string{RuleOpaque}, rules(Check("git publish", false)))
	require.Equal(t, []string{RuleOpaque}, rules(Check("git -C . --git-dir=/other/.git --work-tree=/other push origin HEAD", false)))
	require.Equal(t, []string{RuleReadOnly}, rules(Check("git symbolic-ref --delete refs/remotes/origin/HEAD", true)))
	require.Equal(t, []string{RuleReadOnly}, rules(Check("git symbolic-ref -d refs/remotes/origin/HEAD", true)))
}

func TestCheckTool_FileContentIsNotACommand(t *testing.T) {
	got := CheckTool(input(t, "edit_file", map[string]any{"path": "rules_test.go", "newString": `{"git push --force", nil}`}), false)
	require.Empty(t, rules(got))
}

func TestCheck_PRTarget(t *testing.T) {
	require.Equal(t, []string{RulePushScope}, rules(Check("gh pr create --head other --repo owner/x", false)))
	require.Equal(t, []string{RulePushScope}, rules(Check("gh -R owner/x pr create --fill", false)))
	require.Empty(t, rules(Check("gh pr create --fill --base main", false)))
}

func TestCheck_ScriptsAreInspected(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700))
	}
	write("publish.sh", "#!/bin/sh\ngit push --force\n")
	write("allow.sh", "codeforge allow push\n")
	write("build.sh", "#!/bin/sh\ngo build ./...\n")
	write("bin", "\x00\x01binary")

	for _, cmd := range []string{"./publish.sh", "bash ./publish.sh", "sh allow.sh", ". ./publish.sh", "./bin", "./missing.sh"} {
		require.Equal(t, []string{RuleScript}, rules(checkIn(cmd, false, dir)), cmd)
	}
	require.Empty(t, rules(checkIn("./build.sh", false, dir)))
	require.Empty(t, rules(checkIn("go test ./...", false, dir)), "go test is not a script path")
}

func TestCheckScript_Structural(t *testing.T) {
	tests := []struct {
		code  string
		rules []string
	}{
		{`await api.run ("git push --force")`, []string{RuleForcePush}},
		{"await api . run(\n 'gh pr merge 1')", []string{RuleMerge}},
		{`await api.run(/* c */ "git push --force")`, []string{RuleForcePush}},
		{`const { run } = api; await run("git push --force")`, []string{RuleOpaque}},
		{`const r = api.run; await r("git push --force")`, []string{RuleOpaque}},
		{`await api["run"]("git push --force")`, []string{RuleOpaque}},
		{`helper(api)`, []string{RuleOpaque}},
		{`await api.tool(name, {command: "git push --force"})`, []string{RuleOpaque}},
		{`await api.tool('read', {files: [{path: 'a'}]})`, nil},
		{`// api.run("git push --force") in a comment
return api.read("a.go")`, nil},
		{`return api.grep("go test -run TestX")`, nil},
		{"return `${await api.run(\"git push --no-verify\")}`", []string{RuleOpaque}},
		{"const s = `plain ${x}`; return s", []string{RuleOpaque}},
		{"return `no interpolation here`", nil},
		{`await api.run("\u0067it push --no-verify")`, []string{RuleOpaque}},
		{`await api.run("g\x69t push --no-verify")`, []string{RuleOpaque}},
		{`await api.tool('shell', {command: "git status", command: "git push --no-verify"})`, []string{RuleOpaque}},
		{`await api.tool('shell', {...opts, command: "git status"})`, []string{RuleOpaque}},
		{`await api.tool('shell', {["command"]: "git push --no-verify"})`, []string{RuleOpaque}},
		{`await api.tool('shell', {command: 'git status', ['command'] : 'git push --no-verify'})`, []string{RuleOpaque}},
		{`await api.tool('shell', {command: 'git status', \u0063ommand: 'git push --no-verify'})`, []string{RuleOpaque}},
		{`await api.tool('shell', {command: "gh pr view 1", timeout: 5})`, []string{RuleOpaque}},
		{`await api.tool('shell', {command: "git status"} && {command: "git push --no-verify"})`, []string{RuleOpaque}},
		{`await api.tool('shell', {command: "git status"}.command)`, []string{RuleOpaque}},
		{`await api.tool('shell', {command: "git log --format='%h %s'"})`, nil},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			require.Equal(t, tt.rules, rules(checkScript(tt.code, false)))
		})
	}
}

func TestCheck_GHAttachedRepoFlag(t *testing.T) {
	require.Equal(t, []string{RulePushScope}, rules(Check("gh -Rowner/repo pr merge 1", false)))
	require.Equal(t, []string{RulePushScope}, rules(Check("gh pr merge https://github.com/o/b/pull/1", false)))
	for _, cmd := range []string{"gh pr merge 1 --repo owner/B", "gh pr comment 1 -R owner/B -b x", "gh pr review 1 --repo=owner/B", "gh pr close 1 -Rowner/B"} {
		require.Equal(t, []string{RulePushScope}, rules(Check(cmd, false)), cmd)
	}
	require.Equal(t, []string{RulePushScope}, rules(Check("gh -Rowner/repo api repos/{owner}/{repo}/issues/1/comments -fbody=x", false)))
	require.Equal(t, []string{RulePushScope}, rules(Check("gh -Rowner/repo pr create --fill", false)))
	require.Equal(t, []string{RulePushScope}, rules(Check("gh pr create -Hother", false)))
}

func TestCheck_GitConfigProtectsEnforcement(t *testing.T) {
	for _, cmd := range []string{
		"git config --local hook.codeforge.command true", "git config core.hooksPath /dev/null",
		"git config --unset hook.codeforge.event", "git config --remove-section hook.codeforge", "git config alias.p push",
	} {
		require.Equal(t, []string{RuleSelf}, rules(Check(cmd, false)), cmd)
	}
	require.Empty(t, rules(Check("git config user.name x", false)))
}

func TestCheck_ConfigOverrideCannotDisablePrePush(t *testing.T) {
	for _, cmd := range []string{
		"git -c hook.codeforge.command=true push --force origin HEAD",
		"git -c core.hooksPath=/dev/null push -d origin x",
		"git --config-env=core.hooksPath=X push",
		"git -c alias.st=status st",
		"git -chook.codeforge.command=true push --force origin HEAD",
		"git -ccore.hooksPath=/dev/null push -d origin x",
	} {
		require.Contains(t, rules(Check(cmd, false)), RuleSelf, cmd)
	}
}

func TestCheck_NestedResultsKeepDeferral(t *testing.T) {
	require.True(t, checkScript(`await api.run('gh pr merge 1')`, false).Deferred[RuleMerge])
	require.True(t, Check(`sh -c 'gh pr merge 1'`, false).Deferred[RuleMerge])
	require.True(t, Check(`sh -c '/opt/homebrew/bin/gh pr merge 1'`, false).DirectGH)
}

func TestAPIWrites_ValuesAreNotFlags(t *testing.T) {
	require.True(t, apiWrites([]string{"-X", "POST", "repos/{owner}/{repo}/issues/1/comments", "-f", "body=x", "-f", "--method=GET"}))
	require.True(t, apiWrites([]string{"repos/{owner}/{repo}/issues/1/comments", "-f", "-XGET"}))
	require.True(t, apiWrites([]string{"repos/{owner}/{repo}/issues/1/comments", "--field", "--method=GET"}))
	require.False(t, apiWrites([]string{"-X", "GET", "search/issues", "-f", "q=x"}))
	require.False(t, apiWrites([]string{"repos/{owner}/{repo}/pulls", "-H", "Accept: x"}))
	require.Equal(t, []string{RuleReadOnly}, rules(Check(`gh api -X POST repos/{owner}/{repo}/issues/1/comments -f body=x -f '--method=GET'`, true)))
}

func TestGraphQLRead(t *testing.T) {
	require.Empty(t, rules(Check(`gh api graphql -f query='query{viewer{login}}'`, false)))
	require.Empty(t, rules(Check(`gh api graphql -f query='{ viewer { login } }' -F n=3`, false)))
	require.Equal(t, []string{RulePushScope}, rules(Check(`gh api graphql -f query='mutation{addComment(input:{}){clientMutationId}}'`, false)))
	require.Equal(t, []string{RulePushScope}, rules(Check(`gh api graphql -f query='query{a} mutation{b}'`, false)))
	require.Equal(t, []string{RulePushScope}, rules(Check(`gh api graphql -F 'query={owner}{addComment(input:{subjectId:"ID",body:"x"}){commentEdge{node{id}}}}'`, false)), "-F expands placeholders")
	require.Equal(t, []string{RulePushScope}, rules(Check(`gh api graphql -Fquery=@q.graphql`, false)))
	require.Equal(t, []string{RulePushScope}, rules(Check(`gh api -it graphql repos/{owner}/{repo}/issues/1/comments -f body=x -f 'query={viewer{login}}'`, false)), "clustered flags")
	require.Equal(t, []string{RulePushScope}, rules(Check(`gh api --preview x graphql -f 'query={viewer{login}}'`, false)), "unknown flag")
}
