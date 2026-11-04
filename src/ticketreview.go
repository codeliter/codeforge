package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// maxGateRounds bounds how often codeforge sends blocking findings back to the agent before handing over.
	maxGateRounds = 3
	gateFixPrompt = "codeforge gate found blocking issues in your change. Fix every Blocking finding below, re-run the build, lint and tests, and stop. Do not open the PR.\n\n<gate>\n%s\n</gate>"
)

// gateRunner reviews the working tree and returns the report and whether it passed.
type gateRunner func(ctx context.Context) (report string, passed bool, err error)

// engineTurn runs one more headless turn in the saved session with this prompt.
type engineTurn func(ctx context.Context, prompt string) error

// ticketReviewLoop runs the review gate after the ticket's implementation turn, and gives blocking findings back to
// the agent until the gate passes or the rounds run out. It never blocks the TUI from opening: a failing or broken
// gate is reported, and the user continues from there.
func ticketReviewLoop(ctx context.Context, gate gateRunner, fix engineTurn, w io.Writer) {
	for round := 1; round <= maxGateRounds; round++ {
		fmt.Fprintf(w, "codeforge: reviewing the change (round %d of %d)...\n", round, maxGateRounds)
		report, passed, err := gate(ctx)
		fmt.Fprint(w, report)
		switch {
		case err != nil:
			// The reviewers could not give a verdict (unreachable, timed out, no diff): nothing to send back.
			fmt.Fprintln(w, "codeforge: review could not run:", err)
			fmt.Fprintln(w, "codeforge: run `codeforge gate` in the session once the reviewers are reachable.")
			return
		case passed:
			fmt.Fprintln(w, "codeforge: review passed.")
			return
		case round == maxGateRounds:
			fmt.Fprintf(w, "codeforge: review still blocking after %d rounds; the findings are above. Continue in the session.\n", maxGateRounds)
			return
		}
		fmt.Fprintln(w, "codeforge: sending the blocking findings back to the agent...")
		if err := fix(ctx, fmt.Sprintf(gateFixPrompt, strings.TrimSpace(report))); err != nil {
			fmt.Fprintln(w, "codeforge: fixing the findings failed:", err)
			return
		}
	}
}

// codeforgeGate adapts runGate: the report text and whether the gate passed.
func codeforgeGate(home, bin string, cfg Config) gateRunner {
	return func(ctx context.Context) (string, bool, error) {
		var out strings.Builder
		err := runGate(ctx, home, bin, cfg, &out)
		if errors.Is(err, errGateFailed) {
			return out.String(), false, nil
		}
		return out.String(), err == nil, err
	}
}

// sessionTurn runs one headless turn in the saved session, the same way the first ticket turn ran.
func sessionTurn(home, bin, id string) engineTurn {
	return func(ctx context.Context, prompt string) error {
		if id == "" {
			return errSessionMissing
		}
		argv, env, err := launchCommand(ctx, home, bin, modeTicket, []string{"--session", id, "--headless", prompt})
		if err != nil {
			return err
		}
		return runInteractive(ctx, argv, env)
	}
}

// ticketAfterTask is the step launch runs between the ticket's first turn and opening the TUI.
func ticketAfterTask(home, bin string, cfg Config) func(ctx context.Context, id string) {
	return func(ctx context.Context, id string) {
		ticketReviewLoop(ctx, codeforgeGate(home, bin, cfg), sessionTurn(home, bin, id), os.Stderr)
	}
}
