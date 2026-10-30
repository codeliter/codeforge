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
	for _, args := range []map[string]any{
		{"action": "push", "flags": []string{"--force"}},
		{"action": "push"},
		{"action": "stage"},
		{"action": "stash", "sub_action": "push"},
	} {
		require.Empty(t, rules(CheckTool(input(t, "Git", args), false)), "git actions are allowed")
	}

	got := CheckTool(input(t, "Bash", map[string]any{"command": "gh pr merge 3"}), false)
	require.Equal(t, []string{RuleMerge}, rules(got))

	got = CheckTool(input(t, "code_script", map[string]any{"code": `await api.run("git push --force")`}), false)
	require.Empty(t, rules(got))

	got = CheckTool(input(t, "code_script", map[string]any{"code": `await api.run(['gh','pr','merge'].join(' '))`}), false)
	require.Equal(t, []string{RuleOpaque}, rules(got), "computed command")

	got = CheckTool(input(t, "code_script", map[string]any{"code": "const b='x'; await api.run(`gh pr merge ${b}`)"}), false)
	require.Equal(t, []string{RuleOpaque}, rules(got), "template with substitution")

	got = CheckTool(input(t, "code_script", map[string]any{"code": `await api.tool('shell', {command: "gh pr merge 1"})`}), false)
	require.Equal(t, []string{RuleMerge}, rules(got))

	got = CheckTool(input(t, "code_script", map[string]any{"code": `const r = await api.run('go test ./...'); return r`}), false)
	require.Empty(t, rules(got))

	got = CheckTool(input(t, "edit_file", map[string]any{"path": "README.md", "newString": "run gh pr merge to break things"}), false)
	require.Empty(t, rules(got), "text that mentions a command is not a command")

	got = CheckTool(input(t, "edit_file", map[string]any{"path": "a.go"}), true)
	require.Equal(t, []string{RuleReadOnly}, rules(got))

	got = CheckTool(input(t, "read", map[string]any{"path": "a.go"}), true)
	require.Empty(t, rules(got))
}

func TestCheck_InterpreterHidesCommand(t *testing.T) {
	for _, cmd := range []string{
		`python3 -c 'import os; os.system("gh pr merge 1")'`,
		`node -e 'require("child_process").execSync("gh pr merge 1")'`,
	} {
		require.Equal(t, []string{RuleOpaque}, rules(Check(cmd, false)), cmd)
	}
	require.Empty(t, rules(Check(`python3 -c 'import subprocess; subprocess.run(["git","push","--force"])'`, false)), "git is allowed")
	require.Empty(t, rules(Check(`python3 -c 'print(1+1)'`, false)))
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
	write("publish.sh", "#!/bin/sh\ngh pr merge 1\n")
	write("allow.sh", "codeforge allow merge\n")
	write("build.sh", "#!/bin/sh\ngo build ./...\ngit push\n")
	write("bin", "\x00\x01binary")

	for _, cmd := range []string{"./publish.sh", "bash ./publish.sh", "sh allow.sh", ". ./publish.sh", "./bin", "./missing.sh"} {
		require.Equal(t, []string{RuleScript}, rules(checkIn(cmd, false, dir)), cmd)
	}
	require.Empty(t, rules(checkIn("./build.sh", false, dir)), "a script that only uses git is fine")
	require.Empty(t, rules(checkIn("go test ./...", false, dir)), "go test is not a script path")
}

func TestCheckScript_Structural(t *testing.T) {
	tests := []struct {
		code  string
		rules []string
	}{
		{`await api.run ("gh pr merge 1")`, []string{RuleMerge}},
		{"await api . run(\n 'gh pr merge 1')", []string{RuleMerge}},
		{`await api.run(/* c */ "gh pr merge 1")`, []string{RuleMerge}},
		{`await api.run("git push --force")`, nil},
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
