package guard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const zeroSHA = "0000000000000000000000000000000000000000"

type PushUpdate struct {
	LocalRef  string
	LocalSHA  string
	RemoteRef string
	RemoteSHA string
}

// ParsePushUpdates reads the lines git gives a pre-push hook on stdin.
func ParsePushUpdates(r io.Reader) []PushUpdate {
	var out []PushUpdate
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		const fields = 4
		if len(f) != fields {
			continue
		}
		out = append(out, PushUpdate{LocalRef: f[0], LocalSHA: f[1], RemoteRef: f[2], RemoteSHA: f[3]})
	}
	return out
}

// CheckPush decides a push at git's own pre-push boundary, however the push was started: shell, alias,
// script or interpreter. Every pushed commit must be the one the gate reviewed on a clean tree,
// deletions and non-fast-forward updates need an explicit approval, and approvals are spent only on success.
func CheckPush(ctx context.Context, st State, repo string, updates []PushUpdate, now time.Time) error {
	if len(updates) == 0 {
		return nil
	}

	var problems, spend []string
	for _, u := range updates {
		switch {
		case u.LocalSHA == zeroSHA:
			if st.Approved(repo, RuleDeleteBranch, now) {
				spend = append(spend, RuleDeleteBranch)
				continue
			}
			problems = append(problems, fmt.Sprintf("deletes %s; approve once with `codeforge allow %s`", u.RemoteRef, RuleDeleteBranch))
			continue

		case u.RemoteSHA != zeroSHA && !isAncestor(ctx, repo, u.RemoteSHA, u.LocalSHA):
			if !st.Approved(repo, RuleForcePush, now) {
				problems = append(problems, fmt.Sprintf("rewrites %s (force push); approve once with `codeforge allow %s`", u.RemoteRef, RuleForcePush))
				continue
			}
			spend = append(spend, RuleForcePush)
		}

		// An annotated tag points at a tag object; what it publishes is the commit underneath.
		commit := gitOutput(ctx, repo, "rev-parse", "--verify", "--quiet", u.LocalSHA+"^{commit}")
		if commit == "" {
			commit = u.LocalSHA
		}

		if st.CommitReviewed(repo, commit) || st.Approved(repo, GatePush, now) {
			if !st.CommitReviewed(repo, commit) {
				spend = append(spend, GatePush)
			}
			continue
		}
		problems = append(problems, fmt.Sprintf("%s (%s) has not passed the review gate; commit everything, run `codeforge gate`, then push again", u.LocalRef, short(u.LocalSHA)))
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w:\n- %s", ErrPushRefused, strings.Join(problems, "\n- "))
	}
	for _, rule := range spend {
		st.Consume(repo, rule, now)
	}
	return nil
}

var (
	ErrPushRefused = errors.New("codeforge refused the push")
	ErrGHRefused   = errors.New("codeforge refused this gh command")
)

func gitOutput(ctx context.Context, repo string, args ...string) string {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func isAncestor(ctx context.Context, repo, old, next string) bool {
	return exec.CommandContext(ctx, "git", "-C", repo, "merge-base", "--is-ancestor", old, next).Run() == nil
}

func short(sha string) string {
	const n = 8
	if len(sha) <= n {
		return sha
	}
	return sha[:n]
}
