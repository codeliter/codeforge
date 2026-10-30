package guard

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// CheckGH decides a real gh invocation at the binary boundary. It reuses the command rules on the exact argv
// gh received, so there is no shell text to misread: whatever built the call, these are the arguments that run.
func CheckGH(ctx context.Context, st State, cwd string, argv []string, now time.Time) error {
	words := append([]string{"gh"}, argv...)
	found, gate := classifyGH(words)
	if os.Getenv("GH_REPO") != "" && (len(found) > 0 || gate != gateNone) {
		return fmt.Errorf("%w:\n- GH_REPO points gh at another repository, where approvals and the gate do not apply", ErrGHRefused)
	}
	if len(found) == 0 && gate == gateNone {
		return nil
	}

	repo, err := RepoRoot(ctx, cwd)
	if err != nil {
		repo = cwd
	}

	var problems, spend []string
	for _, f := range found {
		switch {
		case Allowable(f.Rule) && st.Approved(repo, f.Rule, now):
			spend = append(spend, f.Rule)
		case Allowable(f.Rule):
			problems = append(problems, fmt.Sprintf("%s; approve once with `codeforge allow %s`", f.Reason, f.Rule))
		default:
			problems = append(problems, f.Reason)
		}
	}

	if gate == GateOpenPR && len(found) == 0 {
		head := gitHead(ctx, repo)
		switch {
		case st.Approved(repo, GateOpenPR, now):
			spend = append(spend, GateOpenPR)
		case gitDirty(ctx, repo):
			problems = append(problems, "the working tree has uncommitted changes; commit them and run `codeforge gate`")
		case head == "" || !st.CommitReviewed(repo, head):
			problems = append(problems, "the current commit has not passed the review gate; run `codeforge gate` first")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w:\n- %s", ErrGHRefused, strings.Join(problems, "\n- "))
	}
	for _, rule := range spend {
		st.Consume(repo, rule, now)
	}
	return nil
}

func gitHead(ctx context.Context, repo string) string {
	return gitOutput(ctx, repo, "rev-parse", "HEAD")
}

func gitDirty(ctx context.Context, repo string) bool {
	return gitOutput(ctx, repo, "status", "--porcelain", "--", ".", ":(exclude).empryo") != ""
}
