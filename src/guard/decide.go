package guard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Decision struct {
	Allow    bool
	Reason   string
	Consumed bool
}

// Decide combines the rules with the user's approvals and the review gate.
// Gated actions (push, opening a PR) need a review gate pass on the current tree, or an explicit allow.
func Decide(ctx context.Context, st State, in Input, readOnly bool, now time.Time) Decision {
	res := CheckTool(in, readOnly)
	if len(res.Blocked) == 0 && res.Gate == gateNone {
		return Decision{Allow: true}
	}

	repo, err := targetRepo(ctx, in.Cwd, res.Dirs)
	if err != nil {
		return Decision{Reason: "codeforge blocked this: " + err.Error() + ". Run guarded commands from inside the repository they change."}
	}

	// The hook runs before the whole command, so a push must stand alone and cover only the reviewed HEAD;
	// otherwise edits or commits earlier in the same command would be published unreviewed.
	if res.Gate != gateNone {
		res.Blocked = append(res.Blocked, pushScope(ctx, repo, res)...)
	}

	if f := bypassesShim(res); f != nil {
		return Decision{Reason: "codeforge blocked this: " + f.Reason + "."}
	}

	// Approvals are only spent when the whole action goes through, and each one covers a single action,
	// so `gh pr merge 1 && gh pr merge 2` needs two approvals.
	var reasons, spend []string
	seen := map[string]int{}
	for _, f := range res.Blocked {
		seen[f.Rule]++
		switch {
		case f.Rule == RuleReadOnly:
			reasons = append(reasons, f.Reason+": review mode is read-only. Switch to ticket or chat mode to change files.")
		case f.Rule == RuleSelf:
			reasons = append(reasons, f.Reason+". Only the user can do this, outside the agent.")
		case seen[f.Rule] == 1 && st.Approved(repo, f.Rule, now):
			spend = append(spend, f.Rule)
		case seen[f.Rule] > 1:
			reasons = append(reasons, fmt.Sprintf("%s (%s) appears more than once in one command; one approval covers one action. Run them separately.", f.Reason, f.Rule))
		case !Allowable(f.Rule):
			reasons = append(reasons, fmt.Sprintf("%s (%s).", f.Reason, f.Rule))
		default:
			reasons = append(reasons, fmt.Sprintf("%s (%s). The user can approve one with `codeforge allow %s`.", f.Reason, f.Rule, f.Rule))
		}
	}

	if res.Gate != gateNone && st.Approved(repo, res.Gate, now) {
		spend = append(spend, res.Gate)
	} else if res.Gate != gateNone {
		tree, err := TreeID(ctx, repo)
		if err != nil || !st.GatePassed(repo, tree) {
			reasons = append(reasons, fmt.Sprintf(
				"%s needs a passing review of the current changes in %s first. Run `codeforge gate` there, fix every Blocking finding, then try again. The user can skip this once with `codeforge allow %s`.",
				res.Gate, repo, res.Gate))
		}
	}

	if len(reasons) > 0 {
		return Decision{Reason: "codeforge blocked this: " + strings.Join(reasons, " ")}
	}

	// Push and gh approvals are spent by git's pre-push hook and the gh shim; spending them here too would
	// leave nothing for the real check. Every other rule has no later boundary, so it is spent now.
	consumed := false
	for _, rule := range spend {
		if !res.Deferred[rule] {
			st.Consume(repo, rule, now)
			consumed = true
		}
	}
	return Decision{Allow: true, Consumed: consumed}
}

// pushScope keeps a push or a new PR to exactly what the gate reviewed: the committed HEAD of the current branch.
func pushScope(ctx context.Context, repo string, res Result) []Finding {
	git := func(args ...string) string {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}

	var out []Finding
	if res.OtherWrites {
		out = append(out, Finding{RuleMixedPush, "the push shares a command with other changes, so it would publish work the gate has not seen; run the push on its own"})
	}
	if git("status", "--porcelain", "--", ".", ":(exclude).empryo") != "" {
		out = append(out, Finding{RuleMixedPush, "the working tree has uncommitted changes, so the gate's review does not match what would be pushed; commit or discard them and run `codeforge gate` again"})
	}

	current := git("branch", "--show-current")
	for _, ref := range res.PushRefs {
		if current == "" || !pushesHead(ref, current) {
			out = append(out, Finding{RulePushScope, "pushes " + ref + ", but the gate reviewed only the current branch"})
		}
	}

	if len(res.PushRefs) == 0 && implicitWidePush(git) {
		out = append(out, Finding{RulePushScope, "git is configured to push more than the current branch (push.default=matching or a remote push refspec); name the branch explicitly, e.g. `git push origin HEAD`"})
	}
	return out
}

func implicitWidePush(git func(...string) string) bool {
	return git("config", "--get", "push.default") == "matching" ||
		git("config", "--get-regexp", `^remote\..*\.push$`) != "" ||
		git("config", "--get-regexp", `^remote\..*\.mirror$`) != ""
}

// pushesHead accepts refspecs whose source is the checked-out branch.
func pushesHead(ref, current string) bool {
	src, _, _ := strings.Cut(strings.TrimPrefix(ref, "+"), ":")
	return src == "HEAD" || src == current || src == "refs/heads/"+current
}

// AllowRules are the rules a user may approve once with `codeforge allow`.
var AllowRules = []string{
	RuleForcePush, RuleNoVerify, RuleMerge, RulePRComment, RuleInfra, RuleWholeTree, RuleDeleteBranch, GatePush, GateOpenPR,
}

func Allowable(rule string) bool {
	return slices.Contains(AllowRules, rule)
}

// bypassesShim refuses gh run by path for guarded actions, because only the shim on PATH enforces them.
func bypassesShim(res Result) *Finding {
	if !res.DirectGH || (len(res.Blocked) == 0 && res.Gate == gateNone) {
		return nil
	}
	return &Finding{RuleOpaque, "gh is called by path, which skips codeforge's gh check; call it as plain `gh`"}
}

var errAmbiguousTarget = errors.New("guarded commands target more than one repository or one that cannot be resolved")

// targetRepo resolves the single repository a command acts on, following `cd` and `git -C`.
// A command that spans repositories, or names a directory that cannot be resolved, is refused.
func targetRepo(ctx context.Context, cwd string, dirs []string) (string, error) {
	if len(dirs) == 0 {
		dirs = []string{""}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	repo := ""
	for _, d := range dirs {
		if strings.Contains(d, "$") && !strings.HasPrefix(d, "$HOME") {
			return "", errAmbiguousTarget
		}
		dir := expand(d, home)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}

		root, err := RepoRoot(ctx, dir)
		if err != nil {
			root = filepath.Clean(dir)
		}
		if repo != "" && root != repo {
			return "", errAmbiguousTarget
		}
		repo = root
	}
	return repo, nil
}
