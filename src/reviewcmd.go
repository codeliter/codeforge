package src

import (
	"context"
	"fmt"
	"strings"

	"github.com/codeliter/codeforge/src/review"
)

// reviewArgs turns `codeforge review [pr] [--repo owner/name] [empryo flags]` into an Empryo launch.
// With no PR it reviews the current branch and uncommitted work against the base branch.
func reviewArgs(ctx context.Context, rc ReviewConfig, args []string) ([]string, error) {
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
		return append(rest, review.PromptLocal(l, rc.SecondOpinion)), nil
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
	return append(rest, review.Prompt(b, rc.SecondOpinion)), nil
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
