package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/review"
)

var (
	errGateUsage   = errors.New("usage: codeforge gate [accept PATH:LINE [REASON...] | unaccept PATH:LINE | accepted | reset]")
	errNoFailedRun = errors.New("gate: no failed gate run on this branch to accept a finding from")
	errNotAccepted = errors.New("gate: no accepted finding there; `codeforge gate accepted` lists them")
)

// gateCapAdvice says what a person can do once the gate stops: the agent cannot run either command (see guard.classify).
const gateCapAdvice = "Accept a finding you will not fix with `codeforge gate accept PATH:LINE [REASON]`, or allow more runs with " +
	"`codeforge gate reset`; only the user can run either."

func gateRounds(home string) review.Rounds {
	return review.Rounds{Dir: filepath.Join(config.Dir(home), "review-rounds")}
}

func gateAccepted(home string) review.AcceptedStore {
	return review.AcceptedStore{Dir: filepath.Join(config.Dir(home), "review-accepted")}
}

// printCapped shows what is still blocking when the gate will not run again.
func printCapped(w io.Writer, prev review.Round) {
	fmt.Fprintf(w, "codeforge gate: %d failed runs on this branch since its last pass; not reviewing it again.\n", prev.FailedRuns())
	if len(prev.Blocking) > 0 {
		fmt.Fprintln(w, "Still blocking from the last run:")
		for _, f := range prev.Blocking {
			fmt.Fprintf(w, "- %s:%d %s\n", f.File, f.Line, f.What)
		}
	}
	fmt.Fprintln(w, gateCapAdvice)
}

// reportAccepted lists the findings the reviewers raised again that the user had accepted, so nothing is hidden.
func reportAccepted(w io.Writer, fs []review.Finding) {
	if len(fs) == 0 {
		return
	}
	fmt.Fprintln(w, "\nAccepted, not fixing (not blocking):")
	for _, f := range fs {
		fmt.Fprintf(w, "- %s:%d %s\n", f.File, f.Line, f.What)
	}
}

// runGateCommand manages the gate's state for the current branch: the findings the user accepted and the run cap.
func runGateCommand(ctx context.Context, home string, args []string, w io.Writer) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding working directory: %w", err)
	}
	repo, err := guard.RepoRoot(ctx, wd)
	if err != nil {
		return fmt.Errorf("gate: %w", err)
	}
	local, err := review.LoadLocal(ctx, review.ExecRunner{})
	if err != nil {
		return fmt.Errorf("collecting changes: %w", err)
	}
	return gateCommand(gateRounds(home), gateAccepted(home), review.RoundKey(repo, local.Branch, local.Base), args, w, time.Now())
}

func gateCommand(rounds review.Rounds, accepts review.AcceptedStore, key string, args []string, w io.Writer, now time.Time) error {
	list := accepts.Load(key, now)
	switch {
	case len(args) == 1 && args[0] == "accepted":
		if len(list) == 0 {
			fmt.Fprintln(w, "codeforge gate: no accepted findings on this branch.")
		}
		for _, a := range list {
			fmt.Fprintf(w, "- %s:%d %s", a.File, a.Line, a.What)
			if a.Reason != "" {
				fmt.Fprintf(w, " (accepted: %s)", a.Reason)
			}
			fmt.Fprintln(w)
		}
		return nil
	case len(args) == 1 && args[0] == "reset":
		rounds.Clear(key)
		fmt.Fprintf(w, "codeforge gate: run count cleared; the next %d runs on this branch review it again.\n", review.MaxGateRuns)
		return nil
	case len(args) >= 2 && args[0] == "accept":
		path, line, err := review.ParsePlace(args[1])
		if err != nil {
			return err
		}
		prev, ok := rounds.Load(key, now)
		if !ok {
			return errNoFailedRun
		}
		updated, a, err := review.Accept(list, prev.Blocking, path, line, strings.Join(args[2:], " "), now)
		if err != nil {
			return fmt.Errorf("gate: %w", err)
		}
		if err := accepts.Save(key, updated, now); err != nil {
			return fmt.Errorf("gate: %w", err)
		}
		fmt.Fprintf(w, "codeforge gate: accepted %s:%d; it no longer blocks on this branch.\n", a.File, a.Line)
		// Accepting the last finding that blocked leaves nothing to fix, so the branch gets its runs back.
		left, _, _ := review.SetAside(prev.Blocking, updated)
		if len(left) == 0 {
			rounds.Clear(key)
			fmt.Fprintln(w, "codeforge gate: nothing from the last run blocks now; run `codeforge gate` to record a pass.")
			return nil
		}
		prev.Blocking = left
		if err := rounds.Save(key, prev); err != nil {
			return fmt.Errorf("gate: %w", err)
		}
		fmt.Fprintf(w, "codeforge gate: %d finding(s) from the last run still block.\n", len(left))
		return nil
	case len(args) == 2 && args[0] == "unaccept":
		path, line, err := review.ParsePlace(args[1])
		if err != nil {
			return err
		}
		updated, ok := review.Unaccept(list, path, line)
		if !ok {
			return fmt.Errorf("%w: %s:%d", errNotAccepted, path, line)
		}
		if err := accepts.Save(key, updated, now); err != nil {
			return fmt.Errorf("gate: %w", err)
		}
		fmt.Fprintf(w, "codeforge gate: %s:%d blocks again when a reviewer reports it.\n", path, line)
		return nil
	}
	return errGateUsage
}
