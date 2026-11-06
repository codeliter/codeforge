//nolint:err113 // dynamic error messages are okay in tests
//go:build eval

package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Large-diff recall: the case the tiny repositories cannot test. The change is a real 56-file, 1,900-line diff of this
// repository (v0.10.3~8..v0.10.3) with known bugs injected into it, so the step and time caps are under real pressure.
//
//	CODEFORGE_EVAL_MODEL=provider/model go test -tags eval ./src/review -run TestEvalLargeDiff -v -timeout 60m
//
// Optional: CODEFORGE_EVAL_SETUPS (comma list of setup names), CODEFORGE_EVAL_BIN, CODEFORGE_EVAL_RUNS (default 6 runs per setup, run in parallel).
// Spurious blockers are not all false positives here: the real diff has defects of its own that nobody labelled, so
// spurious is reported, never gated. Gated when both setups ran: every parallel run returns a verdict, and parallel recall stays
// at recallFloor of the lone reviewer's.

const (
	largeBase      = "v0.10.3~8"
	largeHead      = "v0.10.3"
	minVerdictRate = 0.95
)

type mutation struct {
	file, from, to string
	defect         Defect
}

var largeMutations = []mutation{
	{"src/review/clock.go", "return minTime(time.Now().Add(AgentLimit), WorkDeadline(ctx))", "return minTime(time.Now().Add(AgentLimit), Deadline(ctx))",
		Defect{Name: "agent end ignores the reserve", File: "src/review/clock.go", Lo: 64, Hi: 64, Keywords: []string{"reserve", "WorkDeadline"}}},
	{"src/review/recall.go", "f.Line >= d.Lo-lineSlack && f.Line <= d.Hi+lineSlack", "f.Line >= d.Lo-lineSlack || f.Line <= d.Hi+lineSlack",
		Defect{Name: "line match always true", File: "src/review/recall.go", Lo: 25, Hi: 25, Keywords: []string{"always", "||", "any line"}}},
	{"src/gate.go", "after != tree || cleanHead(ctx, repo) != commit", "after != tree && cleanHead(ctx, repo) != commit",
		Defect{Name: "pass recorded after HEAD moved", File: "src/gate.go", Lo: 178, Hi: 178, Keywords: []string{"&&", "HEAD", "commit changed"}}},
	{"src/guard/lock.go", "if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {", "if !errors.Is(err, syscall.EWOULDBLOCK) || !errors.Is(err, syscall.EINTR) {",
		Defect{Name: "held lock fails instead of waiting", File: "src/guard/lock.go", Lo: 55, Hi: 55, Keywords: []string{"EWOULDBLOCK", "||", "never waits", "held"}}},
	{"src/review/panel.go", "if v.Err != nil || v.Verdict.Verdict == \"\" {", "if v.Err != nil && v.Verdict.Verdict == \"\" {",
		Defect{Name: "failed juror counted as a verdict", File: "src/review/panel.go", Lo: 50, Hi: 50, Keywords: []string{"&&", "v.Err", "error vote"}}},
	{"src/reviewcmd.go", "left := min(review.AgentLimit, time.Until(review.WorkDeadline(ctx))) - review.KillGrace", "left := max(review.AgentLimit, time.Until(review.WorkDeadline(ctx))) - review.KillGrace",
		Defect{Name: "reviewer time exceeds the deadline", File: "src/reviewcmd.go", Lo: 147, Hi: 147, Keywords: []string{"max(", "exceed", "longer than"}}},
}

func buildLargeRepo(t *testing.T) (string, []Defect) {
	t.Helper()
	top, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	root := strings.TrimSpace(string(top))
	for _, ref := range []string{largeBase, largeHead} {
		if err := exec.CommandContext(t.Context(), "git", "-C", root, "rev-parse", "--verify", "-q", ref).Run(); err != nil {
			t.Skipf("%s is not in this clone", ref)
		}
	}
	dir := t.TempDir()
	out, err := exec.CommandContext(t.Context(), "git", "clone", "-q", "--no-checkout", root, dir).CombinedOutput()
	require.NoError(t, err, string(out))
	gitIn(t, dir, "remote", "remove", "origin")
	gitIn(t, dir, "checkout", "-q", "-B", "main", largeBase)
	gitIn(t, dir, "checkout", "-q", "-B", "feature", largeHead)
	gitIn(t, dir, "reset", "-q", "--soft", "main")
	var defects []Defect
	for _, m := range largeMutations {
		p := dir + "/" + m.file
		body, err := os.ReadFile(p)
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(string(body), m.from), "%s: the injected line must exist exactly once at %s", m.file, largeHead)
		require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(body), m.from, m.to, 1)), 0o600))
		defects = append(defects, m.defect)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "change")
	return dir, defects
}

type lsetup struct {
	name    string
	tier    Tier
	timeout time.Duration
	run     func(ctx context.Context, brief string) (string, error)
}

type runResult struct {
	score    Score
	verdict  bool
	elapsed  time.Duration
	err      error
	blocking int
	tail     string
}

func TestEvalLargeDiff(t *testing.T) {
	model := os.Getenv("CODEFORGE_EVAL_MODEL")
	if model == "" {
		t.Skip("set CODEFORGE_EVAL_MODEL=provider/model")
	}
	bin := cmpOr(os.Getenv("CODEFORGE_EVAL_BIN"), "empryo")
	runs := 6
	if v, err := strconv.Atoi(os.Getenv("CODEFORGE_EVAL_RUNS")); err == nil && v > 0 {
		runs = v
	}
	dir, defects := buildLargeRepo(t)
	l, err := LoadLocal(t.Context(), dirRunner{dir: dir})
	require.NoError(t, err)
	t.Logf("change: %d files, %d bytes of diff, %d seeded defects", len(l.Files), len(l.Diff), len(defects))

	risky := TierRisky
	risky.Jury = false
	timedTier := risky.Timed()
	jurors := strings.Split(cmpOr(os.Getenv("CODEFORGE_EVAL_JURORS"), "litellm/llmgateway/kimi-k3"), ",")
	one := func(m string, tier Tier) Reviewer {
		return EmpryoReviewer{Bin: bin, Model: m, Dir: dir, Tier: tier}
	}
	setups := []lsetup{
		{"alone", timedTier, AgentLimit, func(ctx context.Context, brief string) (string, error) {
			return one(model, timedTier).Review(ctx, brief)
		}},
		// What codeforge review does with a jury: the reviewer starts at once and never waits; the jurors run beside it
		// and their blocking findings are added unverified.
		{"parallel", timedTier, AgentLimit, func(ctx context.Context, brief string) (string, error) {
			rs := make([]Reviewer, len(jurors))
			for i, m := range jurors {
				rs[i] = one(m, timedTier)
			}
			var pipe Pipeline
			pipe.Start(ctx, dir, brief, rs, func() {})
			text, err := one(model, timedTier).Review(ctx, brief)
			res, _ := pipe.Wait(ctx)
			v, perr := ParseVerdict(text)
			if perr != nil {
				return text, err
			}
			for _, vote := range res.Votes {
				v.Blocking = append(v.Blocking, vote.Verdict.Blocking...)
			}
			out, merr := json.Marshal(v)
			if merr != nil {
				return text, fmt.Errorf("encoding the merged verdict: %w", merr)
			}
			return string(out), err
		}},
	}
	if only := os.Getenv("CODEFORGE_EVAL_SETUPS"); only != "" {
		setups = slices.DeleteFunc(setups, func(s lsetup) bool { return !slices.Contains(strings.Split(only, ","), s.name) })
	}
	results := map[string][]runResult{}
	for _, s := range setups {
		out := make([]runResult, runs)
		var wg sync.WaitGroup
		for i := range runs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				brief := PromptLocal(l, s.tier, nil) + Pack(t.Context(), ExecRunner{}, dir, l, s.tier)
				ctx, cancel := context.WithTimeout(t.Context(), s.timeout)
				defer cancel()
				start := time.Now()
				text, rerr := s.run(ctx, brief)
				r := runResult{elapsed: time.Since(start), err: rerr, tail: tail(text)}
				if v, perr := ParseVerdict(text); perr == nil {
					r.verdict, r.blocking = true, len(v.Blocking)
					r.score = ScoreBlocking(defects, v.Blocking)
				} else if r.err == nil {
					r.err = perr
				}
				out[i] = r
			}()
		}
		wg.Wait()
		results[s.name] = out
	}

	recall := map[string]float64{}
	var report strings.Builder
	for _, s := range setups {
		var found, verdicts int
		var spurious int
		var times []time.Duration
		missed := map[string]int{}
		for i, r := range results[s.name] {
			times = append(times, r.elapsed)
			if !r.verdict {
				t.Logf("%-5s run %d: no verdict after %s (%v) output ends: %q", s.name, i, r.elapsed.Round(time.Second), r.err, r.tail)
				continue
			}
			verdicts++
			found += len(r.score.Found)
			spurious += r.score.Spurious
			for _, m := range r.score.Missed {
				missed[m]++
			}
			t.Logf("%-5s run %d: found %d/%d, %d other blockers, %s", s.name, i, len(r.score.Found), len(defects), r.score.Spurious, r.elapsed.Round(time.Second))
		}
		slices.Sort(times)
		recall[s.name] = float64(found) / float64(len(defects)*runs)
		fmt.Fprintf(&report, "\n%-5s recall %.2f (%d/%d)  verdict rate %.2f (%d/%d)  other blockers %d  median %s  slowest %s",
			s.name, recall[s.name], found, len(defects)*runs, float64(verdicts)/float64(runs), verdicts, runs, spurious,
			times[len(times)/2].Round(time.Second), times[len(times)-1].Round(time.Second))
		for name, n := range missed {
			fmt.Fprintf(&report, "\n        missed %dx: %s", n, name)
		}
	}
	t.Log(report.String())

	// The gate applies when both setups ran: parallel is what codeforge review does, alone is the baseline it must not lose to.
	treated, base := results["parallel"], results["alone"]
	if treated == nil || base == nil {
		t.Log("a filtered run compares nothing: run both setups to apply the gate")
		return
	}
	var verdicts int
	for _, r := range treated {
		if r.verdict {
			verdicts++
		}
	}
	require.GreaterOrEqual(t, float64(verdicts)/float64(runs), minVerdictRate, "reviews must return a verdict")
	require.Positive(t, recall["alone"], "the baseline found nothing, so recall cannot be compared")
	require.GreaterOrEqual(t, recall["parallel"], recall["alone"]*recallFloor, "recall must stay within %.0f%% of the baseline", recallFloor*100)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
