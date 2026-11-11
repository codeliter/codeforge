package src

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/review"
)

func TestGateCommand_AcceptingTheLastBlockerGivesTheBranchItsRunsBack(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	rounds, accepts := review.Rounds{Dir: dir + "/r"}, review.AcceptedStore{Dir: dir + "/a"}
	require.NoError(t, rounds.Save("k", review.Round{Runs: 2, At: now, Blocking: []review.Finding{
		{File: "a.go", Line: 3, What: "nil deref"}, {File: "b.go", Line: 9, What: "race"},
	}}))
	var out strings.Builder
	require.NoError(t, gateCommand(rounds, accepts, "k", []string{"accept", "a.go:3", "guarded", "upstream"}, &out, now))
	prev, ok := rounds.Load("k", now)
	require.True(t, ok)
	require.True(t, prev.Capped(), "one finding still blocks, so the cap holds")
	require.Len(t, prev.Blocking, 1)
	require.Equal(t, "guarded upstream", accepts.Load("k", now)[0].Reason)

	require.NoError(t, gateCommand(rounds, accepts, "k", []string{"accept", "b.go:9"}, &out, now))
	_, ok = rounds.Load("k", now)
	require.False(t, ok, "nothing blocks, so the next gate run reviews again")

	out.Reset()
	require.NoError(t, gateCommand(rounds, accepts, "k", []string{"accepted"}, &out, now))
	require.Contains(t, out.String(), "- a.go:3 nil deref (accepted: guarded upstream)")

	require.NoError(t, gateCommand(rounds, accepts, "k", []string{"unaccept", "a.go:3"}, &out, now))
	require.Len(t, accepts.Load("k", now), 1)
	require.ErrorIs(t, gateCommand(rounds, accepts, "k", []string{"nope"}, &out, now), errGateUsage)
}

func TestGateCommand_ResetClearsTheRunCount(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	rounds := review.Rounds{Dir: dir}
	require.NoError(t, rounds.Save("k", review.Round{Runs: 2, At: now}))
	require.NoError(t, gateCommand(rounds, review.AcceptedStore{Dir: dir + "/a"}, "k", []string{"reset"}, &strings.Builder{}, now))
	_, ok := rounds.Load("k", now)
	require.False(t, ok)
}
