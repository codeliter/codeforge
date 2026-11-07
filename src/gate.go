package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/config"

	"github.com/codeliter/codeforge/assets"
	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/review"

	"github.com/codeliter/codeforge/src/effort"
)

var errGateFailed = errors.New("review gate failed")

func stateDir(home string) string {
	return config.Dir(home)
}

// runGate reviews the current branch and uncommitted work and records a pass. It is voluntary: nothing,
// pushing and opening a PR included, waits for it.
func runGate(ctx context.Context, home, bin string, cfg config.Config, w io.Writer) error {
	// The gate is a review, so it has the same hard limit as codeforge review: one clock for collecting, classifying,
	// the reviewers and the tests. The reserve after the panel deadline is for the tree checks and recording the pass.
	end := time.Now().Add(review.HardLimit)
	ctx, cancel := context.WithDeadline(review.WithDeadline(ctx, end), end)
	defer cancel()

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding working directory: %w", err)
	}
	repo, err := guard.RepoRoot(ctx, wd)
	if err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	// The commit is read before the review and checked again after it, so a pass can never name a later HEAD.
	commit := cleanHead(ctx, repo)
	tree, err := guard.TreeID(ctx, repo)
	if err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	local, err := review.LoadLocal(ctx, review.ExecRunner{})
	if err != nil {
		return fmt.Errorf("collecting changes: %w", err)
	}

	// A follow-up round after blocking findings sends the findings and only the files changed since, not the whole pack,
	// and keeps the tier the earlier round was judged at instead of paying the judges to size the same work again.
	usage := &gateUsage{}
	rounds := review.Rounds{Dir: filepath.Join(config.Dir(home), "review-rounds")}
	roundKey := review.RoundKey(repo, local.Branch, local.Base)
	digests := review.FileDigests(repo, local.Files)
	packed, note := local, ""
	var tier review.Tier
	var why string
	followUp := false
	if prev, ok := rounds.Load(roundKey, time.Now()); ok && len(prev.Blocking) > 0 && prev.Tree != tree {
		if changed := prev.ChangedSince(digests); len(changed) > 0 && len(changed) < len(local.Files) {
			packed.Files = changed
			packed.Diff = review.FilterDiff(local.Diff, changed)
			note = review.DeltaNote(prev, changed)
			followUp = true
			fmt.Fprintf(w, "codeforge gate: follow-up round, %d of %d files changed since the last review\n", len(changed), len(local.Files))
			if t, ok := review.Reuse(prev.Tier, local.Diff, local.Files); ok {
				tier, why = t, "tier kept from the previous round"
			}
		}
	}

	// Cheap judges read the change and size the review; simple changes get one reviewer, risky ones the jury.
	if tier.Name == "" {
		classes := review.TierCache{Dir: filepath.Join(config.Dir(home), "review-cache")}
		classKey := review.CacheKey(tree, strings.Join(cfg.Judges, ","), assets.ReviewRubric)
		var seen bool
		if tier, why, seen = classes.Get(classKey, time.Now()); !seen {
			tier, why = review.Classify(ctx, judgesFor(bin, cfg.Judges, usage.add), local.Diff, local.Files)
			classes.Put(classKey, tier, why, time.Now())
		}
	}
	tier, why = review.RaiseForPaths(tier, why, local.Files, cfg.Review.SensitivePaths)
	if why != "" {
		fmt.Fprintf(w, "codeforge gate: %s tier: %s\n", tier.Name, why)
	}
	if advice := review.SizeNote(len(local.Diff)); advice != "" {
		fmt.Fprintln(w, advice)
	}
	timed := tier.Timed()
	models := slices.Concat([]string{reviewerFor(tier, cfg.Review)}, review.JuryFor(tier, cfg.Review.SecondOpinion))
	reviewers := make([]review.Reviewer, len(models))
	for i, m := range models {
		reviewers[i] = review.EmpryoReviewer{Bin: bin, Model: m, Dir: repo, Tier: timed, Wrap: sandboxCommand, Sink: usage.add}
	}

	brief := review.PromptLocal(local, tier, nil) + note + review.Pack(ctx, review.ExecRunner{}, repo, packed, tier)
	rubric := review.CacheKey(assets.Gate, review.VerdictInstructions, tier.Name, strconv.FormatBool(followUp))
	opts := review.JuryOptions{
		Cache:    &review.Cache{Dir: filepath.Join(config.Dir(home), "review-cache")},
		CacheKey: func(model string) string { return review.CacheKey(tree, model, rubric) },
		// Every reviewer finishes: cancelling the others at the first blocker is why findings arrived one round at a time.
		StopEarly: false,
		Now:       time.Now(),
	}
	opts.Cache.Prune(opts.Now)

	start := time.Now()
	fmt.Fprintf(w, "codeforge gate: %s tier (%s effort), %d reviewers on %s vs %s: %s\n",
		tier.Name, tier.Effort, len(reviewers), orBranch(local.Branch), local.Base, strings.Join(models, ", "))

	// The tests need no verdict from anyone, so they run while the reviewers work instead of after them. The reviewers
	// are read-only and the tree is re-checked after the review, so nothing they do can change what the tests see.
	panelCtx, stopPanel := context.WithDeadline(ctx, review.AgentEnd(ctx))
	defer stopPanel()
	verifyCtx, stopVerify := context.WithCancel(panelCtx)
	defer stopVerify()
	verified := make(chan []review.Finding, 1)
	go func() { verified <- review.Verify(verifyCtx, review.ExecRunner{}, repo, local.Files) }()

	res, err := review.Jury(panelCtx, repo, brief, reviewers, opts)
	// Deterministic backstop, collected on every review rather than only on a pass: the code's own tests are evidence the
	// repository gives, and a reviewer that missed a failing test is wrong however it voted. They cover every changed
	// package even in a follow-up round, because a fix can break a package whose files did not change since the last
	// round. A jury error leaves nothing to add them to, so it stops the tests instead of waiting for them.
	if err != nil {
		stopVerify()
		<-verified
	} else if findings := <-verified; len(findings) > 0 {
		res.Blocking = append(res.Blocking, findings...)
		fmt.Fprintf(w, "codeforge gate: the changed packages' tests fail, so the review cannot pass\n")
	}
	report(w, res)
	fmt.Fprintf(w, "codeforge gate: done in %s; %s\n", time.Since(start).Round(time.Second), usage.totals())
	usage.record(gateRecord{At: time.Now(), Tier: tier.Name, Models: models, Files: len(packed.Files), Brief: len(brief),
		Elapsed: time.Since(start).Seconds(), Passed: err == nil && res.Passed(), Blocking: len(res.Blocking), FollowUp: followUp})
	if err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	// Verdicts are cached for either outcome, but only if the tree they describe is still the one on disk.
	if after, err := guard.TreeID(ctx, repo); err == nil && after == tree {
		review.SaveVerdicts(res, opts)
		// The tests failed, so no verdict is cached: every rerun must re-verify instead of replaying a pass the repository
		// has already contradicted.
		if res.WithTests() {
			for _, m := range models {
				opts.Cache.Delete(opts.CacheKey(m))
			}
		}
	}
	if after, err := guard.TreeID(ctx, repo); err == nil && after == tree {
		if res.Passed() {
			rounds.Clear(roundKey)
		} else if err := rounds.Save(roundKey, review.Round{Tree: tree, Tier: tier.Name, Files: digests, Blocking: res.Blocking, At: time.Now()}); err != nil {
			fmt.Fprintln(w, "codeforge gate: could not save the review round:", err)
		}
	}
	if !res.Passed() {
		return fmt.Errorf("%w: %d blocking finding(s); fix them and run `codeforge gate` again", errGateFailed, len(res.Blocking))
	}

	// The pass, and any cached verdict, only counts if nothing changed while the reviewers worked.

	unlock, err := guard.LockContext(ctx, stateDir(home))
	if err != nil {
		return fmt.Errorf("gate: %w", err)
	}
	defer unlock()

	// Checked under the state lock, so nothing can change between this check and recording the pass.
	after, err := guard.TreeID(ctx, repo)
	if err != nil || after != tree || cleanHead(ctx, repo) != commit {
		return fmt.Errorf("%w: files or HEAD changed during the review; run `codeforge gate` again", errGateFailed)
	}

	st, err := guard.Load(stateDir(home))
	if err != nil {
		return fmt.Errorf("gate: %w", err)
	}
	// A pass is never recorded after the review's deadline or a cancel, however it was reached.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("gate: not recording a pass: %w", err)
	}
	st.PassGate(repo, tree, commit, fmt.Sprintf("%d reviewers passed", len(reviewers)), time.Now())
	if err := st.Save(stateDir(home)); err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	// A follow-up round only reviewed the files that changed since the last blocking review, so say so: the pass covers
	// the tree, but nobody should read it as someone having read the whole change.
	coverage := ""
	if followUp {
		coverage = fmt.Sprintf(" after a follow-up check of %d changed file(s)", len(packed.Files))
	}

	if commit == "" {
		fmt.Fprintf(w, "codeforge gate: PASS%s for your uncommitted changes.\n", coverage)
		return nil
	}
	fmt.Fprintf(w, "codeforge gate: PASS%s for commit %.8s.\n", coverage, commit)
	return nil
}

// reviewerFor picks the reviewer for a tier: the light model for docs when one is set, else the normal reviewer.
// The jury for bigger tiers always keeps the normal reviewer first.
func reviewerFor(t review.Tier, rc config.ReviewConfig) string {
	if t.Light && rc.Light != "" {
		return rc.Light
	}
	return rc.Model
}

func report(w io.Writer, res review.JuryResult) {
	for _, v := range res.Votes {
		switch {
		case v.Cancelled:
			fmt.Fprintf(w, "- %s: stopped early, another reviewer already blocked\n", v.Reviewer)
		case v.Err != nil:
			fmt.Fprintf(w, "- %s: no verdict (%v)\n", v.Reviewer, v.Err)
		case v.Cached:
			fmt.Fprintf(w, "- %s: %s (cached, same changes). %s\n", v.Reviewer, v.Verdict.Verdict, v.Verdict.Summary)
		default:
			fmt.Fprintf(w, "- %s: %s. %s\n", v.Reviewer, v.Verdict.Verdict, v.Verdict.Summary)
		}
	}

	section := func(title string, fs []review.Finding) {
		if len(fs) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s:\n", title)
		for _, f := range fs {
			if f.Invariant != "" {
				fmt.Fprintf(w, "- %s:%d %s (breaks: %s)\n", f.File, f.Line, f.What, f.Invariant)
				continue
			}
			fmt.Fprintf(w, "- %s:%d %s\n", f.File, f.Line, f.What)
		}
	}
	section("Blocking", res.Blocking)
	section("Should", res.Should)
	section("Dropped (file or line does not exist)", res.Unverified)
}

// cleanHead is the HEAD commit when the tree has no uncommitted or untracked changes, otherwise "".
func cleanHead(ctx context.Context, repo string) string {
	statusCmd := exec.CommandContext(ctx, "git", "-C", repo, "status", "--porcelain", "--", ".", ":(exclude).empryo")
	effort.Bound(statusCmd)
	status, err := statusCmd.Output()
	effort.Reap(statusCmd)
	if err != nil || len(status) > 0 {
		return ""
	}
	headCmd := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "HEAD")
	effort.Bound(headCmd)
	head, err := headCmd.Output()
	effort.Reap(headCmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(head))
}

func orBranch(b string) string {
	if b == "" {
		return "(detached)"
	}
	return b
}
