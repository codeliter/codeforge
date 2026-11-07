//nolint:err113 // dynamic error messages are okay in tests
//go:build eval

package review

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Recall on bugs nobody injected: changes from this repository's own history that a later "fix" commit repaired. The
// lines the fix rewrote, traced by git blame to the change that wrote them, are the labelled defects (see mineHistory).
// The reviewer sees the real change as it was committed, in a clone with no later history.
//
// The idea is the one in citation [14] of TECHNICAL.md, Stanley on Anthropic's code review: judge a review regime by
// what it lets through, not by how little attention it spends, since reviewing faster while more changes are reverted
// only moves the effort. Reverts and fixes are that outcome here.
//
//	CODEFORGE_EVAL_MODEL=provider/model go test -tags eval ./src/review -run TestEvalHistory -v -timeout 90m
//
// Optional: CODEFORGE_EVAL_HISTORY_SCAN (recent commits scanned for fixes, default 200), CODEFORGE_EVAL_HISTORY_CASES
// (most cases to run, default 8), CODEFORGE_EVAL_RUNS (default 3), CODEFORGE_EVAL_SETUPS, CODEFORGE_EVAL_DUMP, and the
// others of TestEvalLargeDiff.
// It reports and never gates: a fix can rewrite a line for reasons that are not a bug, so a miss is evidence to read, not
// proof. "Other blockers" are unlabelled findings, many of them real.

const (
	historyMinBytes    = 3_000
	historyMaxBytes    = 150_000
	historyMaxDefects  = 6
	historyMaxFixLines = 40
)

func envInt(name string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return fallback
}

func TestEvalHistory(t *testing.T) {
	if os.Getenv("CODEFORGE_EVAL_MODEL") == "" {
		t.Skip("set CODEFORGE_EVAL_MODEL=provider/model")
	}
	top, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	mined, err := mineHistory(t.Context(), strings.TrimSpace(string(top)), mineOptions{
		Scan: envInt("CODEFORGE_EVAL_HISTORY_SCAN", 200), MinBytes: historyMinBytes, MaxBytes: historyMaxBytes, MaxDefects: historyMaxDefects, MaxFixLines: historyMaxFixLines,
	})
	require.NoError(t, err)
	if len(mined) == 0 {
		t.Skip("no fix commits with a reviewable change in the scanned history")
	}
	mined = mined[:min(len(mined), envInt("CODEFORGE_EVAL_HISTORY_CASES", 8))]

	t.Setenv("CODEFORGE_EVAL_RUNS", strconv.Itoa(envInt("CODEFORGE_EVAL_RUNS", 3)))
	var cases []largeCase
	for _, m := range mined {
		t.Logf("%.7s (%d KB, %d bugs) later repaired by %.7s: %s", m.Intro, m.DiffBytes/bytesPerKB, len(m.Defects), m.Fix, m.Subject)
		cases = append(cases, largeCase{name: m.Intro[:7], base: m.Parent, head: m.Intro, defects: m.Defects})
	}
	evalCases(t, cases, false)
}
