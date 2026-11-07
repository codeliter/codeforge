//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

func TestWorkFromArgs(t *testing.T) {
	w, rest, err := workFromArgs([]string{"--ticket", "ABC-1", "--model", "a/b", "fix", "the", "race", "--headless"})
	require.NoError(t, err)
	require.Equal(t, "ABC-1", w.ticket)
	require.Equal(t, "fix the race", w.description)
	require.Equal(t, []string{"--model", "a/b", "--headless"}, rest)
}

func TestWorkFromArgs_RejectsEffortAboveCeiling(t *testing.T) {
	_, _, err := workFromArgs([]string{"--effort", "max", "x"})
	require.ErrorIs(t, err, errEffortAboveCeiling)
}

func TestWorkPrompt(t *testing.T) {
	p := work{ticket: "ABC-1", description: "  Fix the race in the worker.\nLogs are in #123.  "}.prompt()

	require.True(t, strings.HasPrefix(p, "Fix the race in the worker.\nLogs are in #123.\n\n"))
	require.Contains(t, p, "Ticket: ABC-1.")
	require.Contains(t, p, "codeforge-ticket-to-pr")
	for _, gone := range []string{"Details:", "Done when", "Out of scope"} {
		require.NotContains(t, p, gone, "only the ticket and description remain")
	}
}

func TestWorkPrompt_NoTicket(t *testing.T) {
	require.Contains(t, work{description: "x"}.prompt(), "No ticket.")
}

func TestStartArgs_NeedsDescriptionWhenNotInteractive(t *testing.T) {
	_, _, err := startArgs([]string{"--ticket", "ABC-1"})
	require.ErrorIs(t, err, errStartUsage)
}

func TestStartArgs_ReturnsDescriptionForTheJudge(t *testing.T) {
	args, description, err := startArgs([]string{"--ticket", "ABC-1", "--model", "a/b", "fix", "the", "race"})
	require.NoError(t, err)
	require.Equal(t, "fix the race", description, "the judge sees the description, not the prompt template")
	require.Equal(t, []string{"--model", "a/b"}, args[:2])
	require.Contains(t, args[2], "Ticket: ABC-1.")
}

func TestTicketJudgeScoresDescriptionOnly(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "empryo")
	capture := filepath.Join(t.TempDir(), "judge-input")
	t.Setenv("CODEFORGE_JUDGE_CAPTURE", capture)
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nfor a; do last=$a; done\nprintf '%s' \"$last\" > \"$CODEFORGE_JUDGE_CAPTURE\"\nprintf '%s' '{\"output\":\"{\\\"ambiguity\\\":0,\\\"blast\\\":0,\\\"risk\\\":0,\\\"novelty\\\":0,\\\"verify\\\":0,\\\"reason\\\":\\\"typo\\\"}\"}'\n"), execPerm))
	mode, args, err := modeArgs(t.Context(), bin, config.Config{Model: "a/coder", Judges: []string{"a/judge"}}, modeTicket, []string{"--ticket", "ABC-1", "fix", "the", "typo"})
	require.NoError(t, err)
	require.Equal(t, modeTicket, mode)
	require.Equal(t, "low", flagValue(args, "--effort"))
	data, err := os.ReadFile(capture)
	require.NoError(t, err)
	require.Equal(t, "<task>\nfix the typo\n</task>", string(data), "no skill line, ticket line or repo signals reach the judge")
}
