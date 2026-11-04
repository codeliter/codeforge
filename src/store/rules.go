package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNoRule identifies an unknown or already forgotten rule.
var ErrNoRule = errors.New("rule not found")

// ErrDismissed means the user already declined this rule.
var ErrDismissed = errors.New("rule was declined")

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Rule is a correction the user accepted. Only accepted, not forgotten rules reach a prompt.
type Rule struct {
	ID          int64
	Repository  string
	Dir         string
	Text        string
	SourceIDs   string
	AcceptedAt  time.Time
	ForgottenAt *time.Time
}

// AddRule saves an accepted rule and returns its ID. The same live rule is never stored twice for a directory.
func (s *Store) AddRule(ctx context.Context, r Rule) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return insertRule(ctx, s.db, r)
}

// ErrRuleLimit means a directory already has as many live rules as it may.
var ErrRuleLimit = errors.New("rule limit reached")

// AddRuleUnlessDismissed saves a rule only if none of the dismissal keys is declined and the directory has fewer than
// limit live rules, checking and inserting in one transaction so a forget or another launch in a different process
// cannot be undone or outrun the limit. created is false when an identical live rule already existed.
func (s *Store) AddRuleUnlessDismissed(ctx context.Context, r Rule, keys []string, limit int) (id int64, created bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("starting rule save: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // a committed transaction cannot be rolled back
	for _, key := range keys {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM dismissals WHERE repository = ? AND key = ?", r.Repository, key).Scan(&n); err != nil {
			return 0, false, fmt.Errorf("checking dismissals: %w", err)
		}
		if n > 0 {
			return 0, false, ErrDismissed
		}
	}
	dir := nonEmpty(r.Dir, ".")
	var existing int64
	switch err := tx.QueryRowContext(ctx, "SELECT id FROM rules WHERE repository = ? AND dir = ? AND text = ? AND forgotten_at IS NULL", r.Repository, dir, r.Text).Scan(&existing); {
	case err == nil:
		return existing, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, fmt.Errorf("checking rules: %w", err)
	}
	var live int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM rules WHERE repository = ? AND dir = ? AND forgotten_at IS NULL", r.Repository, dir).Scan(&live); err != nil {
		return 0, false, fmt.Errorf("counting rules: %w", err)
	}
	if limit > 0 && live >= limit {
		return 0, false, ErrRuleLimit
	}
	id, err = insertRule(ctx, tx, r)
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("saving rule: %w", err)
	}
	return id, true, nil
}

func insertRule(ctx context.Context, db querier, r Rule) (int64, error) {
	var existing int64
	switch err := db.QueryRowContext(ctx, "SELECT id FROM rules WHERE repository = ? AND dir = ? AND text = ? AND forgotten_at IS NULL", r.Repository, nonEmpty(r.Dir, "."), r.Text).Scan(&existing); {
	case err == nil:
		return existing, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("checking rules: %w", err)
	}
	res, err := db.ExecContext(ctx, "INSERT INTO rules(repository, dir, text, source_ids, accepted_at) VALUES (?, ?, ?, ?, ?)",
		r.Repository, nonEmpty(r.Dir, "."), r.Text, r.SourceIDs, r.AcceptedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("saving rule: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reading rule id: %w", err)
	}
	return id, nil
}

// ForgetRuleAndDismiss forgets a rule and declines every key in one transaction.
func (s *Store) ForgetRuleAndDismiss(ctx context.Context, repository string, id int64, keys []string, at time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting forget: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // a committed transaction cannot be rolled back
	stamp := at.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, "UPDATE rules SET forgotten_at = ? WHERE id = ? AND repository = ? AND forgotten_at IS NULL", stamp, id, repository)
	if err != nil {
		return fmt.Errorf("forgetting rule: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("forgetting rule: %w", err)
	} else if n == 0 {
		return ErrNoRule
	}
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO dismissals(repository, key, dismissed_at) VALUES (?, ?, ?)", repository, key, stamp); err != nil {
			return fmt.Errorf("saving dismissal: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("forgetting rule: %w", err)
	}
	return nil
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// Rules lists a repository's rules, oldest first; forgotten ones only when asked.
func (s *Store) Rules(ctx context.Context, repository string, withForgotten bool) ([]Rule, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, "SELECT id, repository, dir, text, source_ids, accepted_at, forgotten_at FROM rules WHERE repository = ? AND (? OR forgotten_at IS NULL) ORDER BY id", repository, withForgotten)
	if err != nil {
		return nil, fmt.Errorf("listing rules: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // read-only cursor; rows.Err reports failures
	var out []Rule
	for rows.Next() {
		var r Rule
		var accepted string
		var forgotten sql.NullString
		if err := rows.Scan(&r.ID, &r.Repository, &r.Dir, &r.Text, &r.SourceIDs, &accepted, &forgotten); err != nil {
			return nil, fmt.Errorf("reading rule: %w", err)
		}
		if r.AcceptedAt, err = time.Parse(time.RFC3339Nano, accepted); err != nil {
			return nil, fmt.Errorf("reading rule time: %w", err)
		}
		if forgotten.Valid {
			at, err := time.Parse(time.RFC3339Nano, forgotten.String)
			if err != nil {
				return nil, fmt.Errorf("reading rule time: %w", err)
			}
			r.ForgottenAt = &at
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing rules: %w", err)
	}
	return out, nil
}

// ForgetRule stops a rule from reaching prompts. The row stays for provenance.
func (s *Store) ForgetRule(ctx context.Context, repository string, id int64, at time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx, "UPDATE rules SET forgotten_at = ? WHERE id = ? AND repository = ? AND forgotten_at IS NULL", at.UTC().Format(time.RFC3339Nano), id, repository)
	if err != nil {
		return fmt.Errorf("forgetting rule: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("forgetting rule: %w", err)
	}
	if n == 0 {
		return ErrNoRule
	}
	return nil
}

// Dismiss remembers that a group of corrections, named by key, was declined.
func (s *Store) Dismiss(ctx context.Context, repository, key string, at time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO dismissals(repository, key, dismissed_at) VALUES (?, ?, ?)", repository, key, at.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("saving dismissal: %w", err)
	}
	return nil
}

// Dismissed returns the keys declined in a repository.
func (s *Store) Dismissed(ctx context.Context, repository string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, "SELECT key FROM dismissals WHERE repository = ?", repository)
	if err != nil {
		return nil, fmt.Errorf("listing dismissals: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // read-only cursor; rows.Err reports failures
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("reading dismissal: %w", err)
		}
		out[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing dismissals: %w", err)
	}
	return out, nil
}
