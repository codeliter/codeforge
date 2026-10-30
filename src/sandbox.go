package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/codeliter/codeforge/src/guard"
)

const sandboxExec = "/usr/bin/sandbox-exec"

var errNoSandbox = errors.New("review mode needs an OS sandbox; only macOS sandbox-exec is supported so far")

// readOnlyRepo wraps a command so the operating system refuses every write inside the repository,
// except Empryo's own .empryo state. This replaces guessing which commands write.
func readOnlyRepo(ctx context.Context, argv []string) ([]string, error) {
	if runtime.GOOS != "darwin" {
		return nil, errNoSandbox
	}
	if _, err := os.Stat(sandboxExec); err != nil {
		return nil, fmt.Errorf("review mode needs %s: %w", sandboxExec, err)
	}

	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("finding working directory: %w", err)
	}
	repo, err := guard.RepoRoot(ctx, wd)
	if err != nil {
		repo = wd
	}
	repo, err = filepath.EvalSymlinks(repo)
	if err != nil {
		return nil, fmt.Errorf("resolving repository: %w", err)
	}

	return append([]string{sandboxExec, "-p", sandboxProfile(repo)}, argv...), nil
}

// sandboxProfile is a Seatbelt policy; for file writes the last matching rule wins.
func sandboxProfile(repo string) string {
	q := func(p string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"` }
	return "(version 1)(allow default)" +
		"(deny file-write* (subpath " + q(repo) + "))" +
		"(allow file-write* (subpath " + q(filepath.Join(repo, ".empryo")) + "))"
}

// sandboxCommand builds an exec.Cmd for a read-only juror.
func sandboxCommand(ctx context.Context, repo string, argv []string) *exec.Cmd {
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		resolved = repo
	}
	full := append([]string{"-p", sandboxProfile(resolved)}, argv...)
	return exec.CommandContext(ctx, sandboxExec, full...)
}
