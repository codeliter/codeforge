package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Observation kinds, origins and outcome statuses match the CHECK constraints in 002_observations.sql.
const (
	KindCorrection = "correction"
	KindOutcome    = "outcome"

	// OriginCLI means the text was typed into the codeforge CLI. It is not proof a person typed it.
	OriginCLI = "cli"
	// OriginRun means codeforge ran the check itself and saw its exit status.
	OriginRun = "codeforge-run"
	// OriginHook means Empryo's tool hook reported that a command's tool call succeeded or failed. The exit
	// code is stored only when Empryo reports one.
	OriginHook = "hook"

	StatusPassed     = "passed"
	StatusFailed     = "failed"
	StatusIncomplete = "incomplete"
)

// ErrNoObservation identifies an unknown observation ID.
var ErrNoObservation = errors.New("observation not found")

// Observation is one piece of learning evidence. It is never applied to prompts, personas or guard policy.
type Observation struct {
	ID         int64
	Kind       string
	Text       string
	Repository string
	SessionID  string
	// Dir is where the command ran, relative to the repository root ("." for the root). Empty means ".".
	Dir        string
	Origin     string
	Status     string
	ExitCode   *int
	Duration   time.Duration
	CreatedAt  time.Time
	RevertedAt *time.Time
}

// Record appends an observation and returns its ID; the schema rejects inconsistent kind, origin and status.
func (s *Store) Record(ctx context.Context, o Observation) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var status, duration any
	if o.Kind == KindOutcome {
		status = o.Status
		// A hook cannot see how long a command took, so it stores no duration instead of a made-up zero.
		if o.Origin != OriginHook {
			duration = o.Duration.Milliseconds()
		}
	}
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO observations(kind, text, repository, session_id, dir, origin, status, exit_code, duration_ms, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		o.Kind, o.Text, o.Repository, o.SessionID, cmp.Or(o.Dir, "."), o.Origin, status, o.ExitCode, duration, o.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("saving observation: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reading observation id: %w", err)
	}
	return id, nil
}

// Observations lists a repository's observations oldest first; reverted ones only when asked.
func (s *Store) Observations(ctx context.Context, repository string, withReverted bool) ([]Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, kind, text, repository, session_id, dir, origin, status, exit_code, duration_ms, created_at, reverted_at FROM observations WHERE repository = ? AND (? OR reverted_at IS NULL) ORDER BY id",
		repository, withReverted)
	if err != nil {
		return nil, fmt.Errorf("listing observations: %w", err)
	}
	defer func() { _ = rows.Close() }() // Read-only cursor; rows.Err reports read failures.
	var out []Observation
	for rows.Next() {
		o, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing observations: %w", err)
	}
	return out, nil
}

func scanObservation(rows *sql.Rows) (Observation, error) {
	var o Observation
	var status, reverted sql.NullString
	var exit, duration sql.NullInt64
	var created string
	if err := rows.Scan(&o.ID, &o.Kind, &o.Text, &o.Repository, &o.SessionID, &o.Dir, &o.Origin, &status, &exit, &duration, &created, &reverted); err != nil {
		return o, fmt.Errorf("reading observation: %w", err)
	}
	o.Status = status.String
	o.Duration = time.Duration(duration.Int64) * time.Millisecond
	if exit.Valid {
		code := int(exit.Int64)
		o.ExitCode = &code
	}
	var err error
	if o.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return o, fmt.Errorf("reading observation time: %w", err)
	}
	if reverted.Valid {
		at, err := time.Parse(time.RFC3339Nano, reverted.String)
		if err != nil {
			return o, fmt.Errorf("reading observation time: %w", err)
		}
		o.RevertedAt = &at
	}
	return o, nil
}

// Revert hides an observation from proposals but keeps the row for provenance; reverting twice keeps the first time.
func (s *Store) Revert(ctx context.Context, repository string, id int64, at time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx,
		"UPDATE observations SET reverted_at = coalesce(reverted_at, ?) WHERE id = ? AND repository = ?",
		at.UTC().Format(time.RFC3339Nano), id, repository)
	if err != nil {
		return fmt.Errorf("reverting observation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reverting observation: %w", err)
	}
	if n == 0 {
		return ErrNoObservation
	}
	return nil
}
