package effort

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const waitDelay = 2 * time.Second

// Bound makes a context-bound command die as a whole. Empryo starts tools and sub-processes; killing only its first
// process leaves them running, and holding its output open, after the deadline.
func Bound(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err != nil {
			return fmt.Errorf("killing process group: %w", err)
		}
		return nil
	}
	cmd.WaitDelay = waitDelay
}

// Reap kills whatever a finished Bound command left running in its process group. A parent that prints its result and
// exits while a tool it started keeps running would otherwise outlive the deadline it was bound to.
func Reap(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	//nolint:errcheck // ESRCH is the normal case: the group is already gone.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
