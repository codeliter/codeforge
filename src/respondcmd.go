package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/review"
)

var (
	errNoFeedback = errors.New("no open review comments on this branch's PR")
	errOtherRepo  = errors.New("respond fixes code in this checkout")
)

// currentRepo uses the same GH_REPO-free runner as feedback loading, so both see this checkout's repository.
func currentRepo(ctx context.Context) string {
	out, err := review.ExecRunner{}.Run(ctx, "gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

const watchInterval = 2 * time.Minute

// respondArgs launches ticket mode on the open review feedback for the current branch's PR.
// --print writes the brief instead, for the agent to call from inside a session.
// --watch polls until new feedback arrives, then launches.
func respondArgs(ctx context.Context, args []string) ([]string, bool, error) {
	printOnly := slices.Contains(args, "--print")
	watch := slices.Contains(args, "--watch")
	rest := slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--print" || a == "--watch" })

	number := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		n, err := review.ParseRef(rest[0])
		if err != nil {
			return nil, false, fmt.Errorf("usage: codeforge respond [PR number or URL] [--watch]: %w", err)
		}
		if other := repoFromURL(rest[0]); other != "" && other != currentRepo(ctx) {
			return nil, false, fmt.Errorf("%w: %s is not this repository; run respond inside its checkout", errOtherRepo, other)
		}
		number, rest = n, rest[1:]
	}

	fb, err := review.LoadFeedback(ctx, review.ExecRunner{}, number)
	seen := fb.Key()
	for watch && err == nil && (fb.Empty() || fb.Key() == seen) {
		fmt.Fprintf(os.Stderr, "codeforge: no new review comments on PR #%d; checking again in %s (Ctrl-C to stop)\n", fb.Number, watchInterval)
		select {
		case <-ctx.Done():
			return nil, false, fmt.Errorf("watch stopped: %w", ctx.Err())
		case <-time.After(watchInterval):
		}
		fb, err = review.LoadFeedback(ctx, review.ExecRunner{}, number)
	}
	if err != nil {
		return nil, false, fmt.Errorf("loading reviews: %w", err)
	}
	if fb.Empty() {
		return nil, false, fmt.Errorf("%w (PR #%d)", errNoFeedback, fb.Number)
	}

	brief := review.PromptFeedback(fb)
	if printOnly {
		fmt.Print(brief)
		return nil, true, nil
	}

	if err := checkEffort(rest); err != nil {
		return nil, false, err
	}
	if !hasEffort(rest) {
		rest = append([]string{"--effort", "medium"}, rest...)
	}
	return append(rest, brief), false, nil
}
