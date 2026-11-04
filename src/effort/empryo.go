package effort

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// DefaultJudges pairs two model families so one family's blind spots do not decide alone.
var DefaultJudges = []string{"copilot/claude-haiku-5.5", "copilot/gpt-6-luna"}

const maxStderrBytes = 200

var errEmpryo = errors.New("empryo run failed")

// EmpryoJudge runs a single headless Empryo turn with no profile, repo map or tools context.
type EmpryoJudge struct {
	Bin   string
	Model string
}

func (j EmpryoJudge) Ask(ctx context.Context, system, user string) (string, error) {
	cmd := exec.CommandContext(ctx, j.Bin,
		"--headless", "--json", "--no-profile", "--no-genome",
		"--model", j.Model, "--effort", "low", "--max-steps", "1",
		"--system", system, user)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()

	var res struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		if runErr != nil {
			return "", fmt.Errorf("running empryo: %w: %s", runErr, truncate(stderr.String(), maxStderrBytes))
		}
		return "", fmt.Errorf("decoding empryo result: %w", err)
	}
	// Some models answer through a final tool call, which uses the single step and makes Empryo exit with
	// "Max steps reached" even though the answer is complete. A usable answer counts; only no answer is an error.
	if res.Output != "" {
		return res.Output, nil
	}
	if res.Error != "" {
		return "", fmt.Errorf("%w: %s", errEmpryo, res.Error)
	}
	if runErr != nil {
		return "", fmt.Errorf("running empryo: %w: %s", runErr, truncate(stderr.String(), maxStderrBytes))
	}
	return "", fmt.Errorf("%w: empty answer", errEmpryo)
}
