package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/codeliter/codeforge/src/review"
)

// EngineExitError preserves only a supervised engine's exit status, not setup failures.
type EngineExitError struct{ Code int }

func (e *EngineExitError) Error() string { return fmt.Sprintf("empryo exited with status %d", e.Code) }

const signalExitBase = 128

// runInteractive keeps the terminal's foreground process group intact. Terminal
// interrupts reach Empryo directly; forwarding them would deliver Ctrl+C twice.
func runInteractive(ctx context.Context, argv, env []string) error {
	return runEngine(ctx, argv, env, time.Time{})
}

// deadlineExit is the status of a run stopped at its deadline, the one Empryo itself uses for a timeout.
const deadlineExit = 2

// runEngine is runInteractive with an optional hard deadline. A run with one gets its own process group, so the whole
// tree dies at the deadline; it is then no longer in the terminal's foreground group, so interrupts are forwarded.
func runEngine(ctx context.Context, argv, env []string, limit time.Time) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("launch cancelled: %w", err)
	}
	const signalBuffer = 2
	signals := make(chan os.Signal, signalBuffer)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	state, stateErr := term.GetState(int(os.Stdin.Fd()))
	// Cancellation from main's signal context must not kill Empryo before it saves.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	var expired <-chan time.Time
	if !limit.IsZero() {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		timer := time.NewTimer(time.Until(limit))
		defer timer.Stop()
		expired = timer.C
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting empryo: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var result, forwardErr error
	timedOut := false
	running := true
	for running {
		select {
		case result = <-done:
			running = false
		case <-expired:
			select {
			case result = <-done:
				// The engine finished in the same instant as the deadline: it did finish.
				running = false
				continue
			default:
			}
			timedOut = true
			expired = nil
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				forwardErr = fmt.Errorf("stopping the run at its deadline: %w", err)
			}
		case sig := <-signals:
			if unix, ok := sig.(syscall.Signal); ok && !limit.IsZero() {
				// Not in the foreground group, so the terminal's Ctrl+C does not reach the engine or its tools on its own.
				if err := syscall.Kill(-cmd.Process.Pid, unix); err != nil && !errors.Is(err, syscall.ESRCH) {
					forwardErr = fmt.Errorf("forwarding %s: %w", sig, err)
				}
			} else if sig == syscall.SIGTERM {
				// SIGTERM sent to Codeforge's PID must also reach its child.
				if err := cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
					forwardErr = fmt.Errorf("forwarding termination: %w", err)
				}
			}
		}
	}
	if !limit.IsZero() {
		// The engine is gone; whatever it started must not outlive it.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			forwardErr = errors.Join(forwardErr, fmt.Errorf("cleaning up the engine's processes: %w", err))
		}
	}
	if stateErr == nil {
		if err := term.Restore(int(os.Stdin.Fd()), state); err != nil {
			return errors.Join(result, forwardErr, fmt.Errorf("restoring terminal: %w", err))
		}
	}
	if !hasFlag(argv, "--headless") && showExitHint(result) {
		id, _, err := sessionID(argv)
		cwd, cwdErr := os.Getwd()
		if cwdErr == nil {
			cwd, cwdErr = filepath.EvalSymlinks(cwd)
		}
		if err == nil && cwdErr == nil && id != "" && verifyEmpryoSession(cwd, id) == nil && sessionHasActivity(cwd, id) {
			colors := resumeHintColors(os.Stderr)
			fmt.Fprint(os.Stderr, sessionSummary(context.WithoutCancel(ctx), cwd, colors), resumeHint(cwd, id, colors))
		}
	}
	if timedOut {
		fmt.Fprintf(os.Stderr, "codeforge: the run passed its time limit (%s per reviewer, %s in all) and was stopped.\n", review.AgentLimit, review.HardLimit)
		return errors.Join(&EngineExitError{Code: deadlineExit}, forwardErr)
	}
	var exit *exec.ExitError
	if forwardErr == nil && errors.As(result, &exit) {
		code := exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = signalExitBase + int(status.Signal())
		}
		return &EngineExitError{Code: code}
	}
	return errors.Join(result, forwardErr)
}

func showExitHint(err error) bool {
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	if exit.ExitCode() == signalExitBase+int(syscall.SIGINT) || exit.ExitCode() == signalExitBase+int(syscall.SIGTERM) {
		return true
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && (status.Signal() == syscall.SIGINT || status.Signal() == syscall.SIGTERM)
}
