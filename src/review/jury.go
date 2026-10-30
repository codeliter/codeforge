package review

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

var ErrNoQuorum = errors.New("review: too few reviewers returned a verdict")

// Reviewer runs one independent review and returns its raw answer.
type Reviewer interface {
	Name() string
	Review(ctx context.Context, brief string) (string, error)
}

type Vote struct {
	Reviewer  string
	Verdict   Verdict
	Err       error
	Cached    bool
	Cancelled bool
	CacheErr  error
}

// JuryOptions makes the jury cheap: cached verdicts are reused, and a grounded block can end the run early.
type JuryOptions struct {
	Cache     *Cache
	CacheKey  func(reviewer string) string
	StopEarly bool
	Now       time.Time
}

type JuryResult struct {
	Votes      []Vote
	Blocking   []Finding
	Unverified []Finding
	Should     []Finding
}

func (r JuryResult) Passed() bool {
	return len(r.Blocking) == 0
}

// Jury runs every reviewer in parallel and fails on any Blocking finding that is grounded in the repository.
// One reviewer is enough to block; a quorum of verdicts is needed to pass.
func Jury(ctx context.Context, repo, brief string, reviewers []Reviewer, opts JuryOptions) (JuryResult, error) {
	votes := make([]Vote, len(reviewers))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Cache hits are resolved before any reviewer starts, so the goroutines below are the only writers.
	for i, rv := range reviewers {
		votes[i].Reviewer = rv.Name()
		if v, ok := cached(opts, rv.Name()); ok {
			votes[i].Verdict, votes[i].Cached = v, true
		}
	}
	if opts.StopEarly && slices.ContainsFunc(votes, func(v Vote) bool { return v.Cached && hasGroundedBlock(repo, v.Verdict) }) {
		cancel()
	}

	var wg sync.WaitGroup
	for i, rv := range reviewers {
		if votes[i].Cached || ctx.Err() != nil {
			continue
		}

		wg.Go(func() {
			out, err := rv.Review(ctx, brief)
			if err != nil {
				votes[i].Err, votes[i].Cancelled = err, ctx.Err() != nil
				return
			}

			votes[i].Verdict, votes[i].Err = ParseVerdict(out)
			if votes[i].Err != nil {
				votes[i].Err = fmt.Errorf("%w; answer ended with: %q", votes[i].Err, tail(out))
				return
			}
			if opts.StopEarly && hasGroundedBlock(repo, votes[i].Verdict) {
				cancel()
			}
		})
	}
	wg.Wait()

	res := JuryResult{Votes: votes}
	ok := 0
	for i, v := range votes {
		if !v.Cached && v.Err == nil && v.Verdict.Verdict == "" {
			votes[i].Cancelled = true
			continue
		}
		if v.Err != nil {
			continue
		}
		ok++
		for _, f := range v.Verdict.Blocking {
			f.What = v.Reviewer + ": " + f.What
			if grounded(repo, f) {
				res.Blocking = append(res.Blocking, f)
				continue
			}
			res.Unverified = append(res.Unverified, f)
		}
		for _, f := range v.Verdict.Should {
			res.Should = append(res.Should, Finding{File: f.File, Line: f.Line, What: v.Reviewer + ": " + f.What})
		}
	}

	if ok < quorum(len(reviewers)) && res.Passed() {
		return res, fmt.Errorf("%w: %d of %d", ErrNoQuorum, ok, len(reviewers))
	}
	return res, nil
}

// SaveVerdicts caches fresh verdicts. The caller runs it only after confirming the reviewed tree did not change,
// so a verdict about one tree is never stored under another tree's fingerprint.
func SaveVerdicts(res JuryResult, opts JuryOptions) {
	if opts.Cache == nil || opts.CacheKey == nil {
		return
	}
	for i, v := range res.Votes {
		if v.Cached || v.Err != nil || v.Cancelled || v.Verdict.Verdict == "" {
			continue
		}
		// A cache write failure only costs a re-review next time; it must not fail the gate.
		if err := opts.Cache.Put(opts.CacheKey(v.Reviewer), v.Verdict, opts.Now); err != nil {
			res.Votes[i].CacheErr = err
		}
	}
}

func tail(s string) string {
	const shown = 200
	s = strings.TrimSpace(s)
	if len(s) <= shown {
		return s
	}
	return "…" + s[len(s)-shown:]
}

func cached(opts JuryOptions, name string) (Verdict, bool) {
	if opts.Cache == nil || opts.CacheKey == nil {
		return Verdict{}, false
	}
	return opts.Cache.Get(opts.CacheKey(name), opts.Now)
}

func hasGroundedBlock(repo string, v Verdict) bool {
	return slices.ContainsFunc(v.Blocking, func(f Finding) bool { return grounded(repo, f) })
}

func quorum(n int) int {
	const independent = 2
	return min(n, independent)
}

// grounded rejects findings that point at files or lines that do not exist, a common reviewer hallucination.
func grounded(repo string, f Finding) bool {
	if f.File == "" || filepath.IsAbs(f.File) || slices.Contains(strings.Split(filepath.ToSlash(f.File), "/"), "..") {
		return false
	}

	file, err := os.Open(filepath.Join(repo, f.File))
	if err != nil {
		return false
	}
	defer file.Close()

	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return false
	}

	if f.Line <= 0 {
		return true
	}

	lines := 0
	sc := bufio.NewScanner(file)
	const maxLineBytes = 1 << 20
	sc.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxLineBytes)
	for sc.Scan() {
		lines++
		if lines >= f.Line {
			return true
		}
	}
	return false
}
