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

// Large-diff recall: the case the tiny repositories cannot test. Each case is a real multi-file diff from this
// repository's history with known bugs injected into it, so the step and time caps are under real pressure. Cases
// differ in size and in the kind of bug (flipped conditions, boundaries, inverted comparators, swapped operators).
//
//	CODEFORGE_EVAL_MODEL=provider/model go test -tags eval ./src/review -run TestEvalLargeDiff -v -timeout 90m
//
// Optional: CODEFORGE_EVAL_CASES (comma list of case names), CODEFORGE_EVAL_SETUPS (comma list of setup names),
// CODEFORGE_EVAL_DUMP=1 (log every blocking finding), CODEFORGE_EVAL_JURORS, CODEFORGE_EVAL_BIN, CODEFORGE_EVAL_RUNS
// (default 6 runs per setup and case, run in parallel).
// Scoring is a heuristic (ScoreBlocking credits each finding to the one defect it fits best), so recall is comparable
// between setups, not an absolute figure. Spurious blockers are not all false positives here: the real diffs have defects of their own that nobody labelled, so
// spurious is reported, never gated. Gated when both setups ran: across all cases, parallel returns a verdict in at
// least minVerdictRate of the runs and its recall stays at recallFloor of the lone reviewer's.

const minVerdictRate = 0.95

type mutation struct {
	file, from, to string
	defect         Defect
}

type largeCase struct {
	name       string
	base, head string
	mutations  []mutation
}

func mut(file string, line int, from, to, name string, keywords ...string) mutation {
	return mutation{file, from, to, Defect{Name: name, File: file, Lo: line, Hi: line, Keywords: keywords}}
}

var largeCases = []largeCase{
	{"gate-clock", "v0.10.3~8", "v0.10.3", []mutation{
		mut("src/review/clock.go", 64, "return minTime(time.Now().Add(AgentLimit), WorkDeadline(ctx))", "return minTime(time.Now().Add(AgentLimit), Deadline(ctx))", "agent end ignores the reserve", "reserve", "WorkDeadline"),
		mut("src/review/recall.go", 25, "f.Line >= d.Lo-lineSlack && f.Line <= d.Hi+lineSlack", "f.Line >= d.Lo-lineSlack || f.Line <= d.Hi+lineSlack", "line match always true", "always", "||", "any line"),
		mut("src/gate.go", 178, "after != tree || cleanHead(ctx, repo) != commit", "after != tree && cleanHead(ctx, repo) != commit", "pass recorded after HEAD moved", "&&", "HEAD", "commit changed"),
		mut("src/guard/lock.go", 55, "if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {", "if !errors.Is(err, syscall.EWOULDBLOCK) || !errors.Is(err, syscall.EINTR) {", "held lock fails instead of waiting", "EWOULDBLOCK", "||", "never waits", "held"),
		mut("src/review/panel.go", 50, "if v.Err != nil || v.Verdict.Verdict == \"\" {", "if v.Err != nil && v.Verdict.Verdict == \"\" {", "failed juror counted as a verdict", "&&", "v.Err", "error vote"),
		mut("src/reviewcmd.go", 147, "left := min(review.AgentLimit, time.Until(review.WorkDeadline(ctx))) - review.KillGrace", "left := max(review.AgentLimit, time.Until(review.WorkDeadline(ctx))) - review.KillGrace", "reviewer time exceeds the deadline", "max(", "exceed", "longer than"),
	}},
	{"provider-chains", "f726928~1", "80631c1", []mutation{
		mut("src/model_levels.go", 59, "if i < len(a) {", "if i <= len(a) {", "version compare reads past the end", "len(a)", "out of range", "panic", "<="),
		mut("src/model_levels.go", 98, "if (da <= 0) != (db <= 0) {", "if (da < 0) != (db <= 0) {", "equal version sorted as newer", "da < 0", "equal", "inconsistent"),
		mut("src/model_levels.go", 114, "if (sa.vendor == target.vendor) != (sb.vendor == target.vendor) {", "if (sa.vendor == target.vendor) == (sb.vendor == target.vendor) {", "vendor preference inverted", "==", "inverted", "same vendor", "comparator"),
		mut("src/model_levels.go", 120, "if c := compareVersions(sb.version, sa.version); c != 0 {", "if c := compareVersions(sa.version, sb.version); c != 0 {", "oldest stand-in chosen first", "newest", "order", "reversed", "swapped"),
		mut("src/models_delta.go", 23, "return providerID(a.Primary) == providerID(b.Primary) && slices.Equal(a.Backups(), b.Backups())", "return providerID(a.Primary) == providerID(b.Primary) || slices.Equal(a.Backups(), b.Backups())", "policy change goes unnoticed", "||", "same primary", "backups", "unnoticed"),
		mut("src/guard/rules.go", 266, "return len(args) == 0 || len(args) == 1 && (args[0] == \"show\" || args[0] == \"list\")", "return len(args) == 0 || len(args) >= 1 && (args[0] == \"show\" || args[0] == \"list\")", "read-only provider listing accepts extra arguments", ">= 1", "extra arguments", "read-only", "len(args)"),
	}},
	{"learn-habits", "540dd93~1", "c58165d", []mutation{
		mut("src/learn_habits.go", 248, "hasExit && (exit == 0) != ok {", "hasExit && (exit == 0) == ok {", "agreeing signals marked incomplete", "== ok", "disagree", "inverted", "incomplete"),
		mut("src/learn_habits.go", 358, "o.RevertedAt != nil || o.Status == store.StatusIncomplete {", "o.RevertedAt == nil || o.Status == store.StatusIncomplete {", "only reverted outcomes count", "RevertedAt", "reverted", "== nil", "skips"),
		mut("src/learn_habits.go", 135, "\"..\") || filepath.IsAbs(p) {", "\"..\") && filepath.IsAbs(p) {", "parent path or absolute path allowed", "&&", "..", "absolute", "traversal"),
		mut("src/learn_habits.go", 382, "h.Passed >= minHabitPasses", "h.Passed > minHabitPasses", "habit needs one pass more than the minimum", "minHabitPasses", "one more pass", "minimum"),
		mut("src/learn_habits.go", 388, "out = out[:maxHabits]", "out = out[:maxHabits-1]", "one habit too few kept", "maxHabits", "one fewer", "-1", "cap"),
	}},
}

// buildLargeRepo makes the case's repository; it reports false when the case's commits are not in this clone, so one
// missing case skips only itself.
func buildLargeRepo(t *testing.T, c largeCase) (string, []Defect, bool) {
	t.Helper()
	top, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	root := strings.TrimSpace(string(top))
	for _, ref := range []string{c.base, c.head} {
		if err := exec.CommandContext(t.Context(), "git", "-C", root, "rev-parse", "--verify", "-q", ref).Run(); err != nil {
			t.Logf("%s: %s is not in this clone, case skipped", c.name, ref)
			return "", nil, false
		}
	}
	dir := t.TempDir()
	out, err := exec.CommandContext(t.Context(), "git", "clone", "-q", "--no-checkout", root, dir).CombinedOutput()
	require.NoError(t, err, string(out))
	gitIn(t, dir, "remote", "remove", "origin")
	gitIn(t, dir, "checkout", "-q", "-B", "main", c.base)
	gitIn(t, dir, "checkout", "-q", "-B", "feature", c.head)
	gitIn(t, dir, "reset", "-q", "--soft", "main")
	var defects []Defect
	for _, m := range c.mutations {
		p := dir + "/" + m.file
		body, err := os.ReadFile(p)
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(string(body), m.from), "%s: the injected line must exist exactly once at %s", m.file, c.head)
		require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(body), m.from, m.to, 1)), 0o600))
		defects = append(defects, m.defect)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "change")
	return dir, defects, true
}

// retrying repeats a run the engine failed outright when there is time left, like the gate's jury does; many reviewers
// in parallel make the engine fail fast now and then, and that is not the reviewer's recall.
type retrying struct{ Reviewer }

func (r retrying) Review(ctx context.Context, brief string) (string, error) {
	out, err := r.Reviewer.Review(ctx, brief)
	if err != nil && retryableRun(ctx, out, err) {
		return r.Reviewer.Review(ctx, brief)
	}
	return out, err
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
	jurors := strings.Split(cmpOr(os.Getenv("CODEFORGE_EVAL_JURORS"), "litellm-llmgateway/llmgateway/gpt-6.1-sol"), ",")
	risky := TierRisky
	risky.Jury = false
	timedTier := risky.Timed()
	cases := largeCases
	if only := os.Getenv("CODEFORGE_EVAL_CASES"); only != "" {
		cases = slices.DeleteFunc(slices.Clone(cases), func(c largeCase) bool { return !slices.Contains(strings.Split(only, ","), c.name) })
	}

	type total struct {
		found, chances, verdicts, runs, spurious int
		times                                    []time.Duration
		missed                                   map[string]int
	}
	totals := map[string]*total{}
	for _, c := range cases {
		dir, defects, ok := buildLargeRepo(t, c)
		if !ok {
			continue
		}
		l, err := LoadLocal(t.Context(), dirRunner{dir: dir})
		require.NoError(t, err)
		t.Logf("%s: %d files, %d bytes of diff, %d seeded defects", c.name, len(l.Files), len(l.Diff), len(defects))

		one := func(m string, tier Tier) Reviewer {
			return EmpryoReviewer{Bin: bin, Model: m, Dir: dir, Tier: tier}
		}
		// The main reviewer is retried here; the jurors are not, because Jury already retries them.
		main := func() Reviewer { return retrying{one(model, timedTier)} }
		setups := []lsetup{
			{"alone", timedTier, AgentLimit, func(ctx context.Context, brief string) (string, error) {
				return main().Review(ctx, brief)
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
				text, err := main().Review(ctx, brief)
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
						if os.Getenv("CODEFORGE_EVAL_DUMP") != "" {
							for _, b := range v.Blocking {
								t.Logf("%s/%s finding %s:%d %s", c.name, s.name, b.File, b.Line, b.What)
							}
						}
					} else if r.err == nil {
						r.err = perr
					}
					out[i] = r
				}()
			}
			wg.Wait()

			a := totals[s.name]
			if a == nil {
				a = &total{missed: map[string]int{}}
				totals[s.name] = a
			}
			var found int
			for i, r := range out {
				a.runs++
				a.chances += len(defects)
				a.times = append(a.times, r.elapsed)
				if !r.verdict {
					t.Logf("%s/%s run %d: no verdict after %s (%v) output ends: %q", c.name, s.name, i, r.elapsed.Round(time.Second), r.err, r.tail)
					for _, d := range defects {
						a.missed[c.name+": "+d.Name]++
					}
					continue
				}
				a.verdicts++
				found += len(r.score.Found)
				a.found += len(r.score.Found)
				a.spurious += r.score.Spurious
				for _, m := range r.score.Missed {
					a.missed[c.name+": "+m]++
				}
			}
			t.Logf("%s/%-8s recall %.2f (%d/%d)", c.name, s.name, float64(found)/float64(len(defects)*runs), found, len(defects)*runs)
		}
	}

	if len(totals) == 0 {
		t.Skip("none of the cases' commits are in this clone")
	}
	var report strings.Builder
	recall := map[string]float64{}
	for _, name := range []string{"alone", "parallel"} {
		a := totals[name]
		if a == nil {
			continue
		}
		slices.Sort(a.times)
		recall[name] = float64(a.found) / float64(a.chances)
		fmt.Fprintf(&report, "\n%-8s recall %.2f (%d/%d)  verdict rate %.2f (%d/%d)  other blockers %d  median %s  slowest %s",
			name, recall[name], a.found, a.chances, float64(a.verdicts)/float64(a.runs), a.verdicts, a.runs, a.spurious,
			a.times[len(a.times)/2].Round(time.Second), a.times[len(a.times)-1].Round(time.Second))
		keys := make([]string, 0, len(a.missed))
		for k := range a.missed {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(&report, "\n          missed %dx: %s", a.missed[k], k)
		}
	}
	t.Log(report.String())

	// The gate applies when both setups ran: parallel is what codeforge review does, alone is the baseline it must not lose to.
	treated, base := totals["parallel"], totals["alone"]
	if treated == nil || base == nil {
		t.Log("a filtered run compares nothing: run both setups to apply the gate")
		return
	}
	require.GreaterOrEqual(t, float64(treated.verdicts)/float64(treated.runs), minVerdictRate, "reviews must return a verdict")
	require.Positive(t, recall["alone"], "the baseline found nothing, so recall cannot be compared")
	require.GreaterOrEqual(t, recall["parallel"], recall["alone"]*recallFloor, "recall must stay within %.0f%% of the baseline", recallFloor*100)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
