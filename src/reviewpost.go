package src

import (
	"bytes"
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

// postBound is the time a post of decision gets in all: plain comments are one gh call each, every call bounded by
// review.PostCallLimit, so the whole post has room for all of them. For plain comments it is only a backstop; the
// limit that applies is each call's own. Once the user has picked how to post, Ctrl+C no longer stops it: a post cut
// off between comments would leave half a review on the PR.
func postBound(decision review.Decision) time.Duration {
	if decision == review.DecisionPlain {
		return review.PostCallLimit * (review.MaxPlainFileComments + 1)
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
	if !post.Enabled() && !styled {
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
	text = review.StripVerdict(text)
	// The closing Decision line is the review's suggestion for its type, shown as such, not as a line of text; the user
	// picks the decision that is posted.
	decision, display := review.ParseDecision(text)
	// A blocker a juror found must not be missing from what goes up; everything else the jury says stays out. Without
	// posting, the leads follow from finishLeads.
	leads := ""
	if pipe := review.PipelineOf(ctx); pipe != nil && post.Enabled() {
		leads = pipe.Leads(ctx)
	}
	asked, answer := false, false
	if styled {
		ask := post.Enabled() && !post.Preapproved()
		key, shown := showReview(ctx, display, decision, leads, out.Bytes(), ask)
		*chat = key == keyChat
		chosen, picked := decisionKeys[key]
		asked, answer = ask && shown, picked
		if picked {
			decision = chosen
		}
	} else {
		fmt.Fprintln(os.Stdout, text)
		if leads != "" {
			fmt.Fprintln(os.Stdout, "\n"+leads)
		}
	}
	if !post.Enabled() {
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
	posted, err := post.Post(pctx, review.ExecRunner{}, text, leads, decision)
	if err != nil && posted.URL != "" {
		// Part of it is on the PR: say so, so a rerun does not post it twice.
		return fmt.Errorf("only part of the review was posted, up to %s; post the rest by hand, since running again posts it all again: %w", posted.URL, err)
	}
	if errors.Is(err, review.ErrMayBePosted) {
		return fmt.Errorf("the review above may not have been posted: %w", err)
	}
	if err != nil {
		return fmt.Errorf("the review above was not posted: %w", err)
	}
	switch {
	case posted.Decision == review.DecisionPlain:
		var parts []string
		if posted.Inline > 0 {
			parts = append(parts, review.CountFileComments(posted.Inline))
		}
		if posted.Final {
			parts = append(parts, "the final comment")
		}
		fmt.Fprintf(os.Stderr, "codeforge: posted the review as plain PR comments: %s %s\n", strings.Join(parts, " and "), posted.URL)
	case posted.Fallback != nil:
		fmt.Fprintf(os.Stderr, "codeforge: GitHub refused the review (%v); posted it as one plain comment, without its %s decision %s\n", posted.Fallback, decision.Label(), posted.URL)
	case posted.Downgraded != nil:
		fmt.Fprintf(os.Stderr, "codeforge: GitHub refused %s (%v; it does not allow it on your own PR); posted the review as Comment with %d inline comments %s\n",
			decision.Label(), posted.Downgraded, posted.Inline, posted.URL)
	default:
		fmt.Fprintf(os.Stderr, "codeforge: posted the review as %s with %d inline comments %s\n", posted.Decision.Label(), posted.Inline, posted.URL)
	}
	return nil
}

// The keys the review view offers besides moving around: post the review as one of the three decisions, or chat about
// it, or as plain PR comments that are no review at all. The user decides; the review's own decision is only marked as
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
// and, with ask, 1, 2 and 3 to post it as Approve, Comment or Request changes, and 4 as plain PR comments; key is
// the one pressed, "" for q or Ctrl+C. shown reports whether the view ran, so the keys were offered.
func showReview(ctx context.Context, text string, decision review.Decision, leads string, raw []byte, ask bool) (key string, shown bool) {
	st := codeStyleFor(ctx, os.Stdout)
	code := reviewCode(ctx, st, text)
	footer := runFooter(raw, st)
	verdict := st.paint(st.pal.dim, "suggested: ") + st.paint(decisionColor(decision, st), decision.Label())
	if accordionAvailable() {
		a := reviewAccordion(text, leads, st, code)
		a.Outro = footer
		for i, item := range a.Items {
			if strings.Contains(item.Title, finalCommentTitle) {
				a.Items[i].Summary = verdict
			}
		}
		if ask {
			for _, k := range decisionOrder {
				label := "post as " + decisionKeys[k].Label()
				if decisionKeys[k] == decision {
					label += " (suggested)"
				}
				a.Keys = append(a.Keys, forms.Action{Key: k, Label: label})
			}
		}
		a.Keys = append(a.Keys, forms.Action{Key: keyChat, Label: "chat about it"})
		key, err := showAccordion("review", a)
		if err == nil || errors.Is(err, forms.ErrAborted) {
			fmt.Fprint(os.Stdout, accordionRecap(a, st))
			return key, true
		}
		fmt.Fprintln(os.Stderr, "codeforge: could not open the review view:", err)
	}
	full := renderReview(text, st, code) + "\n" + findingIndent + verdict + "\n"
	if leads != "" {
		full += "\n" + renderReview(leads, st, nil)
	}
	if footer != "" {
		full += "\n" + footer + "\n"
	}
	fmt.Fprint(os.Stdout, full)
	return "", false
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
	showAccordion      = forms.Show
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
