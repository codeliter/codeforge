package effort

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// DefaultJudges pairs two model families so one family's blind spots do not decide alone.
var DefaultJudges = []string{"copilot/claude-haiku-5.5", "copilot/gpt-6-luna"}

const maxStderrBytes = 200

var errEmpryo = errors.New("empryo run failed")

// Usage is what one headless Empryo call spent, as the engine reports it.
// The JSON keys keep the field names, which scripts/gate-cost.sh reads from usage lines.
type Usage struct {
	Model      string        `json:"Model"`      //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	Input      int           `json:"Input"`      //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	Output     int           `json:"Output"`     //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	CacheRead  int           `json:"CacheRead"`  //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	CacheWrite int           `json:"CacheWrite"` //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	Steps      int           `json:"Steps"`      //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	Cost       float64       `json:"Cost"`       //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	Duration   time.Duration `json:"Duration"`   //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
}

// UsageOf reads the usage block of an Empryo --json result.
func UsageOf(model string, res EmpryoResult) Usage {
	return Usage{
		Model: model, Input: res.Tokens.Input, Output: res.Tokens.Output,
		CacheRead: res.Tokens.CacheRead, CacheWrite: res.Tokens.CacheWrite,
		Steps: res.Steps, Cost: res.Cost, Duration: time.Duration(res.DurationMS) * time.Millisecond,
	}
}

// EmpryoResult is the part of Empryo's --json output codeforge reads.
type EmpryoResult struct {
	Output string  `json:"output"`
	Error  string  `json:"error"`
	Steps  int     `json:"steps"`
	Cost   float64 `json:"cost"`
	// DurationMS is the engine's wall time for the call.
	DurationMS int64 `json:"duration"`
	Tokens     struct {
		Input      int `json:"input"`
		Output     int `json:"output"`
		CacheRead  int `json:"cacheRead"`  //nolint:tagliatelle // The engine's result keys.
		CacheWrite int `json:"cacheWrite"` //nolint:tagliatelle // The engine's result keys.
	} `json:"tokens"`
}

// EmpryoJudge runs a single headless Empryo turn with no profile, repo map or tools context.
type EmpryoJudge struct {
	Bin   string
	Model string
	// Sink, when set, receives the call's usage.
	Sink func(Usage)
	// Steps is the step budget; zero means one. A judge that answers through a final tool call spends its only step
	// on it and exits with "Max steps reached", so callers that cannot afford to lose a vote give it two.
	Steps int
}

func (j EmpryoJudge) Ask(ctx context.Context, system, user string) (string, error) {
	cmd := exec.CommandContext(ctx, j.Bin,
		"--headless", "--json", "--no-profile", "--no-genome",
		"--model", j.Model, "--effort", "low", "--max-steps", "1",
		"--system", system, user)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()

	var res EmpryoResult
	if err := json.Unmarshal(out, &res); err != nil {
		if runErr != nil {
			return "", fmt.Errorf("running empryo: %w: %s", runErr, truncate(stderr.String(), maxStderrBytes))
		}
		return "", fmt.Errorf("decoding empryo result: %w", err)
	}
	if j.Sink != nil {
		j.Sink(UsageOf(j.Model, res))
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
