package src

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/effort"
	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/review"
)

// reviewJudgeSteps gives a classifier judge room for one tool call and its answer; with one step a judge that
// answered through a final tool call was reported as failed even though its answer was complete.
const reviewJudgeSteps = 2

func judgesFor(bin string, models []string, sink func(effort.Usage)) []review.Judge {
	judges := make([]review.Judge, len(models))
	for i, m := range models {
		judges[i] = effort.EmpryoJudge{Bin: bin, Model: m, Sink: sink, Steps: reviewJudgeSteps}
	}
	return judges
}

// jurorsFor builds read-only jurors. They run in the repository the review was started from, or, when the change under
// review is not in this checkout, in an empty scratch directory: a juror must never read, or ground its findings in, a
// tree that is not the change. The returned cleanup removes the scratch directory.
func jurorsFor(bin string) jurorFactory {
	return func(ctx context.Context, models []string, tier review.Tier, scratch bool) ([]review.Reviewer, func()) {
		noop := func() {}
		dir, cleanup := "", noop
		if scratch {
			tmp, err := os.MkdirTemp("", "codeforge-jurors-")
			if err != nil {
				return nil, noop
			}
			dir, cleanup = tmp, func() { _ = os.RemoveAll(tmp) }
		} else {
			wd, err := os.Getwd()
			if err != nil {
				return nil, noop
			}
			if dir, err = guard.RepoRoot(ctx, wd); err != nil {
				dir = wd
			}
		}
		out := make([]review.Reviewer, len(models))
		for i, m := range models {
			out[i] = review.EmpryoReviewer{Bin: bin, Model: m, Dir: dir, Tier: tier, Wrap: sandboxCommand}
		}
		return out, cleanup
	}
}

// jurorFactory builds the independent jurors for a tier. Nil means the review has no panel.
type jurorFactory func(ctx context.Context, models []string, tier review.Tier, scratch bool) ([]review.Reviewer, func())

// panelBrief runs the second-opinion jurors in parallel, each on the shared clock, and returns their findings for the
// reviewer to verify. scratch is set when the change is not in this checkout. Without a panel the brief carries no jury
// wording at all when the jury cannot run in time, and the old dispatch wording when there is simply no factory.
func panelBrief(ctx context.Context, rc ReviewConfig, jurors jurorFactory, tier review.Tier, scratch bool, brief func(tier review.Tier, jury []string) string) string {
	models := review.JuryFor(tier, rc.SecondOpinion)
	if jurors == nil || len(models) == 0 {
		return brief(tier, models)
	}
	// Without a jury wording of its own the brief never tells the reviewer to dispatch jurors it has no time for.
	plainTier := tier.Panel()
	window := min(review.PanelWindow, max(time.Until(review.PanelDeadline(ctx))-review.MinRun-review.KillGrace, 0))
	if window < review.MinRun {
		return brief(plainTier, nil)
	}
	reviewers, cleanup := jurors(ctx, models, tier.Timed(), scratch)
	defer cleanup()
	if len(reviewers) == 0 {
		return brief(plainTier, nil)
	}
	repoDir := ""
	if first, ok := reviewers[0].(review.EmpryoReviewer); ok {
		repoDir = first.Dir
	}
	plain := brief(plainTier, nil)
	return plain + "\n\n" + review.Panel{Repo: repoDir, Reviewers: reviewers}.Run(ctx, plain, window)
}

// clampFlag sets --name to limit unless the caller asked for something stricter, in either --name v or --name=v form.
func clampFlag(args []string, name string, limit int) []string {
	value := limit
	if n, err := strconv.Atoi(flagValue(args, name)); err == nil && n > 0 {
		value = min(n, limit)
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		key, _, equal := strings.Cut(args[i], "=")
		if key == name {
			if !equal {
				i++
			}
			continue
		}
		out = append(out, args[i])
		if flagsWithValue[key] && !equal && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	return append(out, name, strconv.Itoa(value))
}

// reviewArgs turns `codeforge review [pr] [--repo owner/name] [empryo flags]` into an Empryo launch.
// With no PR it reviews the current branch and uncommitted work against the base branch.
func reviewArgs(ctx context.Context, bin string, rc ReviewConfig, judges []review.Judge, jurors jurorFactory, args []string) ([]string, error) {
	var ref, repo string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--repo" && i+1 < len(args):
			repo = args[i+1]
			i++
		case flagsWithValue[args[i]] && i+1 < len(args):
			rest = append(rest, args[i], args[i+1])
			i++
		case ref == "" && !strings.HasPrefix(args[i], "-"):
			ref = args[i]
		default:
			rest = append(rest, args[i])
		}
	}

	if err := checkEffort(rest); err != nil {
		return nil, err
	}

	userEffort := hasEffort(rest)
	if !hasFlag(rest, "--model") {
		rest = append([]string{"--model", rc.Model}, rest...)
	}

	// The clock starts in Run. Collecting the change gets a slice of it and the reviewer gets what is left before the
	// reserve, so a review ends inside review.HardLimit however slow gh or the model is.
	// Limits the caller passed are clamped to the bound, never trusted: a stricter one stays, a looser one is lowered.
	bound := func(rest []string, tier review.Tier) ([]string, string) {
		left := max(time.Until(review.PanelDeadline(ctx))-review.KillGrace, review.MinRun)
		timed := tier.Timed()
		limits := timed.Limits(left)
		for i := 0; i+1 < len(limits); i += 2 {
			if n, err := strconv.Atoi(limits[i+1]); err == nil {
				rest = clampFlag(rest, limits[i], n)
			}
		}
		if !userEffort {
			rest = append([]string{"--effort", review.EffortFor(ctx, bin, flagValue(rest, "--model"), cmp.Or(timed.Effort, "medium"))}, rest...)
		}
		return rest, review.TimeNote(left)
	}

	if ref == "" && repo == "" {
		lctx, cancel := context.WithTimeout(ctx, review.LoadLimit)
		defer cancel()
		l, err := review.LoadLocal(lctx, review.ExecRunner{})
		if err != nil {
			return nil, fmt.Errorf("loading local work: %w", err)
		}
		// Classification has its own slice: a slow load must not leave the paid judges nothing.
		cctx, ccancel := context.WithTimeout(ctx, review.LoadLimit)
		defer ccancel()
		tier, _ := review.Classify(cctx, judges, l.Diff, l.Files)
		prompt := panelBrief(ctx, rc, jurors, tier, false, func(t review.Tier, jury []string) string { return review.PromptLocal(l, t, jury) })
		bounded, note := bound(rest, tier)
		return append(bounded, prompt+"\n\n"+note), nil
	}

	number, err := review.ParseRef(ref)
	if err != nil {
		return nil, fmt.Errorf("usage: codeforge review [number|url] [--repo owner/name]: %w", err)
	}
	if repo == "" {
		repo = repoFromURL(ref)
	}

	lctx, cancel := context.WithTimeout(ctx, review.LoadLimit)
	defer cancel()
	b, err := review.Load(lctx, review.ExecRunner{}, repo, number)
	if err != nil {
		return nil, fmt.Errorf("loading PR %s: %w", number, err)
	}
	cctx, ccancel := context.WithTimeout(ctx, review.LoadLimit)
	defer ccancel()
	tier, _ := review.Classify(cctx, judges, b.Diff, b.Files())
	prompt := panelBrief(ctx, rc, jurors, tier, b.HeadRef == "", func(t review.Tier, jury []string) string { return review.Prompt(b, t, jury) })
	rest, note := bound(rest, tier)
	return append(rest, prompt+"\n\n"+note), nil
}

// withSystemNote appends note to the run's system prompt, or adds one. Review sessions carry the time limit this way,
// so the model knows it in the interactive session too, where Empryo has no flag to enforce it.
func withSystemNote(args []string, note string) []string {
	for i := 0; i < len(args); i++ {
		key, value, equal := strings.Cut(args[i], "=")
		if key == "--system" && equal {
			out := slices.Clone(args)
			out[i] = key + "=" + value + "\n\n" + note
			return out
		}
		if key == "--system" && i+1 < len(args) {
			out := slices.Clone(args)
			out[i+1] += "\n\n" + note
			return out
		}
		if flagsWithValue[key] && !equal {
			i++
		}
	}
	return append([]string{"--system", note}, args...)
}

func repoFromURL(ref string) string {
	rest, ok := strings.CutPrefix(ref, "https://github.com/")
	if !ok {
		return ""
	}

	owner, rest, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}
