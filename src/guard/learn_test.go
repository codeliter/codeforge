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
		"codeforge learn correct use table tests",
		"codeforge learn correct --session s1 'use table tests'",
		"codeforge learn outcome -- go test ./...",
		"codeforge learn outcome -- gh pr create --fill",
		"codeforge learn revert 3",
		"codeforge learn accept 1",
		"codeforge learn dismiss 1",
		"codeforge learn forget R1",
		"codeforge learn approve 3",
		"/opt/homebrew/bin/codeforge learn correct x",
		"env codeforge learn correct x",
		"go test ./... && codeforge learn outcome -- sh -c 'gh pr merge 1'",
	} {
		require.Equal(t, []string{RuleLearning}, rules(Check(cmd, false)), cmd)
	}
	got := CheckTool(input(t, "code_script", map[string]any{"code": `await api.run("codeforge learn correct x")`}), false)
	require.Equal(t, []string{RuleLearning}, rules(got))
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
	for _, code := range []string{`const c = "codeforge observe"; await api.run(c)`, `await api.run("codeforge learn correct x")`} {
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
