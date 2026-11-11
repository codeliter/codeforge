package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/review"
)

var (
	errReviewAcceptUsage = errors.New("usage: codeforge review accept PR PATH:LINE [REASON...] | review unaccept PR PATH:LINE | review accepted PR [--repo OWNER/NAME]")
	errNoReviewRecord    = errors.New("codeforge has no record of a review you posted on this PR")
	errNoLogin           = errors.New("could not tell which GitHub account is signed in (gh auth status)")
	errNotAcceptedOnPR   = errors.New("no accepted finding there; `codeforge review accepted PR` lists them")
)

// reviewAcceptWords are the review subcommands that manage the findings the user accepted on a PR.
var reviewAcceptWords = []string{"accept", "unaccept", "accepted"}

// isReviewAccept reports a `codeforge review accept|unaccept|accepted ...` call, which Run answers before any mode.
func isReviewAccept(args []string) bool {
	return len(args) > 1 && args[0] == modeReview && slices.Contains(reviewAcceptWords, args[1])
}

var prURLRepo = regexp.MustCompile(`^https://github\.com/([\w.-]+/[\w.-]+)/pull/\d+`)

// runReviewAccept marks a finding of the user's posted review of a PR as accepted, not fixing: later rounds on the PR
// leave it out, and its thread no longer blocks.
func runReviewAccept(ctx context.Context, args []string, w io.Writer) error {
	repo := flagValue(args, "--repo")
	args = dropFlag(args, "--repo")
	const wordAndPR = 2
	if len(args) < wordAndPR {
		return errReviewAcceptUsage
	}
	number, err := review.ParseRef(args[1])
	if err != nil {
		return err
	}
	if m := prURLRepo.FindStringSubmatch(args[1]); m != nil && repo == "" {
		repo = m[1]
	}
	me := currentLogin(ctx)
	if me == "" {
		return errNoLogin
	}
	repo = fullRepo(ctx, repo)
	store := reviewRecords()
	load := func() (review.Record, error) {
		rec, ok := store.Load(ctx, me, repo, number)
		if !ok {
			return rec, fmt.Errorf("%w: %s#%s", errNoReviewRecord, repo, number)
		}
		return rec, nil
	}
	return reviewAcceptCommand(args[0], args[2:], load, func(change func(*review.Record) error) (review.Record, error) {
		var out review.Record
		var cerr error
		err := store.Update(ctx, me, repo, number, func(old review.Record, ok bool) review.Record {
			if !ok {
				cerr = fmt.Errorf("%w: %s#%s", errNoReviewRecord, repo, number)
				return old
			}
			next := old
			next.Findings = slices.Clone(old.Findings)
			next.Accepted = slices.Clone(old.Accepted)
			if cerr = change(&next); cerr != nil {
				return old
			}
			out = next
			return next
		})
		if cerr != nil {
			return out, cerr
		}
		return out, err
	}, w, time.Now())
}

// reviewAcceptCommand runs one accept, unaccept or accepted against the record update gives.
// load reads the record without writing it, so listing changes nothing.
func reviewAcceptCommand(word string, rest []string, load func() (review.Record, error), update func(func(*review.Record) error) (review.Record, error), w io.Writer, now time.Time) error {
	switch {
	case word == "accepted" && len(rest) == 0:
		rec, err := load()
		if err != nil {
			return err
		}
		if len(rec.Accepted) == 0 {
			fmt.Fprintln(w, "codeforge review: no accepted findings on this PR.")
		}
		for _, a := range rec.Accepted {
			fmt.Fprintf(w, "- %s:%d %s", a.File, a.Line, a.What)
			if a.Reason != "" {
				fmt.Fprintf(w, " (accepted: %s)", a.Reason)
			}
			fmt.Fprintln(w)
		}
		return nil
	case word == "accept" && len(rest) >= 1:
		path, line, err := review.ParsePlace(rest[0])
		if err != nil {
			return err
		}
		var a review.Accepted
		if _, err := update(func(rec *review.Record) error {
			var aerr error
			a, aerr = rec.Accept(path, line, strings.Join(rest[1:], " "), now)
			return aerr
		}); err != nil {
			return fmt.Errorf("review: %w", err)
		}
		fmt.Fprintf(w, "codeforge review: accepted %s:%d; later rounds on this PR leave it out, and its thread no longer blocks.\n", a.File, a.Line)
		return nil
	case word == "unaccept" && len(rest) == 1:
		path, line, err := review.ParsePlace(rest[0])
		if err != nil {
			return err
		}
		if _, err := update(func(rec *review.Record) error {
			if !rec.Unaccept(path, line) {
				return fmt.Errorf("%w: %s:%d", errNotAcceptedOnPR, path, line)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("review: %w", err)
		}
		fmt.Fprintf(w, "codeforge review: %s:%d counts again in later rounds.\n", path, line)
		return nil
	}
	return errReviewAcceptUsage
}

// dropAccepted leaves out of a review the findings the user accepted as they are, and says how many went.
func dropAccepted(text string, list []review.Accepted) (string, int) {
	lines := reviewLines(text)
	spans := findingSpans(lines)
	dropped := 0
	for i := len(spans) - 1; i >= 0; i-- {
		s := spans[i]
		if !review.AcceptedAt(list, s.F.Path, s.F.Line) {
			continue
		}
		end := s.End
		// The blank line that separated it goes with it, so no double gap is left.
		if end < len(lines) && strings.TrimSpace(lines[end]) == "" && (s.Start == 0 || strings.TrimSpace(lines[s.Start-1]) == "") {
			end++
		}
		lines = slices.Delete(lines, s.Start, end)
		dropped++
	}
	return strings.Join(lines, "\n"), dropped
}

// dropAcceptedLeads leaves out the jurors' leads at a place the user accepted as it is.
func dropAcceptedLeads(leads string, list []review.Accepted) string {
	if len(list) == 0 {
		return leads
	}
	header, body, found := strings.Cut(leads, review.LeadsHeader)
	if !found || strings.TrimSpace(header) != "" {
		return leads
	}
	var kept []string
	for _, l := range reviewLines(body) {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if m := leadLine.FindStringSubmatch(l); m != nil {
			path, line, _ := strings.Cut(m[1], ":")
			if n, err := strconv.Atoi(line); err == nil && review.AcceptedAt(list, path, n) {
				continue
			}
		}
		kept = append(kept, l)
	}
	if len(kept) == 0 {
		return ""
	}
	return review.LeadsHeader + strings.Join(kept, "\n") + "\n"
}
