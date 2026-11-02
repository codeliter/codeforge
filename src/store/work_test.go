package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration_WorkModePreservesData(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	for _, name := range []string{"migrations/001_sessions.sql", "migrations/002_observations.sql"} {
		script, readErr := migrations.ReadFile(name)
		require.NoError(t, readErr)
		_, err = db.ExecContext(t.Context(), string(script))
		require.NoError(t, err)
	}
	for _, stmt := range []string{
		"INSERT INTO sessions VALUES ('old', 'review', 'codeforge-review', '/repo')",
		"INSERT INTO observations (kind, text, repository, session_id, origin, created_at) VALUES ('correction', 'Keep this', '/repo', 'old', 'cli', '2026-11-02T12:00:00Z')",
		"PRAGMA user_version = 2",
	} {
		_, err = db.ExecContext(t.Context(), stmt)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	s, err := Open(t.Context(), dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	old, err := s.Get(t.Context(), "old")
	require.NoError(t, err)
	require.Equal(t, Session{ID: "old", Mode: "review", Profile: "codeforge-review", Repository: "/repo"}, old)
	observations, err := s.Observations(t.Context(), "/repo", true)
	require.NoError(t, err)
	require.Len(t, observations, 1)
	require.Equal(t, "Keep this", observations[0].Text)
	work := Session{ID: "new", Mode: "work", Profile: "codeforge-work", Repository: "/repo"}
	require.NoError(t, s.Put(t.Context(), work))
	got, err := s.Get(t.Context(), work.ID)
	require.NoError(t, err)
	require.Equal(t, work, got)
	work.Mode = "chat"
	work.Profile = "codeforge-chat"
	require.ErrorIs(t, s.Put(t.Context(), work), ErrConflict)
	require.Error(t, s.Put(t.Context(), Session{ID: "bad-mode", Mode: "unknown", Profile: "codeforge-unknown", Repository: "/repo"}))
	require.Error(t, s.Put(t.Context(), Session{ID: "bad-profile", Mode: "work", Profile: "codeforge-chat", Repository: "/repo"}))
}
