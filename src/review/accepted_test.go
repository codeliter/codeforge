package review

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSetAside_AnAcceptedFindingStopsBlockingAndFollowsTheCode(t *testing.T) {
	list := []Accepted{{File: "a.go", Line: 10, What: "x"}}
	kept, aside, moved := SetAside([]Finding{
		{File: "a.go", Line: 13, What: "x again, two edits later"},
		{File: "a.go", Line: 40, What: "something else"},
		{File: "a.go", Line: 0, What: "the repository's own tests fail for a"},
	}, list)
	require.Len(t, aside, 1)
	require.Equal(t, 13, aside[0].Line)
	require.Len(t, kept, 2, "a finding elsewhere and a failing test still block")
	require.True(t, moved)
	require.Equal(t, 13, list[0].Line, "the accepted finding moves with the code")
}

func TestAccept_OnlyAReportedFindingAndNeverATest(t *testing.T) {
	now := time.Now()
	prev := []Finding{{File: "a.go", Line: 3, What: "nil deref"}, {File: "a.go", What: "the repository's own tests fail for a"}}
	list, a, err := Accept(nil, prev, "a.go", 3, "guarded upstream", now)
	require.NoError(t, err)
	require.Equal(t, Accepted{File: "a.go", Line: 3, What: "nil deref", Reason: "guarded upstream", At: now}, a)
	require.Len(t, list, 1)

	_, _, err = Accept(list, prev, "a.go", 4, "", now)
	require.ErrorIs(t, err, ErrNoSuchFinding)
	_, _, err = Accept(list, prev, "a.go", 0, "", now)
	require.ErrorIs(t, err, ErrTestNotAcceptable)

	list, ok := Unaccept(list, "a.go", 3)
	require.True(t, ok)
	require.Empty(t, list)
}

func TestAcceptedStore_RoundTripAndRemove(t *testing.T) {
	s := AcceptedStore{Dir: t.TempDir()}
	now := time.Now()
	require.Empty(t, s.Load("k", now))
	require.NoError(t, s.Save("k", []Accepted{{File: "a.go", Line: 1, What: "x", At: now}}, now))
	require.Len(t, s.Load("k", now), 1)
	require.Contains(t, AcceptedNote(s.Load("k", now)), "- a.go:1 x")
	require.NoError(t, s.Save("k", nil, now))
	require.Empty(t, s.Load("k", now))
}

func TestParsePlace(t *testing.T) {
	p, n, err := ParsePlace("src/a.go:12")
	require.NoError(t, err)
	require.Equal(t, "src/a.go", p)
	require.Equal(t, 12, n)
	_, _, err = ParsePlace("src/a.go")
	require.Error(t, err)
}

func TestRound_CappedAfterTheMostFailedRuns(t *testing.T) {
	require.Equal(t, 3, MaxGateRuns)
	require.False(t, Round{Runs: MaxGateRuns - 1}.Capped())
	require.True(t, Round{Runs: MaxGateRuns}.Capped())
	require.Equal(t, 1, Round{}.FailedRuns(), "a round saved before runs were counted is one failed run")
}
