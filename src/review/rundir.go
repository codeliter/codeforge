package review

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// DirRunner runs one command in a directory. ExecRunner uses the OS; tests use a fake.
type DirRunner interface {
	RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// RunDir runs a command inside dir and returns its output; a non-zero exit is an error that still carries the output.
func (ExecRunner) RunDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = slices.DeleteFunc(cmd.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GH_REPO=") })
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s in %s: %w: %s", name, args[0], dir, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
