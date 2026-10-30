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
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("running empryo: %w: %s", err, truncate(stderr.String(), maxStderrBytes))
	}

	var res struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("decoding empryo result: %w", err)
	}
	if res.Error != "" {
		return "", fmt.Errorf("%w: %s", errEmpryo, res.Error)
	}
	return res.Output, nil
}
