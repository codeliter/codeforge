package src

import (
	"context"
	"fmt"
	"strings"

	"github.com/codeliter/codeforge/src/effort"
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

// reviewArgs turns `codeforge review [pr] [--repo owner/name] [empryo flags]` into an Empryo launch.
// With no PR it reviews the current branch and uncommitted work against the base branch.
func reviewArgs(ctx context.Context, rc ReviewConfig, judges []review.Judge, args []string) ([]string, error) {
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

	if !hasEffort(rest) {
		rest = append([]string{"--effort", "high"}, rest...)
	}
	if !hasFlag(rest, "--model") {
		rest = append([]string{"--model", rc.Model}, rest...)
	}

	if ref == "" && repo == "" {
		l, err := review.LoadLocal(ctx, review.ExecRunner{})
		if err != nil {
			return nil, fmt.Errorf("loading local work: %w", err)
		}
		tier, _ := review.Classify(ctx, judges, l.Diff, l.Files)
		return append(rest, review.PromptLocal(l, tier, review.JuryFor(tier, rc.SecondOpinion))), nil
	}

	number, err := review.ParseRef(ref)
	if err != nil {
		return nil, fmt.Errorf("usage: codeforge review [number|url] [--repo owner/name]: %w", err)
	}
	if repo == "" {
		repo = repoFromURL(ref)
	}

	b, err := review.Load(ctx, review.ExecRunner{}, repo, number)
	if err != nil {
		return nil, fmt.Errorf("loading PR %s: %w", number, err)
	}
	tier, _ := review.Classify(ctx, judges, b.Diff, b.Files())
	return append(rest, review.Prompt(b, tier, review.JuryFor(tier, rc.SecondOpinion))), nil
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
