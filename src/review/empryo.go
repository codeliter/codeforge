package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

var errEmpryoRun = errors.New("review: empryo run failed")

const maxStderrShown = 300

// EmpryoReviewer runs one read-only headless Empryo juror on a fixed model, sized by the triage tier.
type EmpryoReviewer struct {
	Bin   string
	Model string
	Dir   string
	Tier  Tier
	// Wrap runs the juror inside an OS sandbox that keeps the repository read-only.
	Wrap func(ctx context.Context, dir string, argv []string) *exec.Cmd
}

func (r EmpryoReviewer) Name() string {
	return r.Model
}

func (r EmpryoReviewer) Review(ctx context.Context, brief string) (string, error) {
	args := []string{
		"--headless", "--json", "--profile", "codeforge-gate", "--mode", "plan",
		"--model", r.Model, "--effort", fitEffort(r.Tier.Effort, supportedEfforts(ctx, r.Bin, r.Model)), "--cwd", r.Dir,
		"--max-steps", strconv.Itoa(r.Tier.MaxSteps), "--marionette-mode", "none",
	}
	if !r.Tier.Genome {
		args = append(args, "--no-genome")
	}
	argv := append([]string{r.Bin}, append(args, brief+VerdictInstructions)...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if r.Wrap != nil {
		cmd = r.Wrap(ctx, r.Dir, argv)
	}
	cmd.Env = append(cmd.Environ(), "CODEFORGE_GUARD=review", "EMPRYO_HOOK_ENV=CODEFORGE_*")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := stderr.String()
		if len(msg) > maxStderrShown {
			msg = msg[len(msg)-maxStderrShown:]
		}
		return "", fmt.Errorf("%w: %s: %w: %s", errEmpryoRun, r.Model, err, strings.TrimSpace(msg))
	}

	var res struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("decoding %s result: %w", r.Model, err)
	}
	if res.Error != "" {
		return res.Output, fmt.Errorf("%w: %s: %s", errEmpryoRun, r.Model, res.Error)
	}
	return res.Output, nil
}
