package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/codeliter/codeforge/src/guard"
)

const (
	hookName  = "codeforge"
	shimMagic = "# codeforge gh shim"
	execPerm  = 0o755
)

var errNoRealGH = errors.New("gh is not installed")

func gitConfig(ctx context.Context, repo, key string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// removeGHShim deletes the gh wrapper earlier versions installed, so gh runs directly. A gh that codeforge did
// not write is left alone.
func removeGHShim(empryoDir string) error {
	p := filepath.Join(empryoDir, "bin", "gh")
	have, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", p, err)
	}
	if !strings.Contains(string(have), shimMagic) {
		return nil
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the gh shim: %w", err)
	}
	return nil
}

// realGH finds gh on PATH, skipping a stale codeforge shim directory.
func realGH(empryoDir string) (string, error) {
	shimDir := filepath.Join(empryoDir, "bin")
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(dir) == shimDir {
			continue
		}
		p := filepath.Join(dir, "gh")
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	for _, p := range []string{"/opt/homebrew/bin/gh", "/usr/local/bin/gh", "/usr/bin/gh"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errNoRealGH
}

// runPrePush is kept so repositories that still register the old codeforge pre-push hook keep pushing:
// pushes are never blocked.
func runPrePush(_ context.Context, _ string, _ io.Reader, _ io.Writer) int {
	return 0
}

// runGHShim checks the call and then replaces itself with the real gh, keeping gh's exit code and output.
func runGHShim(_ context.Context, home string, args []string) int {
	gh, err := realGH(filepath.Join(home, ".empryo"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge:", err)
		return 1
	}

	if err := guard.CheckGH(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if err := syscall.Exec(gh, append([]string{gh}, args...), os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "codeforge: starting gh:", err)
		return 1
	}
	return 0
}

// ensureEnforced removes the pre-push hook earlier versions registered, so pushes are never gated.
func ensureEnforced(ctx context.Context, _ string) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding working directory: %w", err)
	}

	repo, rootErr := guard.RepoRoot(ctx, wd)
	if rootErr != nil {
		return nil //nolint:nilerr // not being in a repository is a valid state, not a failure
	}

	if gitConfig(ctx, repo, "hook."+hookName+".command") == "" {
		return nil
	}
	if err := exec.CommandContext(ctx, "git", "-C", repo, "config", "--local", "--remove-section", "hook."+hookName).Run(); err != nil {
		return fmt.Errorf("removing the old codeforge pre-push hook: %w", err)
	}
	return nil
}
