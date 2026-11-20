package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheck_LearningWritesAreUserOnly(t *testing.T) {
	for _, cmd := range []string{"codeforge learn list", "codeforge learn list --all", "codeforge learn propose", "codeforge learn habits", "codeforge learn proposals", "codeforge learn rules"} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
	}
	for _, cmd := range []string{
		"codeforge learn",
		"codeforge learn correct --session s1 'use table tests'",
		"codeforge learn correct --session=s1 'use table tests'",
		"codeforge learn correct $s 'use table tests'",
		"codeforge learn correct --sessio? s1 'use table tests'",
		"codeforge learn correct $'--session' s1 x",
		"codeforge learn correct $\"--session\" s1 x",
		"codeforge learn correct \"$s\" s1 x",
		"codeforge learn correct ${s} s1 x",
		"codeforge learn correct $1 s1 x",
		"codeforge learn correct $@ s1 x",
		"s=--session; codeforge learn correct $=s s1 x",
		"s=--session; codeforge learn correct $==s s1 x",
		"s=--session; codeforge learn correct $^s s1 x",
		"s=--session; codeforge learn correct $^^s s1 x",
		"s=--session; codeforge learn correct $~s s1 x",
		"s=--session; codeforge learn correct \"$=s\" s1 x",
		"s=--session; codeforge learn correct \"$~s\" s1 x",
		"codeforge learn correct $[1+1] s1 x",
		"codeforge learn correct --sessio[n] s1 x",
		"codeforge learn correct --sess* s1 x",
		"codeforge learn outcome --sessio? s1 -- go test ./...",
		"codeforge lear? correct --session s1 x",
		"codeforge observ?",
		"codeforge learn outcome --session s1 -- go test ./...",
		"codeforge learn $w use table tests",
		"codeforge learn revert 3",
		"codeforge learn accept 1",
		"codeforge learn dismiss 1",
		"codeforge learn forget R1",
		"codeforge learn approve 3",
		"codeforge learn outcome -- codeforge learn accept P1",
		"codeforge learn outcome -- codeforge observe",
		"codeforge learn outcome -- sh -c 'codeforge learn forget R1'",
	} {
		require.Equal(t, []string{RuleLearning}, rules(Check(cmd, false)), cmd)
	}
	got := CheckTool(input(t, "code_script", map[string]any{"code": `await api.run("codeforge learn accept P1")`}), false)
	require.Equal(t, []string{RuleLearning}, rules(got))
}

// A correction with no session only ever becomes a proposal the user accepts, and an outcome is a check the agent
// could run anyway, so the agent may record both.
func TestCheck_TheAgentRecordsCorrectionsAndOutcomes(t *testing.T) {
	for _, cmd := range []string{
		"codeforge learn correct use table tests",
		"codeforge learn correct 'Always use gh stack for stacked PRs'",
		"codeforge learn correct 'Always quote $HOME and `cmd`'",
		"codeforge learn correct \"cost \\$5\"",
		"codeforge learn outcome -- go test -run '^TestFoo$'",
		"codeforge learn correct '$'",
		"codeforge learn correct $",
		"codeforge learn correct \"$\"",
		"codeforge learn correct \"costs 5 $ each\"",
		"codeforge learn outcome -- printf $",
		"codeforge learn outcome -- printf \"$\"",
		"codeforge learn outcome -- printf '$'",
		"codeforge learn correct 'is it *.go?'",
		"/opt/homebrew/bin/codeforge learn correct x",
		"env codeforge learn correct x",
		"codeforge learn outcome -- go test ./...",
		"codeforge learn outcome -- gh pr create --fill",
		"go test ./... && codeforge learn outcome -- make lint",
		"codeforge job add --while 'codeforge learn outcome -- make lint' -- make",
	} {
		require.Empty(t, rules(Check(cmd, false)), cmd)
		require.True(t, DecideOutsideReview(input(t, "Bash", map[string]any{"command": cmd})).Allow, cmd)
		require.Contains(t, rules(Check(cmd, true)), RuleReadOnly, "review mode records nothing: "+cmd)
	}
	got := CheckTool(input(t, "code_script", map[string]any{"code": `await api.run("codeforge learn correct x")`}), false)
	require.Empty(t, rules(got))

	for cmd, rule := range map[string]string{
		"codeforge learn outcome -- gh land 1":            RuleOpaque,
		"codeforge learn outcome -- $c test":              RuleOpaque,
		"codeforge learn outcome -- codeforge gate reset": RuleUserOnly,
	} {
		require.Equal(t, []string{rule}, rules(Check(cmd, false)), "the check is judged as if run directly: "+cmd)
	}
}

func TestCheck_LearningWritesThroughInterpretersAndScripts(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.sh"), []byte("#!/bin/sh\ncodeforge learn correct x\n"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ok.sh"), []byte("#!/bin/sh\necho hi\n"), 0o700))
	for _, cmd := range []string{
		`python3 -c 'import os; os.system("codeforge observe < payload.json")'`,
		`sh -c "codeforge learn correct x"`,
		"./x.sh", "bash x.sh",
	} {
		require.Equal(t, []string{RuleLearning}, rules(checkIn(cmd, false, dir)), cmd)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cont.sh"), []byte("#!/bin/sh\ncodeforge \\\n  observe < payload.json\n"), 0o700))
	for _, cmd := range []string{"./x.sh", "./cont.sh", "bash cont.sh", `python3 -c 'import os; os.system("codeforge observe")'`} {
		raw, err := json.Marshal(map[string]any{"command": cmd})
		require.NoError(t, err)
		require.False(t, DecideOutsideReview(Input{Tool: "Bash", Cwd: dir, Args: raw}).Allow, cmd)
	}
	for _, code := range []string{`const c = "codeforge observe"; await api.run(c)`, `await api.run("codeforge learn forget R1")`} {
		raw, err := json.Marshal(map[string]any{"code": code})
		require.NoError(t, err)
		require.False(t, DecideOutsideReview(Input{Tool: "code_script", Cwd: dir, Args: raw}).Allow, code)
	}
	for _, cmd := range []string{`python3 -c 'import os; os.system("codeforge " + "ob" + "serve < payload.json")'`, `sh -c 'c=codeforge; d=learn; $c $d correct x'`} {
		raw, err := json.Marshal(map[string]any{"command": cmd})
		require.NoError(t, err)
		require.False(t, DecideOutsideReview(Input{Tool: "Bash", Cwd: dir, Args: raw}).Allow, cmd)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spaced.sh"), []byte("codeforge"+strings.Repeat(" ", 300)+"observe\n"), 0o700))
	require.False(t, DecideOutsideReview(Input{Tool: "Bash", Cwd: dir, Args: []byte(`{"command":"./spaced.sh"}`)}).Allow, "long gap")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big.sh"), []byte("#!/bin/sh\n"+strings.Repeat("# padding\n", maxScriptBytes/len("# padding\n")+1)), 0o700))
	require.False(t, DecideOutsideReview(Input{Tool: "Bash", Cwd: dir, Args: []byte(`{"command":"./big.sh"}`)}).Allow, "oversized script")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big.bin"), append([]byte("\x7fELF\x00"), make([]byte, maxScriptBytes+1)...), 0o700))
	require.True(t, DecideOutsideReview(Input{Tool: "Bash", Cwd: dir, Args: []byte(`{"command":"./big.bin --version"}`)}).Allow, "large native binary")
	require.True(t, DecideOutsideReview(Input{Tool: "Bash", Cwd: dir, Args: []byte(`{"command":"./ok.sh"}`)}).Allow)
}

func TestLearnCorrection(t *testing.T) {
	for cmd, want := range map[string]string{
		"codeforge learn correct use table tests":           "use table tests",
		"codeforge learn correct 'Always use gh stack'":     "Always use gh stack",
		"/opt/homebrew/bin/codeforge learn correct \"a b\"": "a b",
	} {
		got, ok := LearnCorrection(cmd)
		require.True(t, ok, cmd)
		require.Equal(t, want, got, cmd)
	}
	for _, cmd := range []string{
		"codeforge learn correct --session s1 x", "codeforge learn correct --session=s1 x", "codeforge learn correct",
		"codeforge learn correct x && codeforge learn correct y", "echo codeforge learn correct x", "codeforge learn correct $x",
		"codeforge learn correct \"$(cat f)\"", "codeforge learn accept P1", "sh -c 'codeforge learn correct x'", "",
	} {
		_, ok := LearnCorrection(cmd)
		require.False(t, ok, cmd)
	}
	require.True(t, ShellTool("Bash"))
	require.False(t, ShellTool("Edit"))
}

func TestCheck_AgentCannotFeedTheObserver(t *testing.T) {
	for _, cmd := range []string{
		"codeforge observe",
		"echo '{}' | codeforge observe",
		"/opt/homebrew/bin/codeforge observe < payload.json",
		"env CODEFORGE_LAUNCH=1 codeforge observe",
	} {
		require.Equal(t, []string{RuleLearning}, rules(Check(cmd, false)), cmd)
	}
}
