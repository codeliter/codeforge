package store

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func unit(dim int) []float32 {
	v := make([]float32, VectorDims)
	v[dim] = 1
	return v
}

func correction(text string) Observation {
	return Observation{Kind: KindCorrection, Text: text, Repository: "/repo", Origin: OriginHook, CreatedAt: time.Now()}
}

func TestStoreVec_RecordAndNeighbours(t *testing.T) {
	s, _ := openTest(t)
	require.True(t, s.VectorAvailable(), "the bundled sqlite-vec loads in this build")
	a, err := s.RecordWithVector(t.Context(), correction("use table tests"), unit(0))
	require.NoError(t, err)
	b, err := s.RecordWithVector(t.Context(), correction("prefer table tests"), unit(1))
	require.NoError(t, err)
	got, err := s.Neighbours(t.Context(), unit(0), 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, a, got[0].ID, "rowid matches the observation id, nearest first")
	require.InDelta(t, 0, got[0].Distance, 1e-6)
	require.Equal(t, b, got[1].ID)
	require.InDelta(t, math.Sqrt2, got[1].Distance, 1e-6, "orthogonal unit vectors are sqrt(2) apart")
}

func TestStoreVec_WrongSizeFallsBackToPlainRecord(t *testing.T) {
	s, _ := openTest(t)
	id, err := s.RecordWithVector(t.Context(), correction("x"), []float32{1, 2})
	require.NoError(t, err)
	require.NotZero(t, id)
	got, err := s.Neighbours(t.Context(), unit(0), 3)
	require.NoError(t, err)
	require.Empty(t, got, "an observation without a valid vector has none stored")
}

func TestStoreVec_DisabledStoreStillRecords(t *testing.T) {
	s, err := OpenWith(t.Context(), filepath.Join(t.TempDir(), "sessions"), Options{DisableVector: true})
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	require.False(t, s.VectorAvailable())
	id, err := s.RecordWithVector(t.Context(), correction("x"), unit(0))
	require.NoError(t, err)
	require.NotZero(t, id)
	got, err := s.Neighbours(t.Context(), unit(0), 3)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestStoreVec_EnsureVectorIsIdempotent(t *testing.T) {
	s, _ := openTest(t)
	id, err := s.Record(t.Context(), correction("old correction"))
	require.NoError(t, err)
	require.NoError(t, s.EnsureVector(t.Context(), id, unit(3)))
	require.NoError(t, s.EnsureVector(t.Context(), id, unit(3)))
	got, err := s.Neighbours(t.Context(), unit(3), 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestMigration_005KeepsRowsAndAddsHookCorrections(t *testing.T) {
	s, _ := openTest(t)
	_, err := s.Record(t.Context(), correction("from a hook"))
	require.NoError(t, err, "005 allows hook-origin corrections")
	_, err = s.Record(t.Context(), Observation{Kind: KindCorrection, Text: "x", Repository: "/repo", Origin: "weird", CreatedAt: time.Now()})
	require.Error(t, err)
}

func TestRules_AddListForgetAndDismiss(t *testing.T) {
	s, _ := openTest(t)
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	id, err := s.AddRule(t.Context(), Rule{Repository: "/repo", Text: "use table-driven tests", SourceIDs: "1,2", AcceptedAt: at})
	require.NoError(t, err)
	_, err = s.AddRule(t.Context(), Rule{Repository: "/other", Text: "elsewhere", AcceptedAt: at})
	require.NoError(t, err)

	rules, err := s.Rules(t.Context(), "/repo", false)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, Rule{ID: id, Repository: "/repo", Dir: ".", Text: "use table-driven tests", SourceIDs: "1,2", AcceptedAt: at}, rules[0])

	require.NoError(t, s.ForgetRule(t.Context(), "/repo", id, at))
	require.ErrorIs(t, s.ForgetRule(t.Context(), "/repo", id, at), ErrNoRule, "forgetting twice fails")
	require.ErrorIs(t, s.ForgetRule(t.Context(), "/other", id, at), ErrNoRule, "another repository's rule cannot be forgotten")
	rules, err = s.Rules(t.Context(), "/repo", false)
	require.NoError(t, err)
	require.Empty(t, rules)
	rules, err = s.Rules(t.Context(), "/repo", true)
	require.NoError(t, err)
	require.NotNil(t, rules[0].ForgottenAt)

	require.NoError(t, s.Dismiss(t.Context(), "/repo", "key one", at))
	require.NoError(t, s.Dismiss(t.Context(), "/repo", "key one", at), "dismissing twice is fine")
	got, err := s.Dismissed(t.Context(), "/repo")
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"key one": true}, got)
	other, err := s.Dismissed(t.Context(), "/other")
	require.NoError(t, err)
	require.Empty(t, other)
}

func TestRules_RejectInvalidRows(t *testing.T) {
	s, _ := openTest(t)
	_, err := s.AddRule(t.Context(), Rule{Repository: "", Text: "x", AcceptedAt: time.Now()})
	require.Error(t, err)
	_, err = s.AddRule(t.Context(), Rule{Repository: "/repo", Text: "", AcceptedAt: time.Now()})
	require.Error(t, err)
}

func TestStoreVec_ConcurrentBackfillIsSafe(t *testing.T) {
	s, dir := openTest(t)
	id, err := s.Record(t.Context(), correction("old correction"))
	require.NoError(t, err)
	other, err := Open(t.Context(), dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, other.Close()) }()
	errs := make(chan error, 2)
	for _, st := range []*Store{s, other} {
		go func() { errs <- st.EnsureVector(t.Context(), id, unit(5)) }()
	}
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	got, err := s.Neighbours(t.Context(), unit(5), 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
}
