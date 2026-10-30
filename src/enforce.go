package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/codeliter/codeforge/src/guard"
)

const (
	hookName  = "codeforge"
	shimMagic = "# codeforge gh shim"
	execPerm  = 0o755
)

var (
	errNoRealGH     = errors.New("gh is not installed")
	errForeignGH    = errors.New("exists and is not codeforge's; move it and run codeforge again")
	errHookInactive = errors.New("git does not list codeforge as a pre-push hook; git 2.54 or newer is required")
)

// installPrePush registers codeforge as a git pre-push hook through git's hook config. Unlike a file in
// .git/hooks it runs alongside husky and core.hooksPath setups instead of replacing them.
func installPrePush(ctx context.Context, repo, self string) error {
	want := shellQuote(self) + " pre-push"
	if gitConfig(ctx, repo, "hook."+hookName+".command") == want && gitConfig(ctx, repo, "hook."+hookName+".event") == "pre-push" {
		return nil
	}

	for _, kv := range [][2]string{{"hook." + hookName + ".event", "pre-push"}, {"hook." + hookName + ".command", want}} {
		if err := exec.CommandContext(ctx, "git", "-C", repo, "config", "--local", kv[0], kv[1]).Run(); err != nil {
			return fmt.Errorf("registering the pre-push hook: %w", err)
		}
	}
	return nil
}

// prePushInstalled is true when git will run codeforge before a push in this repository.
func prePushInstalled(ctx context.Context, repo string) bool {
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "hook", "list", "pre-push").Output()
	return err == nil && strings.Contains(string(out), hookName)
}

func gitConfig(ctx context.Context, repo, key string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// installGHShim puts a gh wrapper first on the agent's PATH. Empryo puts ~/.empryo/bin first in every shell it
// starts, so the agent's gh calls pass through codeforge however they were built.
func installGHShim(empryoDir, self string) error {
	body := fmt.Sprintf("#!/bin/sh\n%s\nexec %s gh-shim \"$@\"\n", shimMagic, shellQuote(self))
	p := filepath.Join(empryoDir, "bin", "gh")
	if have, err := os.ReadFile(p); err == nil && string(have) == body {
		return nil
	}
	if have, err := os.ReadFile(p); err == nil && !strings.Contains(string(have), shimMagic) {
		return fmt.Errorf("%s %w", p, errForeignGH)
	}
	return writeFile(empryoDir, "bin/gh", []byte(body), execPerm)
}

// realGH finds gh on PATH, skipping codeforge's own shim.
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

// runPrePush is called by git with the push updates on stdin. A non-zero exit stops the push.
func runPrePush(ctx context.Context, home string, stdin io.Reader, stderr io.Writer) int {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "codeforge:", err)
		return 1
	}
	repo, err := guard.RepoRoot(ctx, wd)
	if err != nil {
		fmt.Fprintln(stderr, "codeforge:", err)
		return 1
	}

	return withState(home, stderr, func(st guard.State) error {
		return guard.CheckPush(ctx, st, repo, guard.ParsePushUpdates(stdin), time.Now())
	})
}

// runGHShim checks the call and then replaces itself with the real gh, keeping gh's exit code and output.
func runGHShim(ctx context.Context, home string, args []string) int {
	gh, err := realGH(filepath.Join(home, ".empryo"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge:", err)
		return 1
	}

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge:", err)
		return 1
	}

	if code := withState(home, os.Stderr, func(st guard.State) error {
		return guard.CheckGH(ctx, st, wd, args, time.Now())
	}); code != 0 {
		return code
	}

	if err := syscall.Exec(gh, append([]string{gh}, args...), os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "codeforge: starting gh:", err)
		return 1
	}
	return 0
}

// withState runs check under the state lock and saves only when it succeeds, so refused actions spend nothing.
func withState(home string, stderr io.Writer, check func(guard.State) error) int {
	unlock, err := guard.Lock(stateDir(home))
	if err != nil {
		fmt.Fprintln(stderr, "codeforge:", err)
		return 1
	}
	defer unlock()

	st, err := guard.Load(stateDir(home))
	if err != nil {
		fmt.Fprintln(stderr, "codeforge:", err)
		return 1
	}

	if err := check(st); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := st.Save(stateDir(home)); err != nil {
		fmt.Fprintln(stderr, "codeforge:", err)
		return 1
	}
	return 0
}

// ensureEnforced installs the pre-push hook in the current repository and refuses to launch without it.
func ensureEnforced(ctx context.Context, self string) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding working directory: %w", err)
	}

	// Outside a repository there is nothing to push, so there is nothing to enforce.
	repo, rootErr := guard.RepoRoot(ctx, wd)
	if rootErr != nil {
		return nil //nolint:nilerr // not being in a repository is a valid state, not a failure
	}

	if err := installPrePush(ctx, repo, self); err != nil {
		return err
	}
	if !prePushInstalled(ctx, repo) {
		return fmt.Errorf("%w (%s)", errHookInactive, repo)
	}
	return nil
}
