package store

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestObservations_RecordListRevert(t *testing.T) {
	s, _ := openTest(t)
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	zero, one := 0, 1
	correction := Observation{Kind: KindCorrection, Text: "Use table tests", Repository: "/repo", SessionID: "s1", Origin: OriginCLI, CreatedAt: at}
	passed := Observation{Kind: KindOutcome, Text: "go test ./...", Repository: "/repo", SessionID: "s1", Origin: OriginRun, Status: StatusPassed, ExitCode: &zero, Duration: 1500 * time.Millisecond, CreatedAt: at}
	failed := Observation{Kind: KindOutcome, Text: "go vet ./...", Repository: "/repo", Origin: OriginRun, Status: StatusFailed, ExitCode: &one, CreatedAt: at}
	incomplete := Observation{Kind: KindOutcome, Text: "make lint", Repository: "/repo", Origin: OriginRun, Status: StatusIncomplete, Duration: time.Minute, CreatedAt: at}
	other := Observation{Kind: KindCorrection, Text: "elsewhere", Repository: "/other", Origin: OriginCLI, CreatedAt: at}
	var ids []int64
	for _, o := range []Observation{correction, passed, failed, incomplete, other} {
		id, err := s.Record(t.Context(), o)
		require.NoError(t, err)
		ids = append(ids, id)
	}

	got, err := s.Observations(t.Context(), "/repo", false)
	require.NoError(t, err)
	require.Len(t, got, 4)
	for i, want := range []Observation{correction, passed, failed, incomplete} {
		want.ID = ids[i]
		want.Dir = "." // an observation without a directory is stored as the repository root
		require.Equal(t, want, got[i])
	}

	require.ErrorIs(t, s.Revert(t.Context(), "/repo", ids[4], at), ErrNoObservation)
	require.ErrorIs(t, s.Revert(t.Context(), "/repo", 999, at), ErrNoObservation)
	require.NoError(t, s.Revert(t.Context(), "/repo", ids[0], at))
	require.NoError(t, s.Revert(t.Context(), "/repo", ids[0], at.Add(time.Hour)))
	got, err = s.Observations(t.Context(), "/repo", false)
	require.NoError(t, err)
	require.Len(t, got, 3)
	got, err = s.Observations(t.Context(), "/repo", true)
	require.NoError(t, err)
	require.Len(t, got, 4)
	require.NotNil(t, got[0].RevertedAt)
	require.True(t, at.Equal(*got[0].RevertedAt))
}

func TestObservations_SchemaRejectsInconsistentEvidence(t *testing.T) {
	s, _ := openTest(t)
	at := time.Now()
	zero, one := 0, 1
	for name, o := range map[string]Observation{
		"empty text":            {Kind: KindCorrection, Repository: "/repo", Origin: OriginCLI, CreatedAt: at},
		"empty repository":      {Kind: KindCorrection, Text: "x", Origin: OriginCLI, CreatedAt: at},
		"correction from a run": {Kind: KindCorrection, Text: "x", Repository: "/repo", Origin: OriginRun, CreatedAt: at},
		"outcome from cli":      {Kind: KindOutcome, Text: "x", Repository: "/repo", Origin: OriginCLI, Status: StatusPassed, ExitCode: &zero, CreatedAt: at},
		"passed with failure":   {Kind: KindOutcome, Text: "x", Repository: "/repo", Origin: OriginRun, Status: StatusPassed, ExitCode: &one, CreatedAt: at},
		"failed with success":   {Kind: KindOutcome, Text: "x", Repository: "/repo", Origin: OriginRun, Status: StatusFailed, ExitCode: &zero, CreatedAt: at},
		"incomplete with exit":  {Kind: KindOutcome, Text: "x", Repository: "/repo", Origin: OriginRun, Status: StatusIncomplete, ExitCode: &zero, CreatedAt: at},
		"unknown status":        {Kind: KindOutcome, Text: "x", Repository: "/repo", Origin: OriginRun, Status: "approved", ExitCode: &zero, CreatedAt: at},
		"unknown kind":          {Kind: "preference", Text: "x", Repository: "/repo", Origin: OriginCLI, CreatedAt: at},
	} {
		_, err := s.Record(t.Context(), o)
		require.Error(t, err, name)
	}
}

func TestMigration_UpgradesSessionsDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	s, err := Open(t.Context(), dir)
	require.NoError(t, err)
	require.NoError(t, s.Put(t.Context(), Session{ID: "old", Mode: "chat", Profile: "codeforge-chat", Repository: "/repo"}))
	for _, stmt := range []string{"DROP TABLE observations", "DROP TABLE rules", "DROP TABLE dismissals", "PRAGMA user_version = 1"} {
		_, err := s.db.ExecContext(t.Context(), stmt)
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())

	s, err = Open(t.Context(), dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	got, err := s.Get(t.Context(), "old")
	require.NoError(t, err)
	require.Equal(t, "chat", got.Mode)
	var version int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
	require.Equal(t, 5, version)
	_, err = s.Observations(t.Context(), "/repo", true)
	require.NoError(t, err)
}

func TestObservations_StoredTimesAreUTC(t *testing.T) {
	s, dir := openTest(t)
	at := time.Date(2026, 11, 2, 14, 0, 0, 0, time.FixedZone("CET", 3600))
	_, err := s.Record(t.Context(), Observation{Kind: KindCorrection, Text: "x", Repository: "/repo", Origin: OriginCLI, CreatedAt: at})
	require.NoError(t, err)
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "sessions.db")}
	db, err := sql.Open("sqlite", u.String())
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var raw string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT created_at FROM observations").Scan(&raw))
	require.Equal(t, "2026-11-02T13:00:00Z", raw)
}

func TestObservations_HookOutcomes(t *testing.T) {
	s, _ := openTest(t)
	at := time.Now()
	zero, one := 0, 1
	for name, o := range map[string]Observation{
		"passed without code": {Kind: KindOutcome, Text: "go test ./...", Repository: "/repo", Origin: OriginHook, Status: StatusPassed, CreatedAt: at},
		"failed without code": {Kind: KindOutcome, Text: "go test ./...", Repository: "/repo", Origin: OriginHook, Status: StatusFailed, CreatedAt: at},
		"passed with zero":    {Kind: KindOutcome, Text: "go test ./...", Repository: "/repo", Origin: OriginHook, Status: StatusPassed, ExitCode: &zero, CreatedAt: at},
		"failed with one":     {Kind: KindOutcome, Text: "go test ./...", Repository: "/repo", Origin: OriginHook, Status: StatusFailed, ExitCode: &one, CreatedAt: at},
		"incomplete":          {Kind: KindOutcome, Text: "go test ./...", Repository: "/repo", Origin: OriginHook, Status: StatusIncomplete, CreatedAt: at},
	} {
		_, err := s.Record(t.Context(), o)
		require.NoError(t, err, name)
	}
	for name, o := range map[string]Observation{
		"passed with failure": {Kind: KindOutcome, Text: "x", Repository: "/repo", Origin: OriginHook, Status: StatusPassed, ExitCode: &one, CreatedAt: at},
		"failed with success": {Kind: KindOutcome, Text: "x", Repository: "/repo", Origin: OriginHook, Status: StatusFailed, ExitCode: &zero, CreatedAt: at},
		"unknown origin":      {Kind: KindCorrection, Text: "x", Repository: "/repo", Origin: "weird", CreatedAt: at},
	} {
		_, err := s.Record(t.Context(), o)
		require.Error(t, err, name)
	}
}

func TestMigration_KeepsObservationsWhenRebuilt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	s, err := Open(t.Context(), dir)
	require.NoError(t, err)
	id, err := s.Record(t.Context(), Observation{Kind: KindCorrection, Text: "keep me", Repository: "/repo", Origin: OriginCLI, CreatedAt: time.Now()})
	require.NoError(t, err)
	// Return to the schema before the rebuild, as an upgrading user's database would be.
	for _, stmt := range []string{
		"DROP TABLE observations",
		"DROP TABLE rules",
		"DROP TABLE dismissals",
		"CREATE TABLE observations (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, text TEXT NOT NULL, repository TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '', origin TEXT NOT NULL, status TEXT, exit_code INTEGER, duration_ms INTEGER, created_at TEXT NOT NULL, reverted_at TEXT)",
		"INSERT INTO observations(kind, text, repository, origin, created_at) VALUES ('correction', 'keep me', '/repo', 'cli', '2026-11-02T13:00:00Z')",
		"PRAGMA user_version = 3",
	} {
		_, err := s.db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
	require.NotZero(t, id)
	require.NoError(t, s.Close())

	s, err = Open(t.Context(), dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	obs, err := s.Observations(t.Context(), "/repo", true)
	require.NoError(t, err)
	require.Len(t, obs, 1)
	require.Equal(t, "keep me", obs[0].Text)
}

func TestObservations_StoresDirectory(t *testing.T) {
	s, _ := openTest(t)
	_, err := s.Record(t.Context(), Observation{Kind: KindOutcome, Text: "npm test", Repository: "/repo", Dir: "frontend", Origin: OriginHook, Status: StatusPassed, CreatedAt: time.Now()})
	require.NoError(t, err)
	got, err := s.Observations(t.Context(), "/repo", false)
	require.NoError(t, err)
	require.Equal(t, "frontend", got[0].Dir)
}

func TestObservations_HookOutcomesStoreNoDuration(t *testing.T) {
	s, dir := openTest(t)
	_, err := s.Record(t.Context(), Observation{Kind: KindOutcome, Text: "go test ./...", Repository: "/repo", Origin: OriginHook, Status: StatusPassed, Duration: 5 * time.Second, CreatedAt: time.Now()})
	require.NoError(t, err)
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "sessions.db")}
	db, err := sql.Open("sqlite", u.String())
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var isNull bool
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT duration_ms IS NULL FROM observations").Scan(&isNull))
	require.True(t, isNull, "a duration the hook never saw is not invented")
	got, err := s.Observations(t.Context(), "/repo", false)
	require.NoError(t, err)
	require.Zero(t, got[0].Duration)
}
