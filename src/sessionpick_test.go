package src

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionDay(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		ago  time.Duration
		want string
	}{
		{ago: time.Minute, want: "Today"},
		{ago: 9 * time.Hour, want: "Today"},
		{ago: 10 * time.Hour, want: "Yesterday"},
		{ago: 48 * time.Hour, want: "This week"},
		{ago: 6 * 24 * time.Hour, want: "This week"},
		{ago: 8 * 24 * time.Hour, want: "Earlier"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, sessionDay(now.Add(-tt.ago), now), tt.ago.String())
	}
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
