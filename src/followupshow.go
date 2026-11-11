package src

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
)

// followUpVerdicts orders a follow-up round's verdicts on the user's threads, settled ones first.
var followUpVerdicts = []string{"fixed", "agreed", "accepted", "answered", "waiting", "held", "not fixed"}

// followUpSections lays out a follow-up round's verdicts: one row per file with the user's threads there, each with
// its code, what was said, the verdict, whether the thread is resolved, and the drafted reply.
func followUpSections(ctx context.Context, fu *review.FollowUp, answers []review.ThreadAnswer, st codeStyle) []forms.Section {
	threads := fu.Threads()
	if len(threads) == 0 {
		return nil
	}
	body := st
	if body.wrap {
		body.width = max(minCodeWidth, body.width-len(accordionIndent))
	}
	byThread := map[string]review.ThreadAnswer{}
	for _, a := range answers {
		byThread[a.Thread] = a
	}
	code := threadCode(ctx, fu.Feedback(), body)
	var order []string
	perPath := map[string][]review.MyThread{}
	for _, t := range threads {
		if _, ok := perPath[t.Path]; !ok {
			order = append(order, t.Path)
		}
		perPath[t.Path] = append(perPath[t.Path], t)
	}
	var out []forms.Section
	for _, path := range order {
		var b strings.Builder
		counts := map[string]int{}
		for i, t := range perPath[path] {
			if i > 0 {
				b.WriteString("\n")
			}
			ans, ok := byThread[t.ID]
			evidence := ""
			switch {
			case ok && ans.Resolve:
				evidence = "resolve the thread"
			case ok:
				evidence = "keep the thread open"
			}
			if t.Known && t.Blocking {
				evidence = strings.TrimSpace("blocking · " + evidence)
			}
			b.WriteString(threadBlock(ctx, t.Thread, review.Answer{Thread: t.ID, Verdict: ans.Verdict, Evidence: evidence, Reply: ans.Reply}, ok, body, code))
			if ok {
				counts[ans.Verdict]++
			}
		}
		var summary []string
		for _, v := range followUpVerdicts {
			if n := counts[v]; n > 0 {
				summary = append(summary, fmt.Sprintf("%d %s", n, v))
			}
		}
		out = append(out, forms.Section{
			Title:   st.paint(st.pal.accent, path) + st.paint(st.pal.dim, " · your threads"),
			Summary: strings.Join(summary, " · "),
			Body:    strings.Trim(b.String(), "\n"),
		})
	}
	return out
}

// postFollowUp posts the round's replies and resolves the threads it settled, then says what happened to each.
func postFollowUp(ctx context.Context, fu *review.FollowUp, answers []review.ThreadAnswer) ([]review.ThreadOutcome, error) {
	byID := map[string]review.MyThread{}
	for _, t := range fu.Threads() {
		byID[t.ID] = t
	}
	failed := 0
	outcomes := fu.PostFollowUp(ctx, codeRunner, answers)
	for _, o := range outcomes {
		t := byID[o.Thread]
		place := fmt.Sprintf("%s:%d", t.Path, t.Line)
		switch {
		case o.Err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "codeforge: thread on %s: %v\n", place, o.Err)
		case o.Moved:
			// The author pushed while the round ran: nothing is resolved on code nobody read.
			fmt.Fprintf(os.Stderr, "codeforge: replied on %s %s; the PR has new commits since this round read it, so no thread is resolved\n", place, o.URL)
		case o.Resolved && o.URL != "":
			fmt.Fprintf(os.Stderr, "codeforge: replied on %s and resolved it %s\n", place, o.URL)
		case o.Resolved:
			fmt.Fprintf(os.Stderr, "codeforge: resolved the thread on %s\n", place)
		case o.URL != "":
			fmt.Fprintf(os.Stderr, "codeforge: replied on %s %s\n", place, o.URL)
		}
	}
	if failed > 0 {
		return outcomes, fmt.Errorf("%w: %d of %d threads", errRepliesFailed, failed, len(outcomes))
	}
	return outcomes, nil
}

// errAccountChanged is why a follow-up is not posted when gh is signed in as another account than the one it was read as.
var errAccountChanged = errors.New("the GitHub account changed")

// postingAccount is the GitHub account the review is about to go up as, "" when gh cannot tell. A follow-up round
// read as one account is never posted as another: the threads it answers, and the record it updates, are that
// account's.
func postingAccount(ctx context.Context, fu *review.FollowUp) (string, error) {
	me := ""
	if out, err := codeRunner.Run(ctx, "gh", "api", "user", "--jq", ".login"); err == nil {
		me = strings.TrimSpace(string(out))
	}
	if fu.Active() && fu.Me() != "" && me != fu.Me() {
		return me, fmt.Errorf("%w: this follow-up was read as %s, but gh is now signed in as %s; switch back with gh auth switch, or review again",
			errAccountChanged, fu.Me(), cmp.Or(me, "no account"))
	}
	return me, nil
}

// recordPost keeps what was posted, so a later round knows which threads were blockers. Whatever the round, nothing
// the earlier record knew is lost while its thread is open: its findings are kept beside the new ones, and every open
// thread's blocking status is kept by its id. Only a thread GitHub confirmed resolved leaves the record.
//
// me is the account the review went up as (see postingAccount): the record is kept under it, since another account on
// this machine has a history of its own.
func recordPost(ctx context.Context, me string, post *review.PostRequest, posted review.Posted, decision review.Decision, text string, outcomes []review.ThreadOutcome, fu *review.FollowUp, judged bool) {
	if me == "" {
		fmt.Fprintln(os.Stderr, "codeforge: could not tell which GitHub account posted this review, so no record of it is kept for a later follow-up")
		return
	}
	// The review's own time may be spent by now (the user took a while to pick): the lookup gets its own.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), review.LoadLimit)
	defer cancel()
	repo := fullRepo(rctx, post.Repo())
	rec := review.RecordOf(repo, post.Number(), post.Head(), decision, text)
	if review.HasDeadline(ctx) {
		// When the review started, not when it was saved: of two rounds, the one that read the later code is the later.
		rec.At = review.Deadline(ctx).Add(-review.HardLimit)
	}
	// The review this round put up, known by its id, so a later round never takes it for one the user left by hand.
	rec.AddPosted(posted.ReviewID)
	err := reviewRecords().Update(rctx, me, repo, post.Number(), func(old review.Record, hadOld bool) review.Record {
		return mergeRecord(rec, old, hadOld, outcomes, fu, judged)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge: could not keep a record of this review for a later follow-up:", err)
	}
}

// mergeRecord builds the next record from rec, what was just posted, and old, the record before it.
func mergeRecord(rec, old review.Record, hadOld bool, outcomes []review.ThreadOutcome, fu *review.FollowUp, judged bool) review.Record {
	switch {
	case hadOld && old.At.After(rec.At):
		// A round that started later already kept its baseline: this one, finishing last, never moves it back.
		rec.Head, rec.At, rec.Decision = old.Head, old.At, old.Decision
	case judged:
	case fu.Active():
		// The earlier threads were not all judged: the next round starts from the same commit and looks at them again,
		// with the time and decision of the record that commit came from, when there is one. A review the user leaves by
		// hand after that time still counts; this round's own is known by its id. With no record yet, the round's own
		// time is the only one known, so it stays.
		rec.Head = fu.Since()
		if hadOld {
			rec.At, rec.Decision = old.At, old.Decision
		}
	default:
		// Only part of the review went up: the next round starts from the review before it, or from scratch.
		rec.Head, rec.At, rec.Decision = old.Head, old.At, old.Decision
	}
	// Every review codeforge ever posted here stays known by its id, whichever round wins the baseline.
	for _, id := range old.Posted {
		rec.AddPosted(id)
	}
	// What the user accepted as it is stays accepted, whichever round wins.
	if hadOld {
		rec.Accepted = slices.Clone(old.Accepted)
	}
	resolved := map[string]bool{}
	for _, o := range outcomes {
		resolved[o.Thread] = o.Resolved
	}
	rec.Threads = map[string]bool{}
	for id, blocking := range old.Threads {
		rec.Threads[id] = blocking
	}
	var gone []review.MyThread // threads GitHub confirmed resolved
	if fu.Active() {
		for _, t := range fu.Threads() {
			switch {
			case resolved[t.ID]:
				delete(rec.Threads, t.ID)
				gone = append(gone, t)
			case review.AcceptedAt(rec.Accepted, t.Path, t.OriginalLine):
				rec.Threads[t.ID] = false // accepted, not fixing: it never blocks again
			default:
				// Only ever raised: a round that read the thread as non-blocking never lowers what another recorded, by
				// thread id or by a blocking finding where the thread was posted. A finding matched on its line alone may
				// raise a thread, as LoadFollowUp does, but never vouch that it does not block (that needs the review it went
				// up in). A thread nothing is known of stays out, so it keeps counting as possibly blocking.
				f, _ := old.FindingAt(t.Path, t.OriginalLine)
				if blocking := rec.Threads[t.ID] || t.Blocking || f.Blocking; t.Known || blocking {
					rec.Threads[t.ID] = blocking
				}
			}
		}
	}
	if hadOld {
		for _, f := range old.Findings {
			// A finding leaves with the thread posted for it: on its line, or on the last line of its range, where a
			// comment on a range sits. Another thread inside the range keeps it.
			only := review.Record{Findings: []review.RecordFinding{f}}
			if slices.ContainsFunc(gone, func(t review.MyThread) bool {
				return t.Path == f.Path && (t.OriginalLine == f.Line || (f.End > f.Line && t.OriginalLine == f.End))
			}) && !slices.ContainsFunc(fu.Threads(), func(t review.MyThread) bool {
				_, in := only.FindingAt(t.Path, t.OriginalLine)
				return in && !resolved[t.ID]
			}) {
				continue
			}
			// A new finding at the same place never hides an earlier blocker: the earlier one comes first.
			if i := slices.IndexFunc(rec.Findings, func(n review.RecordFinding) bool {
				return n.Path == f.Path && n.Line == f.Line && n.End == f.End
			}); i >= 0 {
				if f.Blocking && !rec.Findings[i].Blocking {
					rec.Findings[i] = f
				}
				continue
			}
			rec.Findings = append(rec.Findings, f)
		}
	}
	return rec
}

// followUpPlain prints a follow-up round's verdicts as text, for a run with no review view: each thread, its verdict,
// whether it will be resolved, and the reply that will be posted.
func followUpPlain(fu *review.FollowUp, answers []review.ThreadAnswer) string {
	if !fu.Active() || len(answers) == 0 {
		return ""
	}
	byID := map[string]review.MyThread{}
	for _, t := range fu.Threads() {
		byID[t.ID] = t
	}
	var b strings.Builder
	b.WriteString("Your earlier threads:\n\n")
	for _, a := range answers {
		t := byID[a.Thread]
		action := "keep open"
		if a.Resolve {
			action = "resolve"
		}
		fmt.Fprintf(&b, "%s:%d — %s, %s\n", t.Path, t.Line, a.Verdict, action)
		if a.Reply != "" {
			fmt.Fprintf(&b, "  reply: %s\n", oneParagraph(a.Reply))
		}
	}
	return b.String() + "\n"
}
