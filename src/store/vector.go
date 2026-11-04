package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	// Registers sqlite-vec, the CGo-free vector search extension, on every connection opened afterwards.
	_ "modernc.org/sqlite/vec"
)

// VectorDims is the length of every stored correction vector.
const VectorDims = 256

// Options changes how a store opens. DisableVector skips the vector table, which is how the fallback path is tested.
type Options struct{ DisableVector bool }

// Neighbour is a stored vector near a query, by Euclidean distance. Vectors are unit length, so the cosine
// similarity is 1 - Distance*Distance/2.
type Neighbour struct {
	ID       int64
	Distance float64
}

// initVector creates the vector table after migrating. Failure is not an error: grouping falls back to word
// overlap, so learning never depends on the extension.
func (s *Store) initVector(ctx context.Context, opts Options) {
	if opts.DisableVector {
		return
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("CREATE VIRTUAL TABLE IF NOT EXISTS correction_vec USING vec0(embedding float[%d])", VectorDims))
	s.vec.Store(err == nil)
}

// VectorAvailable reports whether vector search works in this store.
func (s *Store) VectorAvailable() bool { return s.vec.Load() }

func vectorBlob(v []float32) []byte {
	out := make([]byte, 0, len(v)*4) //nolint:mnd // four bytes per float32
	for _, f := range v {
		out = binary.LittleEndian.AppendUint32(out, math.Float32bits(f))
	}
	return out
}

// RecordWithVector saves an observation and its vector in one transaction. Without a usable vector table, or if
// the vector cannot be saved, the observation is still recorded on its own.
func (s *Store) RecordWithVector(ctx context.Context, o Observation, v []float32) (int64, error) {
	if !s.vec.Load() || len(v) != VectorDims {
		return s.Record(ctx, o)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.Record(ctx, o)
	}
	id, err := insertObservation(ctx, tx, o)
	if err != nil {
		_ = tx.Rollback() //nolint:errcheck // the plain insert below reports the real failure
		return s.Record(ctx, o)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO correction_vec(rowid, embedding) VALUES (?, ?)", id, vectorBlob(v)); err != nil {
		_ = tx.Rollback() //nolint:errcheck // the plain insert below reports the real failure
		// Only the vector table failing makes vectors unavailable; a busy database or a bad observation does not.
		s.vec.Store(false)
		return s.Record(ctx, o)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("saving observation: %w", err)
	}
	return id, nil
}

// EnsureVector stores a vector for an observation recorded before vectors existed. It does nothing when one is saved.
func (s *Store) EnsureVector(ctx context.Context, id int64, v []float32) error {
	if !s.vec.Load() || len(v) != VectorDims {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM correction_vec WHERE rowid = ?", id).Scan(&n); err != nil {
		return fmt.Errorf("checking vector: %w", err)
	}
	if n > 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO correction_vec(rowid, embedding) VALUES (?, ?)", id, vectorBlob(v)); err != nil {
		// Another reader may have backfilled the same row between the check and the insert; that is success.
		if recheck := s.db.QueryRowContext(ctx, "SELECT count(*) FROM correction_vec WHERE rowid = ?", id).Scan(&n); recheck == nil && n > 0 {
			return nil
		}
		return fmt.Errorf("saving vector: %w", err)
	}
	return nil
}

// Neighbours returns the k stored vectors nearest to v, closest first. It cannot filter by repository, so callers
// filter the result.
func (s *Store) Neighbours(ctx context.Context, v []float32, k int) ([]Neighbour, error) {
	if !s.vec.Load() || len(v) != VectorDims || k <= 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT rowid, distance FROM correction_vec WHERE embedding MATCH ? AND k = %d ORDER BY distance", k), vectorBlob(v))
	if err != nil {
		return nil, fmt.Errorf("searching vectors: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // read-only cursor; rows.Err reports failures
	var out []Neighbour
	for rows.Next() {
		var n Neighbour
		if err := rows.Scan(&n.ID, &n.Distance); err != nil {
			return nil, fmt.Errorf("reading vector match: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("searching vectors: %w", err)
	}
	return out, nil
}
