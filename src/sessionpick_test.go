package src

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionsByMode_EachModeApartNewestFirst(t *testing.T) {
	now := time.Now()
	at := func(id, mode string, ago time.Duration) sessionInfo {
		return sessionInfo{ID: id, Mode: mode, Updated: now.Add(-ago)}
	}
	groups := sessionsByMode([]sessionInfo{
		at("w1", modeWork, time.Minute), at("r1", modeReview, time.Hour), at("w2", modeWork, 2*time.Hour),
		at("c1", modeChat, 3*time.Hour), at("r2", modeReview, 4*time.Hour),
	})
	var got []string
	for _, g := range groups {
		ids := []string{modeHeading(g.Mode) + ":"}
		for _, s := range g.Sessions {
			ids = append(ids, s.ID)
		}
		got = append(got, strings.Join(ids, " "))
	}
	require.Equal(t, []string{"Work: w1 w2", "Review: r1 r2", "Chat: c1"}, got, "the mode used last comes first, and each keeps its order")
	require.Empty(t, sessionsByMode(nil))
	require.Empty(t, modeHeading(""))
}

func TestSplitDetails_ASessionLineSplitsWhereItsDetailsStart(t *testing.T) {
	now := time.Now()
	s := sessionInfo{ID: "0123456789abcdef", Mode: "chat", Title: "A title", Turns: 3, Updated: now, Started: now, Model: "copilot/gpt-6.1-sol"}
	columns, details := splitDetails(sessionLabel(s, now, false), false)
	require.Equal(t, "3 turns · gpt-6.1-sol · 01234567", details)
	require.Contains(t, columns, "A title")
	require.NotContains(t, columns, "turns")

	require.Equal(t, []string{"WHEN             MODE    TITLE", "TURNS · TIME · COST · MODEL · ID"}, headerParts(false))
	columns, details = splitDetails("short", false)
	require.Equal(t, "short", columns)
	require.Empty(t, details)

	s.Title = "Fix  the  thing"
	columns, details = splitDetails(sessionLabel(s, now, true), true)
	require.Contains(t, columns, "Fix  the  thing", "a title with wide gaps stays whole")
	require.Equal(t, "3 turns · gpt-6.1-sol · 01234567", details)
}
