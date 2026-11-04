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

	"golang.org/x/term"
)

// EngineExitError preserves only a supervised engine's exit status, not setup failures.
type EngineExitError struct{ Code int }

func (e *EngineExitError) Error() string { return fmt.Sprintf("empryo exited with status %d", e.Code) }

const signalExitBase = 128

// runInteractive keeps the terminal's foreground process group intact. Terminal
// interrupts reach Empryo directly; forwarding them would deliver Ctrl+C twice.
func runInteractive(ctx context.Context, argv, env []string) error {
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
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting empryo: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var result, forwardErr error
	running := true
	for running {
		select {
		case result = <-done:
			running = false
		case sig := <-signals:
			if sig == syscall.SIGTERM {
				// SIGTERM sent to Codeforge's PID must also reach its child.
				if err := cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
					forwardErr = fmt.Errorf("forwarding termination: %w", err)
				}
			}
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
			fmt.Fprint(os.Stderr, resumeHint(cwd, id, resumeHintColors(os.Stderr)))
		}
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
