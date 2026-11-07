package src

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/codeliter/codeforge/src/guard"
)

const hookExitBlock = 2

// noAskEnv is set on runs with no person at a terminal to approve a call; the hook then refuses what it would ask about.
const noAskEnv = "CODEFORGE_NO_ASK"

// hookOut is where a hook answer for Empryo goes: its standard output.
var hookOut io.Writer = os.Stdout

// errAllowRemoved answers the retired `codeforge allow`: nothing needs a terminal approval any more.
var errAllowRemoved = errors.New("codeforge allow was removed: opening a PR, merges, comments and infra changes need no approval")

// runHook is the PreToolUse hook Empryo calls before every tool. In review sessions (CODEFORGE_GUARD=review) it
// keeps the review read-only (no writes, pushes, PR comments or merges) and stops the agent from changing
// codeforge's settings or switching the guard off. Other sessions are not gated, except that the agent may not
// write learning evidence (codeforge observe, codeforge learn).
func runHook(_ context.Context, _ string, stdin io.Reader, stderr io.Writer) int {
	review := os.Getenv("CODEFORGE_GUARD") == "review"

	var in guard.Input
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		if !review {
			return 0
		}
		fmt.Fprintln(stderr, "codeforge guard: unreadable tool call, blocking:", err)
		return hookExitBlock
	}

	d := guard.DecideOutsideReview(in)
	if review {
		d = guard.Decide(in, true)
	}
	if d.Allow {
		return 0
	}
	if d.Ask && os.Getenv(noAskEnv) == "1" {
		// A headless run has no one to approve the call, and must not rely on how the engine answers an ask.
		fmt.Fprintln(stderr, d.Reason+" Nobody is here to approve it: run codeforge review with --comment to post the review.")
		return hookExitBlock
	}
	if d.Ask {
		// Empryo shows the user the call and this reason, and runs it only if they approve in their session.
		if err := json.NewEncoder(hookOut).Encode(map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "ask",
			"permissionDecisionReason": d.Reason,
		}}); err != nil {
			fmt.Fprintln(stderr, "codeforge guard: could not ask for approval, blocking:", err)
			return hookExitBlock
		}
		return 0
	}
	fmt.Fprintln(stderr, d.Reason)
	return hookExitBlock
}
