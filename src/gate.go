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
	"strings"
	"time"

	"github.com/codeliter/codeforge/assets"
	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/review"
)

var errGateFailed = errors.New("review gate failed")

const gateTimeout = 15 * time.Minute

func stateDir(home string) string {
	return configDir(home)
}

// runGate reviews the current branch and uncommitted work with every configured reviewer and records a pass.
// Ticket and chat mode run it through the shell; a blocked push points the agent at it.
func runGate(ctx context.Context, home, bin string, cfg Config, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, gateTimeout)
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

	tier := review.Triage(local.Diff, local.Files)
	models := slices.Concat([]string{cfg.Review.Model}, cfg.Review.SecondOpinion)
	reviewers := make([]review.Reviewer, len(models))
	for i, m := range models {
		reviewers[i] = review.EmpryoReviewer{Bin: bin, Model: m, Dir: repo, Tier: tier, Wrap: sandboxCommand}
	}

	brief := review.PromptLocal(local, nil) + review.Pack(ctx, review.ExecRunner{}, repo, local)
	rubric := review.CacheKey(assets.Gate, review.VerdictInstructions, tier.Name)
	opts := review.JuryOptions{
		Cache:     &review.Cache{Dir: filepath.Join(configDir(home), "review-cache")},
		CacheKey:  func(model string) string { return review.CacheKey(tree, model, rubric) },
		StopEarly: true,
		Now:       time.Now(),
	}
	opts.Cache.Prune(opts.Now)

	start := time.Now()
	fmt.Fprintf(w, "codeforge gate: %s tier (%s effort), %d reviewers on %s vs %s: %s\n",
		tier.Name, tier.Effort, len(reviewers), orBranch(local.Branch), local.Base, strings.Join(models, ", "))
	res, err := review.Jury(ctx, repo, brief, reviewers, opts)
	report(w, res)
	fmt.Fprintf(w, "codeforge gate: done in %s\n", time.Since(start).Round(time.Second))
	if err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	// Verdicts are cached for either outcome, but only if the tree they describe is still the one on disk.
	if after, err := guard.TreeID(ctx, repo); err == nil && after == tree {
		review.SaveVerdicts(res, opts)
	}
	if !res.Passed() {
		return fmt.Errorf("%w: %d blocking finding(s); fix them and run `codeforge gate` again", errGateFailed, len(res.Blocking))
	}

	// The pass, and any cached verdict, only counts if nothing changed while the reviewers worked.

	unlock, err := guard.Lock(stateDir(home))
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
	st.PassGate(repo, tree, commit, fmt.Sprintf("%d reviewers passed", len(reviewers)), time.Now())
	if err := st.Save(stateDir(home)); err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	if commit == "" {
		fmt.Fprintln(w, "codeforge gate: PASS for your uncommitted changes. Commit them and run `codeforge gate` again before pushing; git only lets reviewed commits out.")
		return nil
	}
	fmt.Fprintf(w, "codeforge gate: PASS. Commit %.8s may be pushed and opened as a PR.\n", commit)
	return nil
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
			fmt.Fprintf(w, "- %s:%d %s\n", f.File, f.Line, f.What)
		}
	}
	section("Blocking", res.Blocking)
	section("Should", res.Should)
	section("Dropped (file or line does not exist)", res.Unverified)
}

// cleanHead is the HEAD commit when the tree has no uncommitted or untracked changes, otherwise "".
func cleanHead(ctx context.Context, repo string) string {
	status, err := exec.CommandContext(ctx, "git", "-C", repo, "status", "--porcelain", "--", ".", ":(exclude).empryo").Output()
	if err != nil || len(status) > 0 {
		return ""
	}
	head, err := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "HEAD").Output()
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
