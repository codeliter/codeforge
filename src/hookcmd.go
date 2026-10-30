package src

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/codeliter/codeforge/src/guard"
)

var errAllowUsage = errors.New("usage: codeforge allow <rule>")

const hookExitBlock = 2

// runHook is the PreToolUse hook Empryo calls before every tool. For push and gh it only warns: git's pre-push
// hook and the gh shim enforce and spend those approvals. Rules with no later boundary are enforced and spent here.
func runHook(ctx context.Context, home string, stdin io.Reader, stderr io.Writer) int {
	var in guard.Input
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		fmt.Fprintln(stderr, "codeforge guard: unreadable tool call, blocking:", err)
		return hookExitBlock
	}

	unlock, err := guard.Lock(stateDir(home))
	if err != nil {
		fmt.Fprintln(stderr, "codeforge guard:", err)
		return hookExitBlock
	}
	defer unlock()

	st, err := guard.Load(stateDir(home))
	if err != nil {
		fmt.Fprintln(stderr, "codeforge guard:", err)
		return hookExitBlock
	}

	readOnly := os.Getenv("CODEFORGE_GUARD") == "review"
	d := guard.Decide(ctx, st, in, readOnly, time.Now())
	if d.Consumed {
		if err := st.Save(stateDir(home)); err != nil {
			fmt.Fprintln(stderr, "codeforge guard:", err)
			return hookExitBlock
		}
	}

	if d.Allow {
		return 0
	}
	fmt.Fprintln(stderr, d.Reason)
	return hookExitBlock
}

// runAllow is for the user only: the guard blocks the agent from running it.
func runAllow(ctx context.Context, home string, args []string) error {
	if len(args) != 1 || !guard.Allowable(args[0]) {
		return fmt.Errorf("%w; rules: %v", errAllowUsage, guard.AllowRules)
	}

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding working directory: %w", err)
	}
	repo, err := guard.RepoRoot(ctx, wd)
	if err != nil {
		repo = wd
	}

	unlock, err := guard.Lock(stateDir(home))
	if err != nil {
		return fmt.Errorf("allow: %w", err)
	}
	defer unlock()

	st, err := guard.Load(stateDir(home))
	if err != nil {
		return fmt.Errorf("allow: %w", err)
	}
	st.Allow(repo, args[0], time.Now())
	if err := st.Save(stateDir(home)); err != nil {
		return fmt.Errorf("allow: %w", err)
	}

	fmt.Fprintf(os.Stderr, "codeforge: allowed one %s in %s for the next %s\n", args[0], repo, guard.ApprovalTTL)
	return nil
}
