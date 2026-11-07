package review

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// ExecRunner runs commands with the OS. It is the Runner and DirRunner of every real review; tests use fakes.
type ExecRunner struct{}

// DirRunner runs one command in a directory.
type DirRunner interface {
	RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// Run runs a command in the working directory and returns its output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, stderr, err := execute(ctx, "", name, args...)
	if err != nil {
		const shownArgs = 2
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args[:min(shownArgs, len(args))], " "), err, stderr)
	}
	return out, nil
}

// RunDir runs a command inside dir and returns its output; a non-zero exit is an error that still carries the output.
func (ExecRunner) RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	out, stderr, err := execute(ctx, dir, name, args...)
	if err != nil {
		return out, fmt.Errorf("%s %s in %s: %w: %s", name, args[0], dir, err, stderr)
	}
	return out, nil
}

// execute runs the command bounded and reaped, without prompts, and returns its output and trimmed standard error.
// gh must act on this checkout's repository, never on one picked by the environment, so GH_REPO is dropped.
func execute(ctx context.Context, dir, name string, args ...string) ([]byte, string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(slices.DeleteFunc(cmd.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "GH_REPO=") || strings.HasPrefix(kv, "GH_PROMPT_DISABLED=") || strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=")
	}), noPrompts...)
	Bound(cmd)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	Reap(cmd)
	return out, strings.TrimSpace(stderr.String()), err
}
