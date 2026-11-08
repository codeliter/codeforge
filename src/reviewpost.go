package src

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
)

var (
	errCommentNeedsPR = errors.New("--comment posts on a pull request: use codeforge review <number|url> --comment")
	errCommentValue   = errors.New("--comment takes no value except --comment=yes")
	errCommentJSON    = errors.New("--comment reads the run as JSON itself, so it cannot be combined with --json")
	errCommentResume  = errors.New("--comment applies to a new PR review, not to a resumed session")
	// errCommentUnavailable means the review has no way to post: a bug in how it was started, not a choice.
	errCommentUnavailable = errors.New("--comment cannot be used in this review")
)

// postLimit bounds the gh call that posts a confirmed review. It starts when the user answers, so a slow answer never
// leaves the post without time.
const postLimit = 20 * time.Second

// plainOtherCalls counts the gh calls of a plain post besides its line comments: the head lookup and the final comment.
const plainOtherCalls = 2

// postBound is the time a post of decision gets in all: plain comments are one gh call each, every call bounded by
// review.PostCallLimit, so the whole post has room for all of them. For plain comments it is only a backstop; the
// limit that applies is each call's own. Once the user has picked how to post, Ctrl+C no longer stops it: a post cut
// off between comments would leave half a review on the PR.
func postBound(decision review.Decision) time.Duration {
	if decision == review.DecisionPlain {
		return review.PostCallLimit * (review.MaxPlainLineComments + plainOtherCalls)
	}
	return postLimit
}

// Where the confirmation is read from and whether there is a person to ask; tests replace both.
var (
	confirmIn  io.Reader = os.Stdin
	confirmTTY           = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
)

// runReviewEngine runs the review's engine like runEngine. When the review is printed on a terminal, or the user asked
// for it to be posted on the PR (--comment), it keeps the run's JSON result instead of passing it through and prints
// the review itself: on a terminal in the Codeforge Steel style with the code around each finding. When posting, it
// then asks the user to confirm and only then posts it with gh. The model stays read-only and posts nothing; a failed
// run is never posted. Posting without a person to ask needs --comment=yes. Output for a script (--json, or standard
// output that is not a terminal) passes through untouched.
//
// chat reports whether the follow-up chat should open: always outside review mode, where the chat is the point, and in
// review mode only when the user pressed c in the review.
func runReviewEngine(ctx context.Context, argv, env []string, limit time.Time) (chat bool, err error) {
	if fb := review.RespondOf(ctx); fb != nil && !hasFlag(argv, "--json") && stdoutStyled() {
		return runShownRespond(ctx, argv, env, limit, fb)
	}
	post := review.PostOf(ctx)
	inReview := review.DisplayOf(ctx) != nil
	styled := inReview && !hasFlag(argv, "--json") && stdoutStyled()
	// A follow-up round's verdicts are read even when nothing is shown or posted, so their block never reaches the
	// output as review text; a script that asks for --json gets the run as it is.
	if !post.Enabled() && !styled && (!review.FollowUpOf(ctx).Active() || hasFlag(argv, "--json")) {
		return true, runEngine(ctx, argv, env, limit)
	}
	err = runShownReview(ctx, argv, env, limit, styled, &chat)
	return chat, err
}

// runShownReview runs a review whose result codeforge shows (styled, on a terminal) or posts, as runReviewEngine
// describes; it sets *chat when the user asks for the follow-up chat.
func runShownReview(ctx context.Context, argv, env []string, limit time.Time, styled bool, chat *bool) error {
	post := review.PostOf(ctx)
	if !hasFlag(argv, "--json") {
		argv = append(slices.Clone(argv), "--json")
	}
	// On a terminal the engine's own start-up warnings are held back so the review stands alone; they are shown when the
	// run fails, where they may explain why.
	var chatter bytes.Buffer
	engineCtx := ctx
	if styled {
		fmt.Fprintln(os.Stderr, "codeforge: reviewing; the review appears here when the model is done.")
		engineCtx = withEngineStderr(ctx, &chatter)
	}
	var out bytes.Buffer
	if err := runEngineTo(engineCtx, argv, env, limit, &out); err != nil {
		fmt.Fprint(os.Stderr, chatter.String())
		fmt.Fprint(os.Stdout, out.String())
		return err
	}
	text, err := review.EngineOutput(out.Bytes())
	if err != nil {
		fmt.Fprint(os.Stderr, chatter.String())
		fmt.Fprint(os.Stdout, out.String())
		if !post.Enabled() {
			return err
		}
		return fmt.Errorf("not posting the review: %w", err)
	}
	// A follow-up round ends with its verdicts on the user's earlier threads; they are shown beside those threads and
	// posted as replies, never as part of the review.
	fu := review.FollowUpOf(ctx)
	var answers []review.ThreadAnswer
	judged := true      // whether every earlier thread got a verdict, so the round can count as done
	unreadable := false // whether the verdicts could not be read at all: then nothing of the round is posted
	if fu.Active() {
		rest, a, perr := fu.ParseFollowUp(text)
		text, answers, judged = rest, a, perr == nil
		switch {
		case errors.Is(perr, review.ErrUnanswered):
			fmt.Fprintln(os.Stderr, "codeforge: some of your earlier threads got no verdict; they are left as they are, and the next round looks at them again.")
		case perr != nil:
			unreadable = true
			fmt.Fprintln(os.Stderr, "codeforge: the reviewer gave no readable verdicts on your earlier threads, so this round is shown but not posted.")
		}
		if post.Preapproved() {
			// Nobody looks before posting (--comment=yes): a reply may go up, but a blocker is only resolved by a person.
			answers = fu.KeepBlockersOpen(answers)
		}
	}
	// A note to the user before the findings is for the terminal at most, never for the PR.
	text = review.StripPreamble(review.StripVerdict(text))
	// A comment too long to take in on one read goes back to the reviewer once, to be cut.
	if long := longComments(text); len(long) > 0 {
		fmt.Fprintf(os.Stderr, "codeforge: asking the reviewer to cut %s that run too long.\n", plural(len(long), "comment"))
		text = tightenReview(ctx, text, reviewTurn(argv, env), func(revised string) string {
			if fu.Active() {
				revised, _, _ = fu.ParseFollowUp(revised) //nolint:errcheck // only the review part is kept; the first reply's verdicts stand
			}
			return review.StripPreamble(review.StripVerdict(revised))
		})
	}
	// The closing Decision line is the review's suggestion for its type, shown as such, not as a line of text; the user
	// picks the decision that is posted.
	decision, display := review.ParseDecision(text)
	if fu.Active() && decision == review.DecisionApprove && fu.Unsettled(answers) {
		// A blocking thread is still open: the suggestion is never to approve past it.
		decision = review.DecisionRequestChanges
	}
	// A blocker a juror found must not be missing from what goes up; everything else the jury says stays out. Without
	// posting, the leads follow from finishLeads.
	leads := ""
	if pipe := review.PipelineOf(ctx); pipe != nil && post.Enabled() {
		leads = pipe.Leads(ctx)
		if dropCoveredLeads(leads, text) != "" {
			// Another reviewer wrote them; what goes on the PR reads as the user's own, like the rest of the review.
			fmt.Fprintln(os.Stderr, "codeforge: putting the unconfirmed blocking leads in your words...")
			leads = voiceLeads(ctx, leads, reviewTurn(argv, env))
		}
	}
	asked, answer := false, false
	if styled {
		ask := post.Enabled() && !post.Preapproved() && !unreadable // a round whose verdicts did not read is never posted
		reviewPostNote = ""                                         // one review's note never carries over to the next
		if !ask && !post.Preapproved() {
			reviewPostNote = noPostNote(post.Enabled(), unreadable)
		}
		var threads []forms.Section
		if fu.Active() {
			threads = followUpSections(ctx, fu, answers, codeStyleFor(ctx, os.Stdout))
		}
		// The user may mark changes and send them, as often as they like: rewrites and deletions apply as written,
		// questions go to the reviewer, and the view opens again on the review as it now reads.
		var round reviewRound
		// A lead the review makes as a blocking finding of its own is left out, against the review as it reads now: one
		// the user's edits have left uncovered shows, and is posted, again.
		key, changes, shown := showReview(ctx, display, decision, dropCoveredLeads(leads, text), out.Bytes(), ask, threads, round)
		for key == forms.KeySend {
			if n := countQuestions(changes); n > 0 {
				fmt.Fprintf(os.Stderr, "codeforge: asking the reviewer %s (up to %d minutes); the review opens again with the answers.\n",
					plural(n, "question"), int(revisionLimit.Minutes()))
			}
			next, err := reviseReview(ctx, text, changes, reviewTurn(argv, env), func(revised string) string {
				// The revised reply is tidied like the first one: thread verdicts and notes to the user are never review text.
				if fu.Active() {
					revised, _, _ = fu.ParseFollowUp(revised) //nolint:errcheck // only the review part is kept; the first reply's verdicts stand
				}
				return review.StripPreamble(review.StripVerdict(revised))
			})
			if err != nil {
				// Only the questions needed the reviewer: the user's own rewrites and deletions stand. They change findings
				// only, never the Decision line, so the decision below is read as before.
				fmt.Fprintln(os.Stderr, "codeforge: "+err.Error()+"; your questions were not answered, your rewrites and deletions are kept.")
				next.Answers = append(next.Answers, "Your questions were not answered: "+err.Error()+". Your rewrites and deletions are kept; ask again with ? or /.")
			}
			round, text = next, next.Text
			decision, display = review.ParseDecision(text)
			if fu.Active() && decision == review.DecisionApprove && fu.Unsettled(answers) {
				decision = review.DecisionRequestChanges
			}
			key, changes, shown = showReview(ctx, display, decision, dropCoveredLeads(leads, text), out.Bytes(), ask, threads, round)
		}
		*chat = key == keyChat
		chosen, picked := decisionKeys[key]
		asked, answer = ask && shown, picked
		if picked {
			decision = chosen
		}
	} else {
		// What may be posted is printed first, the replies to the earlier threads included.
		fmt.Fprint(os.Stdout, followUpPlain(fu, answers))
		fmt.Fprintln(os.Stdout, text)
		if l := dropCoveredLeads(leads, text); l != "" {
			fmt.Fprintln(os.Stdout, "\n"+l)
		}
	}
	if !post.Enabled() {
		return nil
	}
	if unreadable {
		fmt.Fprintln(os.Stderr, "codeforge: not posted. Run the review again for a follow-up whose verdicts read, or with --fresh for a full review.")
		return nil
	}

	// The review has ended; waiting for the user's answer is not part of its clock, so only the posting is bounded.
	if !post.Preapproved() {
		if !asked {
			answer = confirmed(ctx, os.Stderr, "Post this review on the PR as "+decision.Label()+"?")
		}
		if !answer {
			how := "Answer yes when asked"
			if asked {
				how = "Pick how to post it in the review (1 to 4)"
			}
			fmt.Fprintln(os.Stderr, "codeforge: not posted. "+how+", or run with --comment=yes to post the suggested one without asking.")
			return nil
		}
	}
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "codeforge: cancelled, so the review was not posted.")
		return nil
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postBound(decision))
	defer cancel()
	me, err := postingAccount(pctx, fu)
	if err != nil {
		return fmt.Errorf("the review above was not posted: %w", err)
	}
	posted, err := post.Post(pctx, review.ExecRunner{}, text, dropCoveredLeads(leads, text), decision)
	if err != nil && posted.URL != "" {
		// Part of it is on the PR: say so, so a rerun does not post it twice, and keep what is up for the next round.
		// The earlier threads are not answered, so the round does not count as having judged them.
		recordPost(ctx, me, post, posted, postedAs(posted, decision), text, nil, fu, false)
		return fmt.Errorf("only part of the review was posted, up to %s; post the rest by hand, since running again posts it all again: %w", posted.URL, err)
	}
	if errors.Is(err, review.ErrMayBePosted) {
		recordPost(ctx, me, post, posted, postedAs(posted, decision), text, nil, fu, false)
		return fmt.Errorf("the review above may not have been posted: %w", err)
	}
	if err != nil {
		return fmt.Errorf("the review above was not posted: %w", err)
	}
	switch {
	case posted.Decision == review.DecisionPlain:
		var parts []string
		if posted.Inline > 0 {
			parts = append(parts, review.CountLineComments(posted.Inline))
		}
		if posted.Final {
			parts = append(parts, "the final comment")
		}
		fmt.Fprintf(os.Stderr, "codeforge: posted the review as separate comments you can delete one by one: %s %s\n", strings.Join(parts, " and "), posted.URL)
		if n := len(posted.Refused); n > 0 {
			fmt.Fprintf(os.Stderr, "codeforge: GitHub would not put %s on its line (%v); they are in the final comment, named by their place\n",
				review.CountLineComments(n), posted.Refused[0])
		}
	case posted.Fallback != nil:
		fmt.Fprintf(os.Stderr, "codeforge: GitHub refused the review (%v); posted it as one plain comment, without its %s decision %s\n", posted.Fallback, decision.Label(), posted.URL)
	case posted.Downgraded != nil:
		fmt.Fprintf(os.Stderr, "codeforge: GitHub refused %s (%v; it does not allow it on your own PR); posted the review as Comment with %d inline comments %s\n",
			decision.Label(), posted.Downgraded, posted.Inline, posted.URL)
	default:
		fmt.Fprintf(os.Stderr, "codeforge: posted the review as %s with %d inline comments %s\n", posted.Decision.Label(), posted.Inline, posted.URL)
	}
	// The record is kept after the threads are answered, so it knows which ones are really resolved.
	var outcomes []review.ThreadOutcome
	var ferr error
	if len(answers) > 0 {
		// The replies get their own time, never what is left of the review's.
		fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), review.PostCallLimit*time.Duration(2*len(answers)+1))
		outcomes, ferr = postFollowUp(fctx, fu, answers)
		fcancel()
	}
	recordPost(ctx, me, post, posted, postedAs(posted, decision), text, outcomes, fu, judged)
	if ferr != nil {
		// The review is up and recorded; only some replies or resolves failed, and the error says that much.
		where := "the review is on the PR"
		if posted.URL != "" {
			where += " (" + posted.URL + ")"
		}
		return fmt.Errorf("%s, but %w", where, ferr)
	}
	return nil
}

// postedAs is the decision that actually went up: a review GitHub refused that went up as one plain comment is no
// review at all, so no review state is recorded for it.
func postedAs(posted review.Posted, decision review.Decision) review.Decision {
	if posted.Fallback != nil {
		return review.DecisionPlain
	}
	return cmp.Or(posted.Decision, decision)
}

// The keys the review view offers besides moving around: post the review as one of the three decisions, or chat about
// it, or as separate comments that are no review at all. The user decides; the review's own decision is only marked as
// the suggestion.
const keyChat = "c"

var (
	decisionOrder = []string{"1", "2", "3", "4"}
	decisionKeys  = map[string]review.Decision{
		"1": review.DecisionApprove, "2": review.DecisionComment, "3": review.DecisionRequestChanges, "4": review.DecisionPlain,
	}
)

// showReview shows a finished review on the terminal: as an accordion of its files when the forms helper is there,
// else printed in full. The suggested decision sits on the final comment's row. It offers c to chat about the review
// and, with ask, 1, 2 and 3 to post it as Approve, Comment or Request changes, and 4 as separate comments; key is
// the one pressed, "" for q or Ctrl+C. In the accordion the user can also mark changes on the findings and send them
// (forms.KeySend): key is then KeySend and changes holds them. round shows what the last round of changes did. shown
// reports whether the view ran, so the keys were offered.
func showReview(ctx context.Context, text string, decision review.Decision, leads string, raw []byte, ask bool, threads []forms.Section, round reviewRound) (key string, changes []forms.Change, shown bool) {
	st := codeStyleFor(ctx, os.Stdout)
	code := reviewCode(ctx, st, text)
	footer := runFooter(raw, st)
	verdict := st.paint(st.pal.dim, "suggested: ") + st.paint(decisionColor(decision, st), decision.Label())
	if accordionAvailable() {
		a := reviewView(st, code, text, leads, decision, round, threads, ask)
		a.Outro = footer
		key, changes, err := showAccordion("review", a)
		if err == nil || errors.Is(err, forms.ErrAborted) {
			if key != forms.KeySend {
				fmt.Fprint(os.Stdout, accordionRecap(a, st))
			}
			return key, changes, true
		}
		fmt.Fprintln(os.Stderr, "codeforge: could not open the review view:", err)
	}
	var b strings.Builder
	for _, t := range threads {
		b.WriteString(t.Title + "\n\n" + t.Body + "\n\n")
	}
	full := b.String() + renderReview(text, st, code) + "\n" + findingIndent + verdict + "\n"
	if leads != "" {
		full += "\n" + renderReview(leads, st, nil)
	}
	if footer != "" {
		full += "\n" + footer + "\n"
	}
	fmt.Fprint(os.Stdout, full)
	return "", nil, false
}

// reviewView is the review as the accordion shows it, with the keys to act on it: what the last round of changes did
// on top, a follow-up round's earlier threads first, the suggested decision on the final comment's row, and, with ask,
// the decisions to post it as, apart from the other ways to finish.
func reviewView(st codeStyle, code codeSource, text, leads string, decision review.Decision, round reviewRound, threads []forms.Section, ask bool) forms.Accordion {
	a := reviewAccordion(text, leads, st, code, round.Marks)
	a.Edits = true
	a.Intro = roundIntro(st, text, round)
	if !ask && reviewPostNote != "" {
		a.Intro = strings.TrimSpace(st.paint(st.pal.dim, reviewPostNote) + "\n\n" + a.Intro)
	}
	// A follow-up round's earlier threads come first: what the author did about them is the point of the round.
	a.Items = append(slices.Clone(threads), a.Items...)
	verdict := st.paint(st.pal.dim, "suggested: ") + st.paint(decisionColor(decision, st), decision.Label())
	for i, item := range a.Items {
		if strings.Contains(item.Title, finalCommentTitle) {
			a.Items[i].Summary = verdict
		}
	}
	other := ""
	if ask {
		other = "Or"
		for _, k := range decisionOrder {
			label := "post as " + decisionKeys[k].Label()
			if decisionKeys[k] == decision {
				label += " (suggested)"
			}
			group := "Post as"
			if decisionKeys[k] == review.DecisionPlain {
				group = other // no review at all, so not one of the decisions
			}
			a.Keys = append(a.Keys, forms.Action{Key: k, Label: label, Group: group})
		}
	}
	a.Keys = append(a.Keys, forms.Action{Key: keyChat, Label: "chat about it", Group: other})
	return a
}

// reviewPostNote, when set, says in the view why it offers no way to post, so p showing only the chat is not a mystery.
var reviewPostNote string

// noPostNote says why a review cannot be posted from the view.
func noPostNote(enabled, unreadable bool) string {
	if enabled && unreadable {
		return "Posting is off for this round: the reviewer's verdicts on your earlier threads could not be read. Run the review again to post it."
	}
	return "Posting is off: run codeforge review with --comment to post this review."
}

// decisionColor colours a decision like the labels it follows from: Request changes as an issue, Approve as praise.
func decisionColor(d review.Decision, st codeStyle) string {
	switch d {
	case review.DecisionRequestChanges:
		return st.pal.issue
	case review.DecisionApprove:
		return st.pal.praise
	}
	return st.pal.suggestion
}

// showAccordion runs the accordion through the forms helper, and accordionAvailable says whether there is one; tests
// replace both.
var (
	showAccordion      = forms.Show // name, accordion -> key pressed, changes sent with it
	accordionAvailable = func() bool { _, err := forms.Helper(); return err == nil }
)

// readLine reads one line a byte at a time, so nothing typed after it is taken from the terminal: the TUI that may open
// next must still get it. End of input gives what was read, which is not a yes.
func readLine(r io.Reader) string {
	var line strings.Builder
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			line.WriteByte(b[0])
		}
		if err != nil {
			break
		}
	}
	return line.String()
}

// confirmed asks a yes/no question of the person at the terminal. With nobody to ask, or anything but y or yes, it is no.
// Cancelling the command (Ctrl+C) ends the question at once, and a late yes after it is not a yes.
func confirmed(ctx context.Context, w io.Writer, question string) bool {
	if !confirmTTY() {
		fmt.Fprintln(w, "codeforge: no terminal to ask on.")
		return false
	}
	fmt.Fprintf(w, "\ncodeforge: %s [y/N] ", question)
	answer := make(chan string, 1)
	go func() { answer <- readLine(confirmIn) }()
	select {
	case <-ctx.Done():
		fmt.Fprintln(w)
		return false
	case line := <-answer:
		reply := strings.ToLower(strings.TrimSpace(line))
		return (reply == "y" || reply == "yes") && ctx.Err() == nil
	}
}
