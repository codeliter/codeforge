package src

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const pipeWaitDelay = 100 * time.Millisecond

const summaryTimeout = time.Second // Empryo gives a SessionEnd hook 3 seconds in total

// summarySandboxProfile lets git read but not write anywhere (except /dev/null) and not use the network.
const summarySandboxProfile = `(version 1)(allow default)(deny file-write*)(allow file-write* (literal "/dev/null"))(deny network*)`

var aheadCount = regexp.MustCompile(`ahead (\d+)`)

// sessionSummary says where the work stands when a session closes: branch, uncommitted changes, unpushed
// commits and the next step. It is empty outside a Git project or when git is slow, because it runs while
// the terminal is being handed back and must never delay or break it.
func sessionSummary(ctx context.Context, cwd string, color bool) string {
	ctx, cancel := context.WithTimeout(ctx, summaryTimeout)
	defer cancel()
	// git status can run a repository's own programs (clean filters, fsmonitor). This runs outside any Empryo
	// sandbox, so git itself runs in an OS sandbox that refuses all writes and all network access; without
	// that sandbox no summary is shown.
	if runtime.GOOS != "darwin" {
		return ""
	}
	if _, err := os.Stat(sandboxExec); err != nil {
		return ""
	}
	argv := []string{"-p", summarySandboxProfile, "git", "--no-optional-locks", "-C", cwd, "-c", "core.fsmonitor=false",
		"status", "--porcelain=v1", "--branch", "--untracked-files=all", "--", ":/", ":(top,exclude).empryo"}
	cmd := exec.CommandContext(ctx, sandboxExec, argv...)
	// A filter can leave a descendant holding the output pipes, so the whole process group is killed on timeout
	// and Wait gives up on the pipes shortly after; the deadline then really bounds shutdown.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = pipeWaitDelay
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if cmd.Process != nil {
		// Also reap any descendant a filter left behind, whether git finished or was cancelled.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck // nothing left to kill is fine
	}
	if err != nil {
		return ""
	}
	out := stdout.Bytes()
	return formatSummary(string(out), color)
}

// formatSummary turns git's porcelain status into the closing summary.
func formatSummary(porcelain string, color bool) string {
	lines := strings.Split(strings.TrimRight(porcelain, "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "## ") {
		return ""
	}
	header := strings.TrimPrefix(lines[0], "## ")
	// A branch with no commits yet is reported as "No commits yet on main" or "Initial commit on main".
	for _, prefix := range []string{"No commits yet on ", "Initial commit on "} {
		header = strings.TrimPrefix(header, prefix)
	}
	branch, _, _ := strings.Cut(header, "...")
	branch, _, _ = strings.Cut(branch, " ")
	if branch == "" || strings.ContainsFunc(branch, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return ""
	}
	changed, added := 0, 0
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "??") {
			added++
		} else if line != "" {
			changed++
		}
	}
	unpushed := 0
	if m := aheadCount.FindStringSubmatch(header); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			unpushed = n
		}
	}

	var parts []string
	if changed > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", changed))
	}
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d new", added))
	}
	if unpushed > 0 {
		parts = append(parts, fmt.Sprintf("%d unpushed", unpushed))
	}
	title, state, next := "Where things stand", "no changes", ""
	if len(parts) > 0 {
		state = strings.Join(parts, ", ")
		next = "\n    Before you push: codeforge gate"
	}
	if color {
		title = "\x1b[2m" + title + "\x1b[0m"
	}
	return fmt.Sprintf("\n  %s\n    %s: %s%s\n", title, branch, state, next)
}
