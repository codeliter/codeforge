// Package gitroot finds the repository a command was started in.
package gitroot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/effort"
)

const limit = 10 * time.Second

// Find is the top of the git repository holding the working directory, or the working directory itself outside one.
func Find(ctx context.Context) (string, error) {
	// Bounded on its own: it runs before any review clock, and a git that never answers must not stall every launch.
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	effort.Bound(cmd)
	out, err := cmd.Output()
	effort.Reap(cmd)
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("finding working directory: %w", err)
	}
	return wd, nil
}
