package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/codeliter/codeforge/src/config"

	"github.com/codeliter/codeforge/src/effort"
)

var (
	errEffortAboveCeiling = errors.New("effort is above the high ceiling")
	errEmpryoNotFound     = errors.New("empryo not found: run `codeforge init`")
)

var allowedEfforts = map[string]bool{"off": true, "none": true, "minimal": true, "low": true, "medium": true, "high": true}

// flagsWithValue are Empryo flags whose next argument is a value, not part of the prompt.
var flagsWithValue = map[string]bool{
	"--model": true, "--profile": true, "--agent": true, "--effort": true, "--mode": true, "--session": true, "--resume": true, "-s": true,
	"--session-id": true, "--fork": true, "--cwd": true, "--locale": true, "--system": true,
	"--max-steps": true, "--timeout": true, "--include": true, "--skill": true, "--image": true,
	"--thinking": true, "--thinking-budget": true, "--speed": true, "--max-iterations": true,
}

// taskArgs prepends the coding model and an effort to a task launch; an explicit --effort wins if within the ceiling.
func taskArgs(ctx context.Context, bin string, cfg config.Config, args []string) ([]string, error) {
	if err := checkEffort(args); err != nil {
		return nil, err
	}

	if !hasEffort(args) {
		level := effort.Medium
		if prompt := promptOf(args); prompt != "" {
			level = classify(ctx, bin, prompt, cfg.Judges)
		}
		args = append([]string{"--effort", level.String()}, args...)
	}

	if cfg.Model != "" && !hasFlag(args, "--model") {
		args = append([]string{"--model", cfg.Model}, args...)
	}
	return args, nil
}

func checkEffort(args []string) error {
	for i := 0; i < len(args); i++ {
		key, _, equal := strings.Cut(args[i], "=")
		if key == "--effort" {
			value, ok := effortFlag(args, i)
			if !ok || !allowedEfforts[value] {
				return fmt.Errorf("%w: %q", errEffortAboveCeiling, value)
			}
			if !equal {
				i++
			}
		} else if flagsWithValue[key] && !equal {
			i++
		}
	}
	return nil
}

func promptOf(args []string) string {
	var prompt []string
	for i := 0; i < len(args); i++ {
		switch {
		case flagsWithValue[args[i]]:
			i++
		case !strings.HasPrefix(args[i], "-"):
			prompt = append(prompt, args[i])
		}
	}
	return strings.Join(prompt, " ")
}

func classify(ctx context.Context, bin, task string, models []string) effort.Level {
	judges := make([]effort.Judge, len(models))
	for i, m := range models {
		judges[i] = effort.EmpryoJudge{Bin: bin, Model: m}
	}

	c, err := effort.New(effort.Options{Judges: judges})
	if err != nil {
		return effort.Medium
	}

	// Judge the described work only. The tree's uncommitted changes belong to earlier work, not this task,
	// and inflated the blast score.
	res, err := c.Classify(ctx, task, effort.Signals{})
	if len(res.Votes) == 0 {
		fmt.Fprintln(os.Stderr, "codeforge: effort judges failed, using medium:", err)
		return res.Level
	}

	fmt.Fprintf(os.Stderr, "codeforge: effort %s from %d/%d judges (%s)\n", res.Level, len(res.Votes), len(judges), res.Scores.Reason)
	return res.Level
}

func hasFlag(args []string, name string) bool {
	for i := 0; i < len(args); i++ {
		key, _, equal := strings.Cut(args[i], "=")
		if key == name {
			return true
		}
		if flagsWithValue[key] && !equal {
			i++
		}
	}
	return false
}

func hasEffort(args []string) bool { return hasFlag(args, "--effort") }

func effortFlag(args []string, i int) (string, bool) {
	if v, ok := strings.CutPrefix(args[i], "--effort="); ok {
		return v, true
	}
	if args[i] == "--effort" && i+1 < len(args) {
		return args[i+1], true
	}
	return "", false
}

func findEmpryo(empryoDir string) (string, error) {
	if bin, err := exec.LookPath("empryo"); err == nil {
		return bin, nil
	}

	bin := filepath.Join(empryoDir, "bin", "empryo")
	if _, err := os.Stat(bin); err != nil {
		return "", errEmpryoNotFound
	}
	return bin, nil
}

// flagValue returns the value of --name or --name=value, or "".
func flagValue(args []string, name string) string {
	value := ""
	for i := 0; i < len(args); i++ {
		key, v, equal := strings.Cut(args[i], "=")
		if key == name {
			if equal {
				value = v
			} else if i+1 < len(args) {
				i++
				value = args[i]
			}
		} else if flagsWithValue[key] && !equal {
			i++
		}
	}
	return value
}

// dropFlag removes --name and its value, so a flag codeforge has already applied is not applied twice.
func dropFlag(args []string, name string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		key, _, equal := strings.Cut(args[i], "=")
		if key == name {
			if flagsWithValue[key] && !equal && i+1 < len(args) {
				i++
			}
			continue
		}
		out = append(out, args[i])
		if flagsWithValue[key] && !equal && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	return out
}
