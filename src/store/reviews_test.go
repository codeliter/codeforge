package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpen_WaitsPastTheOperationTimeoutForAnotherProcessesMigration(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(t.Context(), dir)
	require.NoError(t, err)
	// Another process holds the write lock, as one migrating does, for longer than one operation may take.
	tx, err := first.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(t.Context(), "PRAGMA user_version = user_version")
	require.NoError(t, err)
	released := time.AfterFunc(operationTimeout+500*time.Millisecond, func() { tx.Rollback() }) //nolint:errcheck // releasing the lock is all that matters
	defer released.Stop()
	defer first.Close() //nolint:errcheck // test

	start := time.Now()
	second, err := Open(t.Context(), dir)
	require.NoError(t, err, "opening waits for the lock instead of failing at operationTimeout")
	require.Greater(t, time.Since(start), operationTimeout)
	require.NoError(t, second.Close())
}

func TestReviewRecords_UpdatesOnOnePRNeverLoseEachOther(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			// Each run opens the database itself, as separate codeforge processes do.
			s, err := Open(t.Context(), dir)
			require.NoError(t, err)
			defer s.Close() //nolint:errcheck // test
			err = s.UpdateReviewRecord(t.Context(), pr42, func(old []byte) ([]byte, error) {
				threads := map[string]bool{}
				if old != nil {
					require.NoError(t, json.Unmarshal(old, &threads))
				}
				threads[fmt.Sprintf("T%d", i)] = true
				return json.Marshal(threads)
			})
			require.NoError(t, err)
		})
	}
	wg.Wait()
	s, err := Open(t.Context(), dir)
	require.NoError(t, err)
	defer s.Close() //nolint:errcheck // test
	data, err := s.ReviewRecord(t.Context(), pr42)
	require.NoError(t, err)
	var threads map[string]bool
	require.NoError(t, json.Unmarshal(data, &threads))
	require.Len(t, threads, 8, "every run merged from the one before it")
}

var (
	errMerge = errors.New("merge failed")
	pr42     = ReviewKey{Account: "alice", Repo: "acme/w", Number: "42"}
)

func TestReviewRecords_OnePerAccountAndPRAndNothingOnAFailedMerge(t *testing.T) {
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "s"))
	require.NoError(t, err)
	defer s.Close() //nolint:errcheck // test
	_, err = s.ReviewRecord(t.Context(), pr42)
	require.ErrorIs(t, err, ErrNotFound)

	set := func(v string) func([]byte) ([]byte, error) {
		return func([]byte) ([]byte, error) { return []byte(v), nil }
	}
	require.NoError(t, s.UpdateReviewRecord(t.Context(), pr42, set(`{"head":"a"}`)))
	require.ErrorIs(t, s.UpdateReviewRecord(t.Context(), pr42, func([]byte) ([]byte, error) { return nil, errMerge }), errMerge)
	got, err := s.ReviewRecord(t.Context(), pr42)
	require.NoError(t, err)
	require.JSONEq(t, `{"head":"a"}`, string(got), "a failed merge changes nothing")

	bob := pr42
	bob.Account = "bob"
	require.NoError(t, s.UpdateReviewRecord(t.Context(), bob, func(old []byte) ([]byte, error) {
		require.Nil(t, old, "bob never sees alice's record")
		return []byte(`{"head":"b"}`), nil
	}))
	got, err = s.ReviewRecord(t.Context(), pr42)
	require.NoError(t, err)
	require.JSONEq(t, `{"head":"a"}`, string(got), "and never replaces it")

	require.Error(t, s.UpdateReviewRecord(t.Context(), ReviewKey{Account: "alice", Repo: "acme", Number: "1"}, set(`{}`)), "a repo is owner/name")
	require.Error(t, s.UpdateReviewRecord(t.Context(), ReviewKey{Repo: "acme/w", Number: "1"}, set(`{}`)), "a record has an account")
	require.Error(t, s.UpdateReviewRecord(t.Context(), ReviewKey{Account: "alice", Repo: "acme/w", Number: "1"}, set(`not json`)), "a record is json")
}
