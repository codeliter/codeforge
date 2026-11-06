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
	fmt.Fprintln(stderr, d.Reason)
	return hookExitBlock
}
