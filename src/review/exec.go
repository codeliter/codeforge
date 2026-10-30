package review

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	// gh must act on this checkout's repository, never on one picked by the environment.
	cmd.Env = slices.DeleteFunc(cmd.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GH_REPO=") })
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		const shownArgs = 2
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args[:min(shownArgs, len(args))], " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
