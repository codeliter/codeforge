//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkFromArgs(t *testing.T) {
	w, rest, err := workFromArgs([]string{"--ticket", "ABC-1", "--model", "a/b", "fix", "the", "race", "--headless"})
	require.NoError(t, err)
	require.Equal(t, "ABC-1", w.ticket)
	require.Equal(t, "fix the race", w.summary)
	require.Equal(t, []string{"--model", "a/b", "--headless"}, rest)
}

func TestWorkFromArgs_RejectsEffortAboveCeiling(t *testing.T) {
	_, _, err := workFromArgs([]string{"--effort", "max", "x"})
	require.ErrorIs(t, err, errEffortAboveCeiling)
}

func TestWorkPrompt(t *testing.T) {
	p := work{ticket: "ABC-1", summary: "Fix the race", details: "logs attached", done: "test passes 100x"}.prompt()

	require.True(t, strings.HasPrefix(p, "Fix the race\n"))
	require.Contains(t, p, "Ticket: ABC-1.")
	require.Contains(t, p, "Details:\nlogs attached")
	require.Contains(t, p, "Done when:\ntest passes 100x")
	require.NotContains(t, p, "Out of scope")
}

func TestWorkPrompt_NoTicket(t *testing.T) {
	require.Contains(t, work{summary: "x"}.prompt(), "No ticket.")
}

func TestStartArgs_NeedsSummaryWhenNotInteractive(t *testing.T) {
	_, err := startArgs([]string{"--ticket", "ABC-1"})
	require.ErrorIs(t, err, errStartUsage)
}
