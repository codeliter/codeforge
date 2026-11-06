package review

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTierCache_RoundTripAndSkipsFallback(t *testing.T) {
	c := TierCache{Dir: t.TempDir()}
	now := time.Now()

	_, _, ok := c.Get("k", now)
	require.False(t, ok)

	c.Put("k", TierRisky, "judged", now)
	got, why, ok := c.Get("k", now)
	require.True(t, ok)
	require.Equal(t, TierRisky.Name, got.Name)
	require.Equal(t, "judged", why)

	_, _, ok = c.Get("k", now.Add(cacheTTL+time.Minute))
	require.False(t, ok, "an old classification is not reused")

	c.Put("fallback", TierStandard, "no judge answered (HTTP 401); using the standard tier", now)
	_, _, ok = c.Get("fallback", now)
	require.False(t, ok, "a fallback says nothing about the tree")
}

func TestRounds_SaveLoadClearAndExpire(t *testing.T) {
	r := Rounds{Dir: t.TempDir()}
	now := time.Now()
	key := RoundKey("/r", "feat", "main")

	_, ok := r.Load(key, now)
	require.False(t, ok)

	require.NoError(t, r.Save(key, Round{Tree: "t1", Blocking: []Finding{{File: "a.go", Line: 3, What: "x"}}, At: now}))
	got, ok := r.Load(key, now)
	require.True(t, ok)
	require.Equal(t, "t1", got.Tree)

	_, ok = r.Load(key, now.Add(roundTTL+time.Minute))
	require.False(t, ok, "an old round is not a follow-up")

	_, ok = r.Load(RoundKey("/r", "other", "main"), now)
	require.False(t, ok, "rounds are per branch")

	r.Clear(key)
	_, ok = r.Load(key, now)
	require.False(t, ok)
}

func TestChangedSince(t *testing.T) {
	prev := Round{Files: map[string]string{"a.go": "1", "b.go": "2", "gone.go": "3"}}
	now := map[string]string{"a.go": "1", "b.go": "9", "new.go": "4"}
	require.Equal(t, []string{"b.go", "gone.go", "new.go"}, prev.ChangedSince(now))
	require.Empty(t, prev.ChangedSince(prev.Files))
}

func TestFileDigests(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("one"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=1"), 0o600))

	first := FileDigests(dir, []string{"a.go", ".env", "missing.go"})
	require.NotContains(t, first, ".env", "secrets are never fingerprinted into the round")
	require.Equal(t, "unreadable", first["missing.go"])

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("two"), 0o600))
	require.NotEqual(t, first["a.go"], FileDigests(dir, []string{"a.go"})["a.go"])
}

func TestFilterDiff(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n+one\ndiff --git a/b.go b/b.go\n+two\n"
	require.Equal(t, "diff --git a/b.go b/b.go\n+two\n", FilterDiff(diff, []string{"b.go"}))
	require.Empty(t, FilterDiff(diff, nil))
}

func TestDeltaNote(t *testing.T) {
	note := DeltaNote(Round{Blocking: []Finding{{File: "a.go", Line: 7, What: "nil deref"}}}, []string{"a.go"})
	require.Contains(t, note, "a.go:7 nil deref")
	require.Contains(t, note, "these files changed: a.go")
	require.Contains(t, note, "unchanged")
	require.NotContains(t, note, "were already reviewed")
	require.Contains(t, note, "git grep hits per changed exported symbol")
}
