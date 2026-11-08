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
func runReviewEngine(ctx context.Context, argv, env []string, limit time.Time) error {
	post := review.PostOf(ctx)
	styled := !hasFlag(argv, "--json") && stdoutStyled()
	if !post.Enabled() && !styled {
		return runEngine(ctx, argv, env, limit)
	}
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
	// A blocker a juror found must not be missing from what goes up; everything else the jury says stays out. Without
	// posting, the leads follow from finishLeads.
	leads := ""
	if pipe := review.PipelineOf(ctx); pipe != nil && post.Enabled() {
		leads = pipe.Leads(ctx)
	}
	asked, answer := false, false
	if styled {
		var closed bool
		asked, answer, closed = showReview(ctx, text, leads, out.Bytes(), post.Enabled() && !post.Preapproved())
		if closed {
			return errReviewClosed
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
			answer = confirmed(ctx, os.Stderr, "Post this review on the PR as your comment?")
		}
		if !answer {
			fmt.Fprintln(os.Stderr, "codeforge: not posted. Press p in the review to post it, or run with --comment=yes to post without asking.")
			return nil
		}
	}
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "codeforge: cancelled, so the review was not posted.")
		return nil
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postLimit)
	defer cancel()
	posted, err := post.Post(pctx, review.ExecRunner{}, text, leads)
	if err != nil {
		return fmt.Errorf("the review above was not posted: %w", err)
	}
	switch {
	case posted.Fallback != nil:
		fmt.Fprintf(os.Stderr, "codeforge: GitHub refused the inline comments (%v); posted the review as one comment %s\n", posted.Fallback, posted.URL)
	case posted.Inline > 0:
		fmt.Fprintf(os.Stderr, "codeforge: posted the review on the PR with %d inline comments %s\n", posted.Inline, posted.URL)
	default:
		fmt.Fprintf(os.Stderr, "codeforge: posted the review on the PR %s\n", posted.URL)
	}
	return nil
}

// errReviewClosed is returned when the user leaves the review with Ctrl+C: they are done, so nothing is posted and no
// follow-up chat opens. It is not a failure.
var errReviewClosed = errors.New("review closed")

// showReview shows a finished review on the terminal: as an accordion of its files when the forms helper is there,
// else printed in full. ask offers posting inside the accordion (key p); asked reports whether that question was put,
// post the answer, and closed whether the user left with Ctrl+C.
func showReview(ctx context.Context, text, leads string, raw []byte, ask bool) (asked, post, closed bool) {
	st := codeStyleFor(ctx, os.Stdout)
	code := reviewCode(ctx, st, text)
	footer := runFooter(raw, st)
	if accordionAvailable() {
		a := reviewAccordion(text, leads, st, code)
		a.Outro = footer
		if ask {
			a.Confirm, a.ConfirmKey = "post on the PR", "p"
		}
		yes, err := showAccordion("review", a)
		if err == nil || errors.Is(err, forms.ErrAborted) {
			fmt.Fprint(os.Stdout, accordionRecap(a, st))
			return ask, yes, err != nil
		}
		fmt.Fprintln(os.Stderr, "codeforge: could not open the review view:", err)
	}
	full := renderReview(text, st, code)
	if leads != "" {
		full += "\n" + renderReview(leads, st, nil)
	}
	if footer != "" {
		full += "\n" + footer + "\n"
	}
	fmt.Fprint(os.Stdout, full)
	return false, false, false
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
