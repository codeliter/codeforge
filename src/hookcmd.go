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

// runHook is the PreToolUse hook Empryo calls before every tool. It keeps review mode read-only and stops the
// agent from changing codeforge's settings or switching the guard off.
func runHook(_ context.Context, _ string, stdin io.Reader, stderr io.Writer) int {
	var in guard.Input
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		fmt.Fprintln(stderr, "codeforge guard: unreadable tool call, blocking:", err)
		return hookExitBlock
	}

	d := guard.Decide(in, os.Getenv("CODEFORGE_GUARD") == "review")
	if d.Allow {
		return 0
	}
	fmt.Fprintln(stderr, d.Reason)
	return hookExitBlock
}
