package src

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLearnRules_EndToEnd runs corrections through the real binary: the prompt hook records them across sessions,
// the next launch adopts the repeated correction as a rule by itself (vector grouping), a rule stays in the
// directory it was learned in, and forgetting it is permanent.
func TestLearnRules_EndToEnd(t *testing.T) {
	e := newE2E(t)
	e.runWith(e.repo, "prompt", "always use table-driven tests", "chat", "--session-id", "rule-session-1")
	out := e.runWith(e.repo, "prompt", "always use table-driven tests", "chat", "--session-id", "rule-session-2")
	require.NotContains(t, out, "learned a rule", "two corrections exist only after session 2's prompt is recorded")

	// The next launch is the first to see both corrections: it adopts them with no accept step.
	out = e.runWith(e.repo, "prompt", "add a test for the parser", "chat", "--session-id", "rule-session-3")
	require.Contains(t, out, "learned a rule from your corrections")
	require.Contains(t, out, "undo: codeforge learn forget R1")
	require.Contains(t, e.launches()[2], "- always use table-driven tests")

	rules := e.run(e.repo, "", "learn", "rules")
	require.Contains(t, rules, "R1")
	require.Contains(t, rules, "always use table-driven tests")
	require.NotContains(t, rules, "parser", "a prompt that is not a correction is never stored")
	require.Contains(t, e.run(e.repo, "", "learn", "proposals"), "No correction has been recorded", "an adopted group is no longer a proposal")

	out = e.run(e.repo, "", "chat", "--session-id", "rule-session-4")
	require.NotContains(t, out, "learned a rule", "adopting happens once")
	require.Contains(t, out, "using 1 learned rule(s)")

	out = e.run(filepath.Join(e.repo, "sub"), "", "chat", "--session-id", "rule-session-5")
	require.NotContains(t, out, "using 1 learned rule", "a rule applies from the directory it was learned in")

	e.run(e.repo, "", "learn", "forget", "R1")
	e.run(e.repo, "", "chat", "--session-id", "rule-session-6")
	require.NotContains(t, e.launches()[5], "always use table-driven tests")
	e.run(e.repo, "", "chat", "--session-id", "rule-session-7")
	require.NotContains(t, e.launches()[6], "always use table-driven tests", "a forgotten rule is not adopted again")
}
