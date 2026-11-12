package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/codeliter/codeforge/src/jobs"
)

func startRunnerProcess(ctx context.Context, st jobs.Store, j *jobs.Job) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding codeforge: %w", err)
	}
	logFile, err := os.OpenFile(st.LogPath(j.ID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, privateFilePerm)
	if err != nil {
		return fmt.Errorf("opening the job log: %w", err)
	}
	defer logFile.Close() //nolint:errcheck // the runner holds its own copy
	// The runner outlives this command: nothing here may stop it.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), self, "job", "run", j.ID)
	cmd.Dir, cmd.Stdout, cmd.Stderr = j.Dir, logFile, logFile
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, launchMarker+"=") || strings.HasPrefix(kv, humanLaunchMarker+"=") || strings.HasPrefix(kv, noAskEnv+"=")
	})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the job runner: %w", err)
	}
	return cmd.Process.Release() //nolint:wrapcheck // never fails on unix
}

// jobRun is the runner: it takes the job to its end, then posts the result back into the session that scheduled it
// once that session is closed (a live one gets it from codeforge job wait).
func jobRun(ctx context.Context, home string, st jobs.Store, id string) error {
	// Signals are caught before anything else, so a cancel that arrives early is recorded, not lost with the process.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	// One runner per job: the lock goes with the process, however it ends. The job is read only under it, so a runner
	// that waited for another never works from a copy the other has finished.
	unlock, err := st.LockRunner(id)
	if err != nil {
		return err //nolint:wrapcheck // says which job
	}
	defer unlock()
	j, err := st.Load(id)
	if err != nil {
		return err //nolint:wrapcheck // says which job
	}
	// However the job ends here, its result is posted back like any other.
	deliver := func() {
		stop()
		if needsDelivery(st, j) {
			deliverWhenClosed(context.WithoutCancel(ctx), home, st, j)
		}
	}
	// A command that was running when its runner died is never run again: what is left of it is stopped.
	if settleOrphan(ctx, st, j) {
		deliver()
		return nil
	}
	if j.State.Final() {
		// A runner started again for a result that was never posted back.
		deliver()
		return nil
	}
	// The file could have been changed since it was scheduled: what runs is checked again, now.
	if err := scheduleRefusal(j); err != nil {
		j.State, j.Reason, j.Finished = jobs.Cancelled, err.Error(), time.Now()
		if serr := st.Save(j); serr != nil {
			return errors.Join(err, serr)
		}
		deliver()
		return err
	}
	// Saved before anything runs, so codeforge job cancel finds the runner from the start. A cancel asked for before this
	// is in its file, which jobs.Run reads before every round and right before the command starts.
	j.RunnerPID = os.Getpid()
	if err := st.Save(j); err != nil {
		return err //nolint:wrapcheck // says which job
	}
	deps := realDeps(st)
	deps.Cancelled = func() bool { return st.CancelRequested(id) }
	// A script the job names can change while it waits: every check round and the start are checked again.
	deps.Allowed = commandRefusal
	// The file is also watched while the runner sleeps or the command runs.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for ctx.Err() == nil && !st.CancelRequested(id) {
			time.Sleep(time.Second)
		}
		cancel()
	}()
	err = jobs.Run(ctx, j, deps)
	cancel() // the cancel file is not watched while the result waits to be posted
	if err != nil {
		return err //nolint:wrapcheck // the job's own error
	}
	// The job is over: from here a signal ends the runner as usual, so a result still waiting for its session to close
	// can be dropped with a kill. It stays readable with codeforge job show.
	stop()
	if needsDelivery(st, j) {
		// A signal that cancelled the job still has its cancel posted back.
		deliverWhenClosed(context.WithoutCancel(ctx), home, st, j)
	}
	return nil
}

func realDeps(st jobs.Store) jobs.Deps {
	return jobs.Deps{
		Now: time.Now,
		Sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err() //nolint:wrapcheck // the caller only checks for nil
			case <-t.C:
				return nil
			}
		},
		Check: func(ctx context.Context, dir, command string) (int, string) {
			cctx, cancel := context.WithTimeout(ctx, jobCheckLimit)
			defer cancel()
			out := &tailBuffer{max: jobOutputKeep}
			cmd := groupCommand(cctx, dir, command, out, out)
			// On the limit or a cancel the whole group goes, and output still held open by a child is not waited for.
			cmd.Cancel = func() error { stopGroup(cmd.Process.Pid, 0); return nil }
			cmd.WaitDelay = time.Second
			err := cmd.Run()
			code := exitCode(err)
			if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
			// A check is over when its shell is: anything it left in the background goes too.
			if cmd.Process != nil {
				stopGroup(cmd.Process.Pid, 0)
			}
			return code, out.String()
		},
		Start: func(ctx context.Context, j *jobs.Job) (<-chan jobs.Exit, func(), error) {
			out := &tailBuffer{max: jobOutputKeep}
			// The shell waits for one line on its input before it runs the command: the runner first saves which process
			// it is, so a runner that dies at any point never leaves a command it cannot find. Without the line (the
			// runner died first) it exits without running anything.
			release, gate, err := os.Pipe()
			if err != nil {
				return nil, nil, fmt.Errorf("starting the command: %w", err)
			}
			defer release.Close() //nolint:errcheck // the child has its copy
			// Stopped by stop below, not by ctx.
			cmd := groupCommand(context.WithoutCancel(ctx), j.Dir, `IFS= read -r _ || exit 125; exec sh -c "$1"`, io.MultiWriter(os.Stdout, out), io.MultiWriter(os.Stderr, out))
			cmd.Args = append(cmd.Args, "sh", j.Command)
			cmd.Stdin = release
			// A background child that keeps the output open does not keep the job running once the shell is gone.
			cmd.WaitDelay = jobPipeGrace
			if err := cmd.Start(); err != nil {
				_ = gate.Close() //nolint:errcheck // nothing started
				return nil, nil, fmt.Errorf("starting the command: %w", err)
			}
			j.CommandPID, j.CommandStart, j.CommandBoot = cmd.Process.Pid, processStart(cmd.Process.Pid), bootID(ctx)
			saveErr := st.Save(j)
			// The very last look: a cancel asked for since the runner's own check holds the command back.
			if saveErr == nil && (ctx.Err() != nil || st.CancelRequested(j.ID)) {
				saveErr = errHeldBack
			}
			if saveErr == nil {
				_, saveErr = gate.WriteString("\n")
			}
			_ = gate.Close() //nolint:errcheck // the line, or EOF, is what the shell waits for
			if saveErr != nil {
				// Not released: the shell exits on EOF and nothing ran.
				_ = cmd.Wait() //nolint:errcheck // it exits 125
				return nil, nil, fmt.Errorf("recording the command before it runs: %w", saveErr)
			}
			done := make(chan jobs.Exit, 1)
			go func() {
				err := cmd.Wait()
				// Whatever the shell left behind goes with it: nothing of the job runs unwatched by its --while checks.
				stopGroup(cmd.Process.Pid, jobStopGrace)
				code := exitCode(err)
				if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil {
					// The shell ended as it did; only a child it left in the background still held the output.
					code = cmd.ProcessState.ExitCode()
				}
				done <- jobs.Exit{Code: code, Output: out.String()}
			}()
			// Blocks until the whole group is gone, so the runner never exits with part of the command still running.
			stop := func() { stopGroup(cmd.Process.Pid, jobStopGrace) }
			return done, stop, nil
		},
		Save: st.Save,
		Log:  os.Stdout,
	}
}

// groupCommand runs a shell line in a process group of its own, so stopping it stops everything it started.
func groupCommand(ctx context.Context, dir, line string, stdout, stderr io.Writer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", line) //nolint:gosec // a job's command or check, checked by the guard when it was scheduled
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// stopGroup ends process group pgid: SIGTERM, then SIGKILL once grace has passed with any of it still alive.
func stopGroup(pgid int, grace time.Duration) {
	_ = syscall.Kill(-pgid, syscall.SIGTERM) //nolint:errcheck // it may be gone
	for end := time.Now().Add(grace); time.Now().Before(end); time.Sleep(groupPoll) {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL) //nolint:errcheck // it may be gone
}

// jobOutputKeep is how much output a command or check keeps in memory; the job keeps less of it.
const jobOutputKeep = 64 * 1024

// groupPoll is how often stopGroup looks whether the group is gone.
const groupPoll = 100 * time.Millisecond

// tailBuffer keeps the last max bytes written to it, so a command that prints for hours never fills memory.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func exitCode(err error) int {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee) && ee.ExitCode() >= 0:
		return ee.ExitCode()
	}
	return -1
}

func alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// deliveryWait is how long a finished job's result waits for its session to close before it is dropped; it can still be
// read with codeforge job show.
const (
	deliveryWait = 7 * 24 * time.Hour
	deliveryPoll = 30 * time.Second
)
