//nolint:err113 // dynamic error messages are okay in tests
package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeReviewer struct {
	name string
	out  string
	err  error
}

func (f fakeReviewer) Name() string { return f.name }

func (f fakeReviewer) Review(context.Context, string) (string, error) { return f.out, f.err }

const pass = `{"verdict":"pass","summary":"ok","blocking":[],"should":[]}`

func repoWith(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o600))
	return dir
}

func TestJury_AllPass(t *testing.T) {
	res, err := Jury(t.Context(), repoWith(t), "brief", []Reviewer{
		fakeReviewer{name: "x", out: pass}, fakeReviewer{name: "y", out: pass},
	}, JuryOptions{})
	require.NoError(t, err)
	require.True(t, res.Passed())
}

func TestJury_OneGroundedBlockFails(t *testing.T) {
	block := `{"verdict":"fail","blocking":[{"file":"a.go","line":3,"what":"nil deref"}]}`
	res, err := Jury(t.Context(), repoWith(t), "brief", []Reviewer{
		fakeReviewer{name: "x", out: pass}, fakeReviewer{name: "y", out: block},
	}, JuryOptions{})
	require.NoError(t, err)
	require.False(t, res.Passed())
	require.Equal(t, "y: nil deref", res.Blocking[0].What)
}

func TestJury_UngroundedBlockDoesNotFail(t *testing.T) {
	ghost := `{"verdict":"fail","blocking":[{"file":"missing.go","line":1,"what":"x"},{"file":"a.go","line":99,"what":"y"},{"file":"../etc/passwd","line":1,"what":"z"}]}`
	res, err := Jury(t.Context(), repoWith(t), "brief", []Reviewer{
		fakeReviewer{name: "x", out: pass}, fakeReviewer{name: "y", out: ghost},
	}, JuryOptions{})
	require.NoError(t, err)
	require.True(t, res.Passed())
	require.Len(t, res.Unverified, 3)
}

func TestJury_NeedsQuorumToPass(t *testing.T) {
	_, err := Jury(t.Context(), repoWith(t), "brief", []Reviewer{
		fakeReviewer{name: "x", out: pass}, fakeReviewer{name: "y", err: errors.New("timeout")},
	}, JuryOptions{})
	require.ErrorIs(t, err, ErrNoQuorum)

	_, err = Jury(t.Context(), repoWith(t), "brief", []Reviewer{
		fakeReviewer{name: "x", out: pass}, fakeReviewer{name: "y", out: "no verdict here"},
	}, JuryOptions{})
	require.ErrorIs(t, err, ErrNoQuorum)
}

func TestJury_ABlockStandsWithoutQuorum(t *testing.T) {
	block := `{"verdict":"fail","blocking":[{"file":"a.go","line":1,"what":"bad"}]}`
	res, err := Jury(t.Context(), repoWith(t), "brief", []Reviewer{
		fakeReviewer{name: "x", out: block}, fakeReviewer{name: "y", err: errors.New("down")},
	}, JuryOptions{})
	require.NoError(t, err)
	require.False(t, res.Passed())
}

type slowReviewer struct {
	name    string
	started chan struct{}
}

func (s slowReviewer) Name() string { return s.name }

func (s slowReviewer) Review(ctx context.Context, _ string) (string, error) {
	close(s.started)
	<-ctx.Done()
	return "", fmt.Errorf("slow: %w", ctx.Err())
}

func TestJury_StopsEarlyOnGroundedBlock(t *testing.T) {
	block := `{"verdict":"fail","blocking":[{"file":"a.go","line":1,"what":"bad"}]}`
	slow := slowReviewer{name: "slow", started: make(chan struct{})}

	res, err := Jury(t.Context(), repoWith(t), "brief", []Reviewer{
		slow, fakeReviewer{name: "fast", out: block},
	}, JuryOptions{StopEarly: true})
	require.NoError(t, err)
	require.False(t, res.Passed())
	require.True(t, res.Votes[0].Cancelled, "the slow reviewer is cancelled once a grounded block is in")
}

type countingReviewer struct {
	name  string
	out   string
	calls *int
}

func (c countingReviewer) Name() string { return c.name }

func (c countingReviewer) Review(context.Context, string) (string, error) {
	*c.calls++
	return c.out, nil
}

func TestJury_ReusesCachedVerdicts(t *testing.T) {
	cache := &Cache{Dir: t.TempDir()}
	opts := JuryOptions{Cache: cache, CacheKey: func(r string) string { return CacheKey("tree", r) }, Now: time.Now()}

	x, y := 0, 0
	reviewers := []Reviewer{countingReviewer{"x", pass, &x}, countingReviewer{"y", pass, &y}}

	first, err := Jury(t.Context(), repoWith(t), "brief", reviewers, opts)
	require.NoError(t, err)
	_, err = Jury(t.Context(), repoWith(t), "brief", reviewers, opts)
	require.NoError(t, err)
	require.Equal(t, 2, x, "nothing is cached until the caller saves")

	SaveVerdicts(first, opts)
	res, err := Jury(t.Context(), repoWith(t), "brief", reviewers, opts)
	require.NoError(t, err)

	require.Equal(t, 2, x)
	require.Equal(t, 2, y)
	require.True(t, res.Votes[0].Cached)
	require.True(t, res.Passed())
}

func TestCache_Expires(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	now := time.Now()
	require.NoError(t, c.Put("k", Verdict{Verdict: VerdictPass}, now))

	_, ok := c.Get("k", now.Add(cacheTTL+time.Hour))
	require.False(t, ok)
	_, ok = c.Get("k", now)
	require.True(t, ok)
}

func TestGrounded_RejectsDirectories(t *testing.T) {
	repo := repoWith(t)
	require.NoError(t, os.Mkdir(filepath.Join(repo, "pkg"), 0o700))
	require.False(t, grounded(repo, Finding{File: "pkg", Line: 0}))
	require.True(t, grounded(repo, Finding{File: "a.go", Line: 0}))
}
