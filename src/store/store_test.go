package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	s, err := Open(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s, dir
}

func TestStore_IdentityAndPermissions(t *testing.T) {
	s, dir := openTest(t)
	session := Session{ID: "id", Mode: "review", Profile: "codeforge-review", Repository: "/repo"}
	require.NoError(t, s.Put(t.Context(), session))
	require.NoError(t, s.Put(t.Context(), session))
	got, err := s.Get(t.Context(), session.ID)
	require.NoError(t, err)
	require.Equal(t, session, got)
	session.Mode, session.Profile = "chat", "codeforge-chat"
	require.ErrorIs(t, s.Put(t.Context(), session), ErrConflict)
	_, err = s.Get(t.Context(), "missing")
	require.ErrorIs(t, err, ErrNotFound)
	for _, name := range []string{"", "sessions.db", "sessions.db-wal", "sessions.db-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		perm := os.FileMode(0o600)
		if name == "" {
			perm = 0o700
		}
		require.Equal(t, perm, info.Mode().Perm(), name)
	}
	var journal string
	var synchronous, wait int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&journal))
	require.Equal(t, "wal", journal)
	require.NoError(t, s.db.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&synchronous))
	require.Equal(t, 2, synchronous)
	require.NoError(t, s.db.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&wait))
	require.Equal(t, busyTimeoutMS, wait)
}

func TestMigration_RollbackAndNewerSchema(t *testing.T) {
	s, dir := openTest(t)
	scripts := fstest.MapFS{
		"migrations/001_sessions.sql":     {Data: []byte("unused")},
		"migrations/002_observations.sql": {Data: []byte("unused")},
		"migrations/003_work_mode.sql":    {Data: []byte("unused")},
		"migrations/004_broken.sql":       {Data: []byte("CREATE TABLE partial (id TEXT); INSERT INTO absent VALUES (1);")},
	}
	require.Error(t, s.initialize(t.Context(), scripts))
	var version, tables int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
	require.Equal(t, 3, version)
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='partial'").Scan(&tables))
	require.Zero(t, tables)
	_, err := s.db.ExecContext(t.Context(), "PRAGMA user_version = 99")
	require.NoError(t, err)
	other, err := Open(t.Context(), dir)
	require.Nil(t, other)
	require.ErrorIs(t, err, ErrNewerSchema)
}

func TestStore_BoundedConcurrentAccess(t *testing.T) {
	s, dir := openTest(t)
	other, err := Open(t.Context(), dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, other.Close()) }()
	tx, err := s.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	start := time.Now()
	session := Session{ID: "locked", Mode: "chat", Profile: "codeforge-chat", Repository: "/repo"}
	require.Error(t, other.Put(t.Context(), session))
	require.Less(t, time.Since(start), operationTimeout)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = s.Get(ctx, "id")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), operationTimeout)
	require.NoError(t, tx.Rollback())
	require.NoError(t, other.Put(t.Context(), session))
	// Two independent openers serialize migrations and writes without losing identity.
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			opened, err := Open(t.Context(), dir)
			if err == nil {
				err = errors.Join(opened.Put(t.Context(), session), opened.Close())
			}
			errs <- err
		}()
	}
	for range 2 {
		require.NoError(t, <-errs)
	}
}

//nolint:revive // The subprocess intentionally exits with an open transaction to test crash recovery.
func TestStore_InterruptedProcess(t *testing.T) {
	if dir := os.Getenv("CODEFORGE_STORE_CRASH_DIR"); dir != "" {
		s, err := Open(context.Background(), dir)
		if err != nil {
			os.Exit(2)
		}
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			os.Exit(2)
		}
		if _, err := tx.ExecContext(context.Background(), "INSERT INTO sessions VALUES ('interrupted', 'chat', 'codeforge-chat', '/repo')"); err != nil {
			os.Exit(2)
		}
		os.Exit(23) // Deliberately terminate without rollback or Close.
	}
	s, dir := openTest(t)
	session := Session{ID: "committed", Mode: "ticket", Profile: "codeforge-ticket", Repository: "/repo"}
	require.NoError(t, s.Put(t.Context(), session))
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStore_InterruptedProcess$")
	cmd.Env = append(os.Environ(), "CODEFORGE_STORE_CRASH_DIR="+dir)
	var exit *exec.ExitError
	require.ErrorAs(t, cmd.Run(), &exit)
	require.Equal(t, 23, exit.ExitCode())
	recovered, err := Open(t.Context(), dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, recovered.Close()) }()
	got, err := recovered.Get(t.Context(), session.ID)
	require.NoError(t, err)
	require.Equal(t, session, got)
	_, err = recovered.Get(t.Context(), "interrupted")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStore_RejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, nil, 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "sessions.db")))
	_, err := Open(t.Context(), dir)
	require.ErrorIs(t, err, errUnsafePath)
}

func TestMigration_InterruptedContext(t *testing.T) {
	s, _ := openTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, s.initialize(ctx, migrations), context.Canceled)
	var version int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version))
	require.Equal(t, 3, version)
}
