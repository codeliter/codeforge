package src

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/store"
)

func TestWhenLabel(t *testing.T) {
	now := time.Date(2026, 11, 8, 21, 0, 0, 0, time.UTC)
	tests := []struct {
		at   time.Time
		want string
	}{
		{at: now.Add(-20 * time.Second), want: "just now"},
		{at: now.Add(-42 * time.Minute), want: "42m ago"},
		{at: now.Add(-3 * time.Hour), want: "today 18:00"},
		{at: now.Add(-22 * time.Hour), want: "yesterday 23:00"},
		{at: now.AddDate(0, 0, -3), want: "Thu 21:00"},
		{at: now.AddDate(0, -2, 0), want: "8 Sep 21:00"},
		{at: now.AddDate(-1, 0, 0), want: "8 Nov 2025"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) { require.Equal(t, tt.want, whenLabel(tt.at, now)) })
	}
}

func TestShortDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{d: 5 * time.Minute, want: "5m"},
		{d: 72 * time.Minute, want: "1h12m"},
		{d: 35 * time.Hour, want: "1d11h"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) { require.Equal(t, tt.want, shortDuration(tt.d)) })
	}
}

func TestSessionTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "Review PR #3 with the codeforge-review skill, as the brief says", want: "Review PR #3"},
		{in: "  Provider intersection ", want: "Provider intersection"},
		{in: "Talk with the codeforge-team", want: "Talk with the codeforge-team"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) { require.Equal(t, tt.want, sessionTitle(tt.in)) })
	}
}

// writeSession lays out one Empryo session in wd, with turns user lines in its log.
func writeSession(t *testing.T, wd, id, cwd, title string, updated time.Time, turns int, headless bool) {
	t.Helper()
	dir := filepath.Join(wd, ".empryo", "sessions", id)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	meta := fmt.Sprintf(`{"id":%q,"title":%q,"cwd":%q,"startedAt":%d,"updatedAt":%d,"activeTabId":"t1",`+
		`"tabs":[{"id":"t1","activeModel":"subscriptions/claude-opus-5-5","tokenUsage":{"cost":1.5}}]}`,
		id, title, cwd, updated.Add(-time.Hour).UnixMilli(), updated.UnixMilli())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600))
	host := "tui"
	if headless {
		host = "headless"
	}
	log := fmt.Sprintf(`{"k":"meta","v":1,"host":%q}`+"\n", host) +
		strings.Repeat(`{"k":"user","ui":{"content":"`+strings.Repeat("x", 70000)+`"}}`+"\n", turns)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(log), 0o600))
}

func TestListSessions_NewestFirstAndOnlyWhatCanBeOpened(t *testing.T) {
	home := t.TempDir()
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	now := time.Now()
	writeSession(t, wd, "old-chat", wd, "Old chat", now.Add(-48*time.Hour), 3, false)
	writeSession(t, wd, "new-review", wd, "Review PR #3 with the codeforge-review skill", now.Add(-time.Hour), 1, true)
	writeSession(t, wd, "empty", wd, "Empty session", now, 0, false)
	writeSession(t, wd, "unknown-mode", wd, "Plain Empryo", now, 2, false)
	writeSession(t, wd, "elsewhere", t.TempDir(), "Another directory", now, 2, false)
	st, err := store.Open(t.Context(), filepath.Join(config.Dir(home), "sessions"))
	require.NoError(t, err)
	for id, mode := range map[string]string{"old-chat": modeChat, "new-review": modeReview, "empty": modeChat, "elsewhere": modeChat} {
		require.NoError(t, st.Put(t.Context(), store.Session{ID: id, Mode: mode, Profile: profileFor(mode), Repository: wd}))
	}
	require.NoError(t, st.Close())

	sessions, skipped, err := listSessions(t.Context(), home, wd)
	require.NoError(t, err)
	require.Equal(t, 3, skipped, "the empty session, the one whose mode is unknown and the one of another directory")
	require.Len(t, sessions, 2, "a session of another directory is not this one's")
	require.Equal(t, "new-review", sessions[0].ID, "newest first")
	require.Equal(t, sessionInfo{
		ID: "new-review", Dir: wd, Mode: modeReview, Title: "Review PR #3", Model: "subscriptions/claude-opus-5-5", Headless: true,
		Started: sessions[0].Started, Updated: sessions[0].Updated, Turns: 1, Cost: 1.5,
	}, sessions[0])
	require.Equal(t, 3, sessions[1].Turns, "turns are counted on lines longer than the read buffer too")

	label := sessionLabel(sessions[1], now, false)
	for _, part := range []string{"chat", "Old chat", "3 turns", "1h00m", "$1.50", "claude-opus-5-5", "old-chat"} {
		require.Contains(t, label, part)
	}
	require.Contains(t, sessionLabel(sessions[0], now, false), "review*", "a headless run is marked")
	require.NotContains(t, label, filepath.Base(wd), "the project shows only with --all")
	require.Contains(t, sessionLabel(sessions[1], now, true), filepath.Base(wd))
	require.Equal(t, wd, sessions[0].Dir)
}

func TestListSessions_NoSessionsYet(t *testing.T) {
	sessions, skipped, err := listSessions(t.Context(), t.TempDir(), t.TempDir())
	require.NoError(t, err)
	require.Empty(t, sessions)
	require.Zero(t, skipped)
}

func TestListAllSessions_EveryProjectCodeforgeRanIn(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	st, err := store.Open(t.Context(), filepath.Join(config.Dir(home), "sessions"))
	require.NoError(t, err)
	var projects []string
	for i, name := range []string{"api", "web"} {
		wd, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		projects = append(projects, wd)
		writeSession(t, wd, name+"-s", wd, "Work on "+name, now.Add(-time.Duration(i)*time.Hour), 2, false)
		require.NoError(t, st.Put(t.Context(), store.Session{ID: name + "-s", Mode: modeWork, Profile: profileFor(modeWork), Repository: wd}))
	}
	require.NoError(t, st.Put(t.Context(), store.Session{ID: "gone", Mode: modeWork, Profile: profileFor(modeWork), Repository: projects[0]}))
	require.NoError(t, st.Close())

	sessions, skipped, err := listAllSessions(t.Context(), home)
	require.NoError(t, err)
	require.Zero(t, skipped)
	require.Len(t, sessions, 2, "a recorded session whose files are gone is not listed")
	require.Equal(t, []string{"api-s", "web-s"}, []string{sessions[0].ID, sessions[1].ID})
	require.Equal(t, projects[1], sessions[1].Dir, "each opens in its own project")
}

func TestSessionHeaderLinesUpWithTheLabels(t *testing.T) {
	s := sessionInfo{ID: "abcdef123", Mode: modeChat, Title: "T", Dir: "/x/proj", Turns: 2, Updated: time.Now()}
	for _, all := range []bool{false, true} {
		header, label := sessionHeader(all), sessionLabel(s, time.Now(), all)
		require.Equal(t, strings.Index(header, "TURNS"), strings.Index(label, "2 turns"))
	}
}
