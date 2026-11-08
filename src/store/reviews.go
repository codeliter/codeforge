package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ReviewKey names one review record: the GitHub account it was posted as and the pull request. It is the record's
// primary key, so each account has its own history of each PR and an update for the same key replaces, never adds.
type ReviewKey struct {
	Account, Repo, Number string
}

// ReviewRecord returns the record codeforge kept of what it posted under key, or ErrNotFound.
func (s *Store) ReviewRecord(ctx context.Context, key ReviewKey) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var record string
	err := s.db.QueryRowContext(ctx, "SELECT record FROM review_records WHERE account = ? AND repo = ? AND number = ?",
		key.Account, key.Repo, key.Number).Scan(&record)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("loading the review record: %w", err)
	}
	return []byte(record), nil
}

// UpdateReviewRecord reads the record of repo#number (nil when there is none), lets merge build the next one from it,
// and writes that, all in one write transaction: two runs on the same PR wait for each other, so neither merges from
// a record the other is about to replace.
func (s *Store) UpdateReviewRecord(ctx context.Context, key ReviewKey, merge func(old []byte) ([]byte, error)) (result error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil) // immediate: the store opens every transaction with _txlock=immediate
	if err != nil {
		return fmt.Errorf("starting the review record update: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			result = errors.Join(result, fmt.Errorf("rolling back the review record update: %w", err))
		}
	}()
	var old []byte
	var record string
	switch err := tx.QueryRowContext(ctx, "SELECT record FROM review_records WHERE account = ? AND repo = ? AND number = ?",
		key.Account, key.Repo, key.Number).Scan(&record); {
	case err == nil:
		old = []byte(record)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("loading the review record: %w", err)
	}
	next, err := merge(old)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO review_records (account, repo, number, record, saved_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (account, repo, number) DO UPDATE SET record = excluded.record, saved_at = excluded.saved_at`,
		key.Account, key.Repo, key.Number, string(next), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("saving the review record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing the review record: %w", err)
	}
	return nil
}
