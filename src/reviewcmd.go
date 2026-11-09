package src

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/config"

	"github.com/codeliter/codeforge/src/effort"
	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/review"
	"github.com/codeliter/codeforge/src/store"
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

// panelBrief starts the second-opinion jurors in the background and returns the reviewer's brief at once: the reviewer
// never waits for them. Their findings are printed after the review (see printLeads). scratch is set when the change is
// not in this checkout. Without a pipeline or a factory the brief carries the old dispatch wording, and with no time for
// the jurors it carries no jury wording at all.
func panelBrief(ctx context.Context, rc config.ReviewConfig, jurors jurorFactory, tier review.Tier, scratch bool, brief func(tier review.Tier, jury []string) string) string {
	models := review.JuryFor(tier, rc.SecondOpinion)
	pipe := review.PipelineOf(ctx)
	if jurors == nil || pipe == nil || len(models) == 0 {
		return brief(tier, models)
	}
	// Without a jury wording of its own the brief never tells the reviewer to dispatch jurors it has no time for.
	plainTier := tier.Panel()
	if time.Until(review.WorkDeadline(ctx))-review.KillGrace < review.MinRun {
		return brief(plainTier, nil)
	}
	reviewers, cleanup := jurors(ctx, models, tier.Timed(), scratch)
	if len(reviewers) == 0 {
		cleanup()
		return brief(plainTier, nil)
	}
	repoDir := ""
	if first, ok := reviewers[0].(review.EmpryoReviewer); ok {
		repoDir = first.Dir
	}
	plain := brief(plainTier, nil)
	pipe.Start(ctx, repoDir, plain, reviewers, cleanup)
	return plain
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

// canPickInView reports whether the review will be shown in the view, where the user picks how to post it: a terminal
// that shows it styled, the forms helper to show it, and no --json asking for the run as it is. These are the
// conditions runReviewEngine shows the view under.
func canPickInView(rest []string) bool {
	return !hasFlag(rest, "--json") && stdoutStyled() && accordionAvailable()
}

// reviewArgs turns `codeforge review [pr] [--repo owner/name] [empryo flags]` into an Empryo launch.
// With no PR it reviews the current branch and uncommitted work against the base branch.
func reviewArgs(ctx context.Context, bin string, rc config.ReviewConfig, judges []review.Judge, jurors jurorFactory, args []string) ([]string, error) {
	var ref, repo string
	var rest []string
	comment, preapproved, followUp, fresh := false, false, false, false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--follow-up":
			// Codeforge's own flags: review only what changed since the user's earlier review and answer the replies to
			// it (--follow-up), or review the whole PR again even so (--fresh). Without either, an earlier review is
			// found on its own.
			followUp = true
		case args[i] == "--fresh":
			fresh = true
		case args[i] == "--comment" || args[i] == "--comment=yes":
			// Codeforge's own flag: post the finished review on the PR once the user confirms it (--comment=yes: without
			// asking). Empryo never sees it.
			comment, preapproved = true, args[i] == "--comment=yes"
		case strings.HasPrefix(args[i], "--comment="):
			return nil, fmt.Errorf("%w: %s", errCommentValue, args[i])
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

	if comment && ref == "" && repo == "" {
		return nil, errCommentNeedsPR
	}
	if followUp && fresh {
		return nil, errFollowUpFresh
	}
	if followUp && ref == "" && repo == "" {
		return nil, errFollowUpNeedsPR
	}
	if comment && hasFlag(rest, "--json") {
		return nil, errCommentJSON
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
		left := min(review.AgentLimit, time.Until(review.WorkDeadline(ctx))) - review.KillGrace
		if left < review.MinRun {
			return rest, review.TimeNote(max(left, time.Second))
		}
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
		tier, _ = review.RaiseForPaths(tier, "", l.Files, rc.SensitivePaths)
		if note := review.SizeNote(len(l.Diff)); note != "" {
			fmt.Fprintln(os.Stderr, note)
		}
		review.DisplayOf(ctx).Local()
		// The changed files and their callers come with the brief, as for the gate and PRs, so the reviewer and the
		// jurors read instead of fetching and still reach a verdict.
		packed := ""
		if wd, err := os.Getwd(); err == nil {
			if root, err := guard.RepoRoot(ctx, wd); err == nil {
				pctx, pcancel := context.WithTimeout(ctx, review.LoadLimit)
				defer pcancel()
				packed = review.Pack(pctx, review.ExecRunner{}, root, l, tier)
			}
		}
		prompt := panelBrief(ctx, rc, jurors, tier, false, func(t review.Tier, jury []string) string { return review.PromptLocal(l, t, jury) + packed })
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
	post := review.PostOf(ctx)
	if comment && post == nil {
		return nil, errCommentUnavailable
	}
	if !comment && post != nil && canPickInView(rest) {
		// In the review view nothing goes up until the user picks a decision, so a PR review can always be posted from
		// there: --comment only matters where there is no view to pick in.
		comment = true
	}
	if comment {
		post.Target(repo, number)
		if preapproved {
			post.Preapprove()
		}
	}

	lctx, cancel := context.WithTimeout(ctx, review.LoadLimit)
	defer cancel()
	b, err := review.Load(lctx, review.ExecRunner{}, repo, number)
	if err != nil {
		return nil, fmt.Errorf("loading PR %s: %w", number, err)
	}
	review.DisplayOf(ctx).PR(b.HeadRef, repo, b.PR.HeadRefOid)
	cctx, ccancel := context.WithTimeout(ctx, review.LoadLimit)
	defer ccancel()
	tier, _ := review.Classify(cctx, judges, b.Diff, b.Files())
	tier, _ = review.RaiseForPaths(tier, "", b.Files(), rc.SensitivePaths)
	if note := review.SizeNote(len(b.Diff)); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	// The changed files come with the brief, so the jurors and the reviewer read instead of fetching and still reach a verdict.
	packed := ""
	pctx, pcancel := context.WithTimeout(ctx, review.LoadLimit)
	defer pcancel()
	switch {
	case b.HeadRef != "":
		packed = review.PackRef(pctx, review.ExecRunner{}, b.HeadRef, b.Files(), b.Diff, tier)
	case b.PR.HeadRefOid != "":
		// Not in this checkout: read the changed files at the PR head from GitHub, so the brief is as full as it can be.
		packed = review.PackRemote(pctx, review.ExecRunner{}, repo, b.PR.HeadRefOid, b.Files(), b.Diff, tier)
	}
	round := ""
	if !fresh {
		fu, found, err := earlierReview(ctx, repo, number, b.PR.HeadRefOid)
		switch {
		case err != nil && followUp:
			return nil, fmt.Errorf("looking for your earlier review of PR %s: %w", number, err)
		case err != nil:
			fmt.Fprintln(os.Stderr, "codeforge: could not check for your earlier review, so this is a full review (--follow-up stops instead):", err)
		case found:
			review.FollowUpOf(ctx).Set(fu)
			if !comment && post != nil && !hasFlag(rest, "--json") { // a script that asks for --json gets the run as it is
				// A follow-up round is about the user's own threads on the PR, so it can always be posted: the view offers
				// the decisions, and nothing goes up until the user picks one.
				comment = true
				post.Target(repo, number)
			}
			round = fu.PromptFollowUp()
			fmt.Fprintf(os.Stderr, "codeforge: you reviewed PR %s before; this is a follow-up round on your %s and the changes since. Use --fresh for a full review.\n",
				number, plural(len(fu.Threads()), "open thread"))
		case followUp:
			return nil, fmt.Errorf("PR %s: %w", number, review.ErrNoEarlierReview)
		}
	}
	prompt := panelBrief(ctx, rc, jurors, tier, b.HeadRef == "", func(t review.Tier, jury []string) string { return review.Prompt(b, t, jury) + packed + round })
	if round != "" {
		// Only the reviewer answers the earlier threads; the jurors' brief has the round's context but not its block.
		prompt += "\n" + review.FollowUpNote
	}
	if comment && post != nil {
		post.SetDiff(b.Diff)
		post.SetHead(b.PR.HeadRefOid)
		prompt += "\n\n" + review.CommentNote
	}
	rest, note := bound(rest, tier)
	return append(rest, prompt+"\n\n"+note), nil
}

// errFollowUpFresh and errFollowUpNeedsPR reject flags that cannot go together.
var (
	errFollowUpFresh   = errors.New("--follow-up and --fresh ask for opposite rounds; pick one")
	errFollowUpNeedsPR = errors.New("--follow-up needs a PR: a follow-up answers the replies to your review of one")
)

// recordStore keeps what codeforge posted for the user, one record per pull request, in the session database.
type recordStore struct{}

// reviewRecords is where codeforge keeps what it posted for the user.
func reviewRecords() recordStore { return recordStore{} }

var errNoRecordPlace = errors.New("no repository to keep the review record under")

// open opens the session database the records live in.
func (recordStore) open(ctx context.Context) (*store.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("finding the home directory: %w", err)
	}
	s, err := store.Open(ctx, filepath.Join(config.Dir(home), "sessions"))
	if err != nil {
		return nil, fmt.Errorf("opening the session database: %w", err)
	}
	return s, nil
}

// Load returns the record of account's last posted review of repo#number; false when there is none or it cannot be
// read, which only makes the next review a full one.
func (rs recordStore) Load(ctx context.Context, account, repo, number string) (review.Record, bool) {
	s, err := rs.open(ctx)
	if err != nil {
		return review.Record{}, false
	}
	defer s.Close() //nolint:errcheck // read only
	data, err := s.ReviewRecord(ctx, store.ReviewKey{Account: account, Repo: repo, Number: number})
	var rec review.Record
	if err != nil || json.Unmarshal(data, &rec) != nil {
		return review.Record{}, false
	}
	return rec, true
}

// Update builds the next record of account's review of repo#number from the one before it with merge, in one
// transaction keyed by account, repo and number: each account has its own history, and an update replaces.
func (rs recordStore) Update(ctx context.Context, account, repo, number string, merge func(old review.Record, ok bool) review.Record) error {
	if account == "" || !strings.Contains(repo, "/") || number == "" {
		return fmt.Errorf("%w: %q %q #%s", errNoRecordPlace, account, repo, number)
	}
	s, err := rs.open(ctx)
	if err != nil {
		return err
	}
	defer s.Close() //nolint:errcheck // the transaction has committed or failed by then
	return s.UpdateReviewRecord(ctx, store.ReviewKey{Account: account, Repo: repo, Number: number}, func(old []byte) ([]byte, error) {
		var prev review.Record
		ok := old != nil && json.Unmarshal(old, &prev) == nil
		next := merge(prev, ok)
		data, err := json.Marshal(next)
		if err != nil {
			return nil, fmt.Errorf("encoding the review record: %w", err)
		}
		return data, nil
	})
}

// currentLogin is the GitHub account gh is signed in as, "" when it cannot tell.
func currentLogin(ctx context.Context) string {
	out, err := review.ExecRunner{}.Run(ctx, "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// fullRepo names repo as owner/name, asking gh for the checkout's own when it is "".
func fullRepo(ctx context.Context, repo string) string {
	if repo != "" {
		return repo
	}
	return currentRepo(ctx)
}

// earlierReview looks for the user's earlier review of repo#number, so this review can be a follow-up round on it.
func earlierReview(ctx context.Context, repo, number, head string) (*review.FollowUp, bool, error) {
	lctx, cancel := context.WithTimeout(ctx, review.LoadLimit)
	defer cancel()
	// The record is this account's own: another account on the same machine has a history of its own.
	rec, hasRec := review.Record{}, false
	me := currentLogin(lctx)
	if me != "" {
		rec, hasRec = reviewRecords().Load(lctx, me, fullRepo(lctx, repo), number)
	} else {
		fmt.Fprintln(os.Stderr, "codeforge: could not tell which GitHub account is signed in, so what codeforge recorded of your earlier reviews is not used; every earlier thread counts as possibly blocking")
	}
	fu, found, err := review.LoadFollowUp(lctx, review.ExecRunner{}, repo, number, head, rec, hasRec)
	if err != nil {
		return nil, false, fmt.Errorf("reading the PR's reviews: %w", err)
	}
	// The round reads the PR as whoever gh is signed in as by then: one account's record never goes with another's
	// threads.
	if hasRec && found && fu.Me() != me {
		return nil, false, fmt.Errorf("%w: the record was read as %s, but the PR as %s; review again", errAccountChanged, me, cmp.Or(fu.Me(), "no account"))
	}
	return fu, found, nil
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
