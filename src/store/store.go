// Package store persists launch identity, never approvals or review verdicts.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	migrationpath "path"
	"path/filepath"
	"strings"
	"time"

	// Register the pure-Go SQLite database/sql driver.
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

const operationTimeout = 2 * time.Second
const busyTimeoutMS = 500
const privateDir = 0o700
const privateFile = 0o600

// ErrNewerSchema prevents an older binary from opening newer storage.
var ErrNewerSchema = errors.New("session database is newer than this codeforge binary")

// ErrNotFound identifies an unknown Codeforge session.
var ErrNotFound = errors.New("session not found")

// ErrConflict prevents changing a session's original identity.
var ErrConflict = errors.New("session identity conflicts with saved launch")
var errUnsafePath = errors.New("session storage path must be a real directory or regular file")

// Session is the immutable identity recorded before Empryo starts.
type Session struct {
	ID         string
	Mode       string
	Profile    string
	Repository string
}

// Store is a concrete SQLite session store.
type Store struct{ db *sql.DB }

// Open creates private storage and applies all pending migrations atomically.
func Open(ctx context.Context, dir string) (*Store, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := os.MkdirAll(dir, privateDir); err != nil {
		return nil, fmt.Errorf("creating session directory: %w", err)
	}
	if err := privatePath(dir, true); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "sessions.db")
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := privatePath(path+suffix, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, privateFile)
	if err != nil {
		return nil, fmt.Errorf("creating session database: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("closing session file: %w", err)
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("opening session database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.initialize(ctx, migrations); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}

func privatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("checking session path: %w", err)
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", errUnsafePath, path)
	}
	perm := fs.FileMode(privateFile)
	if directory {
		perm = privateDir
	}
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("securing session path: %w", err)
	}
	return nil
}

func (s *Store) initialize(ctx context.Context, scripts fs.FS) (result error) {
	files, err := fs.Glob(scripts, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting migration: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			result = errors.Join(result, fmt.Errorf("rolling back migration: %w", err))
		}
	}() // A failed or interrupted migration never advances the schema.
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if version > len(files) {
		return ErrNewerSchema
	}
	for i, name := range files {
		if !strings.HasPrefix(migrationpath.Base(name), fmt.Sprintf("%03d_", i+1)) {
			return fmt.Errorf("%w: invalid migration numbering", errUnsafePath)
		}
		if i < version {
			continue
		}
		script, err := fs.ReadFile(scripts, name)
		if err != nil {
			return fmt.Errorf("reading migration: %w", err)
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			return fmt.Errorf("applying %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			return fmt.Errorf("advancing schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing migration: %w", err)
	}
	var journal string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journal); err != nil {
		return fmt.Errorf("enabling WAL: %w", err)
	}
	if journal != "wal" {
		return fmt.Errorf("%w: WAL unavailable", errUnsafePath)
	}
	return nil
}

// Close releases SQLite and checkpoints its WAL on the final connection.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("closing session database: %w", err)
	}
	return nil
}

// Put records identity once; repeated identical launches are harmless.
func (s *Store) Put(ctx context.Context, session Session) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, "INSERT INTO sessions(id, mode, profile, repository) VALUES (?, ?, ?, ?) ON CONFLICT(id) DO NOTHING", session.ID, session.Mode, session.Profile, session.Repository); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}
	saved, err := s.Get(ctx, session.ID)
	if err != nil {
		return err
	}
	if saved != session {
		return ErrConflict
	}
	return nil
}

// Get loads launch identity without touching enforcement state.
func (s *Store) Get(ctx context.Context, id string) (Session, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var session Session
	err := s.db.QueryRowContext(ctx, "SELECT id, mode, profile, repository FROM sessions WHERE id = ?", id).Scan(&session.ID, &session.Mode, &session.Profile, &session.Repository)
	if errors.Is(err, sql.ErrNoRows) {
		return session, ErrNotFound
	}
	if err != nil {
		return session, fmt.Errorf("loading session: %w", err)
	}
	return session, nil
}
