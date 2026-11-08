//go:build eval

package review

// The interactive review: what `codeforge review` shows you, written by the codeforge-review profile and skill as
// Markdown with one `label: path:line — text` line per finding, rather than the gate's JSON verdict. Same large cases,
// same scoring, so the two can be compared:
//
//	CODEFORGE_EVAL_MODEL=provider/model go test -tags eval ./src/review -run TestEvalInteractiveReview -v -timeout 90m
//
// It reports recall over the findings labelled issue (what the review calls blocking) and over every labelled
// finding. CODEFORGE_EVAL_NO_AUDIT=1 runs it without the checklist (see applyEvalKnobs).
//
// Measured with Opus 5.5, 5 runs per case: issues recall 0.79 without the checklist and 0.88 with it (any label 0.85 and
// 0.91); "oldest stand-in chosen first" went from missed 5 of 5 to 2 of 5.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/effort"
)

// applyEvalKnobs reads the experiment switch: CODEFORGE_EVAL_NO_AUDIT=1 leaves the checklist out of the brief, to
// measure what it adds.
func applyEvalKnobs(t *testing.T) {
	t.Helper()
	old := withAudit
	t.Cleanup(func() { withAudit = old })
	if os.Getenv("CODEFORGE_EVAL_NO_AUDIT") != "" {
		withAudit = false
	}
	t.Logf("checklist %v", withAudit)
}

// interactiveReview runs one headless review the way codeforge review does, and returns its Markdown.
func interactiveReview(ctx context.Context, bin, model, dir, brief string, tier Tier) (string, error) {
	args := []string{
		"--headless", "--json", "--profile", "codeforge-review", "--mode", "plan", "--model", model,
		"--effort", tier.Effort, "--cwd", dir, "--max-steps", strconv.Itoa(tier.MaxSteps), "--marionette-mode", "none",
	}
	prompt := brief + stepBudget(tier.MaxSteps)
	if d, ok := ctx.Deadline(); ok {
		left := max(time.Until(d)-KillGrace, time.Second)
		args = append(args, "--timeout", strconv.FormatInt(left.Milliseconds(), 10))
		prompt += "\n\n" + TimeNote(left)
	}
	out, err := exec.CommandContext(ctx, bin, append(args, prompt)...).Output()
	var res effort.EmpryoResult
	if jerr := json.Unmarshal(out, &res); jerr != nil {
		return "", fmt.Errorf("run: %w (%w)", err, jerr)
	}
	if res.Error != "" {
		return res.Output, fmt.Errorf("run: %s", res.Error) //nolint:err113 // the engine's own message
	}
	return res.Output, nil
}

// labeledFindings reads every `label: path:line — text` finding of a Markdown review; issues are the blocking ones.
func labeledFindings(review string) (issues, all []Finding) {
	lines := strings.Split(review, "\n")
	for i, l := range lines {
		f, ok := ParseLabeled(l)
		if !ok {
			continue
		}
		what := f.Text
		for _, c := range lines[i+1 : LabeledEnd(lines, i)] {
			what += " " + strings.TrimSpace(c)
		}
		fd := Finding{File: f.Path, Line: f.Line, What: what}
		all = append(all, fd)
		if strings.HasPrefix(strings.ToLower(f.Label), "issue") {
			issues = append(issues, fd)
		}
	}
	return issues, all
}

func TestEvalInteractiveReview(t *testing.T) {
	model := os.Getenv("CODEFORGE_EVAL_MODEL")
	if model == "" {
		t.Skip("set CODEFORGE_EVAL_MODEL=provider/model")
	}
	applyEvalKnobs(t)
	bin := cmpOr(os.Getenv("CODEFORGE_EVAL_BIN"), "empryo")
	runs := 5
	if v, err := strconv.Atoi(os.Getenv("CODEFORGE_EVAL_RUNS")); err == nil && v > 0 {
		runs = v
	}
	risky := TierRisky
	risky.Jury = false
	tier := risky.Timed()

	var found, foundAny, chances, answered, total int
	missed := map[string]int{}
	for _, c := range largeCases {
		dir, defects, ok := buildLargeRepo(t, c)
		if !ok {
			continue
		}
		l, err := LoadLocal(t.Context(), dirRunner{dir: dir})
		require.NoError(t, err)
		brief := PromptLocal(l, Tier{}, nil) + Pack(t.Context(), ExecRunner{}, dir, l, tier)
		type result struct {
			text string
			err  error
		}
		out := make([]result, runs)
		var wg sync.WaitGroup
		for i := range runs {
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(t.Context(), AgentLimit)
				defer cancel()
				text, err := interactiveReview(ctx, bin, model, dir, brief, tier)
				out[i] = result{text, err}
			})
		}
		wg.Wait()
		caseFound := 0
		for i, r := range out {
			total++
			chances += len(defects)
			issues, all := labeledFindings(r.text)
			if len(all) == 0 {
				t.Logf("%s run %d: no findings (%v)", c.name, i, r.err)
				for _, d := range defects {
					missed[c.name+": "+d.Name]++
				}
				continue
			}
			answered++
			sc := ScoreBlocking(defects, issues)
			scAll := ScoreBlocking(defects, all)
			found += len(sc.Found)
			caseFound += len(sc.Found)
			foundAny += len(scAll.Found)
			for _, m := range scAll.Missed {
				missed[c.name+": "+m]++
			}
			if os.Getenv("CODEFORGE_EVAL_DUMP") != "" {
				for _, f := range all {
					t.Logf("%s finding %s:%d %s", c.name, f.File, f.Line, f.What)
				}
			}
		}
		t.Logf("%s: issues recall %.2f (%d/%d)", c.name, float64(caseFound)/float64(len(defects)*runs), caseFound, len(defects)*runs)
	}
	if total == 0 {
		t.Skip("none of the cases' commits are in this clone")
	}
	var report strings.Builder
	fmt.Fprintf(&report, "\ninteractive  issues recall %.2f (%d/%d)  any-label recall %.2f (%d/%d)  answered %d/%d",
		float64(found)/float64(chances), found, chances, float64(foundAny)/float64(chances), foundAny, chances, answered, total)
	for k, n := range missed {
		fmt.Fprintf(&report, "\n             missed %dx: %s", n, k)
	}
	t.Log(report.String())
}

// TestEvalAuditCoversSeededDefects checks, without a model, that every seeded defect of the large cases is on the
// checklist the brief carries: the defects are all operator and bound changes, the kind the checklist is for.
func TestEvalAuditCoversSeededDefects(t *testing.T) {
	for _, c := range largeCases {
		dir, _, ok := buildLargeRepo(t, c)
		if !ok {
			continue
		}
		l, err := LoadLocal(t.Context(), dirRunner{dir: dir})
		require.NoError(t, err)
		audit := AuditLines(l.Diff)
		lines := strings.Join(audit, "\n")
		t.Logf("%s: %d lines on the checklist", c.name, len(audit))
		for _, m := range c.mutations {
			if !checkedToken.MatchString(" " + m.to + " ") {
				t.Logf("%s: %s is not an operator or bound change; the checklist is not for it", c.name, m.defect.Name)
				continue
			}
			require.Contains(t, lines, strings.TrimSpace(m.to), "%s: %s is not on the checklist", c.name, m.defect.Name)
		}
	}
}
