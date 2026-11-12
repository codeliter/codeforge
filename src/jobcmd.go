package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/jobs"

	"golang.org/x/term"
)

var (
	errJobUsage = errors.New("usage: codeforge job add [--name N] [--at TIME] [--until CMD]... [--while CMD]... " +
		"[--hold DUR] [--every DUR] [--expire DUR] -- COMMAND | list | show ID | wait ID | log ID | cancel ID")
	errJobInReview   = errors.New("review mode is read-only: a job cannot be scheduled from a review")
	errJobGuard      = errors.New("codeforge will not schedule this")
	errJobNoCommand  = errors.New("give the command to run after --")
	errJobNoWait     = errors.New("a job needs something to wait for: --at, --until or --hold")
	errJobFailed     = errors.New("job did not succeed")
	errJobRunnerGone = errors.New("the job's runner stopped before it finished")
	errHeldBack      = errors.New("cancelled before it started")
	// errSessionReopened is a post-back given up because a person opened the session first.
	errSessionReopened = errors.New("the session was opened again; the result is shown when it is resumed and by codeforge job show")
)

// jobStopGrace is how long a stopped command gets to exit before it is killed.
const jobStopGrace = 10 * time.Second

// jobPipeGrace is how long a command's output is read after its shell exits, for a child that keeps it open.
const jobPipeGrace = 2 * time.Second

// jobCheckLimit bounds one check; privateFilePerm keeps a job's log to its owner.
const (
	jobCheckLimit   = 2 * time.Minute
	privateFilePerm = 0o600
)

func jobStore(home string) jobs.Store {
	return jobs.Store{Dir: filepath.Join(config.Dir(home), "jobs")}
}

// runJob dispatches codeforge job.
func runJob(ctx context.Context, home string, args []string, w io.Writer) error {
	st := jobStore(home)
	if len(args) == 0 {
		return errJobUsage
	}
	switch {
	case args[0] == "add":
		return jobAdd(ctx, st, args[1:], w, time.Now())
	case args[0] == "list" && len(args) == 1:
		return jobList(st, w, time.Now())
	case args[0] == "show" && len(args) == 2:
		j, err := st.Load(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintln(w, jobDetail(j, time.Now()))
		return nil
	case args[0] == "wait" && len(args) == 2:
		return jobWait(ctx, st, args[1], w)
	case args[0] == "log" && len(args) == 2:
		// Through Load first: an id that is not a job's never names a file.
		if _, err := st.Load(args[1]); err != nil {
			return err //nolint:wrapcheck // says which job
		}
		f, err := os.Open(st.LogPath(args[1]))
		if err != nil {
			return fmt.Errorf("%w: %s", jobs.ErrNotFound, args[1])
		}
		defer f.Close() //nolint:errcheck // read only
		// Streamed: a command that printed for hours never has its whole log in memory.
		_, err = io.Copy(w, f)
		return err //nolint:wrapcheck // a write to the terminal
	case args[0] == "cancel" && len(args) == 2:
		return jobCancel(ctx, st, args[1], w)
	case args[0] == "run" && len(args) == 2:
		return jobRun(ctx, home, st, args[1])
	case slices.Contains([]string{"add", "list", "show", "wait", "log", "cancel", "run"}, args[0]):
		return errJobUsage
	}
	return fmt.Errorf("%w\n%w", noSuchCommand(append([]string{"job"}, args...)), errJobUsage)
}

// parseJob reads codeforge job add's flags; everything after -- is the command, run with sh -c.
func parseJob(args []string, now time.Time) (*jobs.Job, error) {
	j := &jobs.Job{ID: jobs.NewID(), State: jobs.Waiting, Created: now, Every: jobs.DefaultEvery, Expire: now.Add(jobs.DefaultExpire)}
	explicitExpire := false
	i := 0
	value := func() (string, error) {
		if i+1 >= len(args) {
			return "", fmt.Errorf("%w: %s needs a value", errJobUsage, args[i])
		}
		i++
		return args[i], nil
	}
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if len(args[i+1:]) > 0 {
				j.Command = guard.JobCommandLine(args[i+1:])
			}
			break
		}
		v, err := value()
		if err != nil {
			return nil, err
		}
		switch a {
		case "--name":
			j.Name = v
		case "--at":
			if j.At, err = jobs.ParseAt(v, now); err != nil {
				return nil, err //nolint:wrapcheck // says what is wrong
			}
		case "--until":
			j.Until = append(j.Until, jobs.Check{Command: v})
		case "--while":
			j.While = append(j.While, jobs.Check{Command: v})
		case "--hold", "--every", "--expire":
			d, err := jobs.ParseDuration(v)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("%w: %s %q", errJobUsage, a, v)
			}
			switch a {
			case "--hold":
				j.Hold = d
			case "--every":
				j.Every = max(d, jobs.MinEvery)
			default:
				j.Expire, explicitExpire = now.Add(min(d, jobs.MaxExpire)), true
			}
		default:
			return nil, fmt.Errorf("%w: unknown flag %s", errJobUsage, a)
		}
	}
	if j.Command == "" {
		return nil, errJobNoCommand
	}
	if j.At.IsZero() && len(j.Until) == 0 && j.Hold == 0 {
		return nil, errJobNoWait
	}
	if j.Hold > 0 && len(j.Until) == 0 {
		return nil, fmt.Errorf("%w: --hold is how long the --until checks must keep passing", errJobUsage)
	}
	switch {
	case !j.At.Before(j.Expire) && explicitExpire:
		// An --expire the user gave is when to give up; it is never moved.
		return nil, fmt.Errorf("%w: --expire must end after --at starts", errJobUsage)
	case !j.At.Before(j.Expire):
		j.Expire = j.At.Add(jobs.DefaultExpire)
	}
	return j, nil
}

// jobCommands are the shell commands a job will run: its checks and its command.
func jobCommands(j *jobs.Job) []string {
	out := []string{j.Command}
	for _, c := range slices.Concat(j.Until, j.While) {
		out = append(out, c.Command)
	}
	return out
}

// scheduleRefusal is why the guard would refuse one of the job's commands if the agent ran it now. A scheduled command
// runs later with nobody watching, so it gets the same check up front.
// Scripts are read from the job's directory, where they will run.
func scheduleRefusal(j *jobs.Job) error {
	for _, c := range jobCommands(j) {
		if err := commandRefusal(j.Dir, c); err != nil {
			return err
		}
	}
	return nil
}

// commandRefusal is why the guard would refuse to run one command line in dir now, with the rules that hold outside
// review mode, the same as for a command the agent runs itself. The runner asks it right before each check and before
// the command, so a script edited while the job waited is read as it is when it runs.
func commandRefusal(dir, c string) error {
	{
		for _, f := range guard.CheckInDir(c, false, dir).Blocked {
			if slices.Contains([]string{guard.RuleLearning, guard.RuleOpaque, guard.RuleUserOnly}, f.Rule) {
				return fmt.Errorf("%w: `%s` %s", errJobGuard, c, f.Reason)
			}
		}
	}
	return nil
}

func jobAdd(ctx context.Context, st jobs.Store, args []string, w io.Writer, now time.Time) error {
	if os.Getenv("CODEFORGE_GUARD") == "review" {
		return errJobInReview
	}
	j, err := parseJob(args, now)
	if err != nil {
		return err
	}
	if j.Dir, err = os.Getwd(); err != nil {
		return fmt.Errorf("finding the working directory: %w", err)
	}
	if err := scheduleRefusal(j); err != nil {
		return err
	}
	// Scheduling writes anyway, so it is also where jobs left without a runner get one again; a refused or invalid
	// call above starts nothing, and listing and watching only read.
	reviveJobs(ctx, st, "")
	if s, ok := callingSession(ctx, j.Dir); ok {
		j.Session, j.Workspace = s.id, s.workspace
	}
	if err := st.Save(j); err != nil {
		return err //nolint:wrapcheck // says which job
	}
	if err := startRunner(ctx, st, j); err != nil {
		j.State, j.Reason, j.Finished = jobs.Failed, "the runner could not start: "+err.Error(), time.Now()
		_ = st.Save(j) //nolint:errcheck // the error below says it all
		return err
	}
	fmt.Fprintf(w, "codeforge job: scheduled %s\n%s\n", j.ID, j.Status(now))
	switch {
	case j.Session != "":
		fmt.Fprintf(w, "Run `codeforge job wait %s` as a background command now: it shows the countdown and ends with the result, "+
			"which wakes you. If this session closes first, the result is posted back into it when the job ends.\n", j.ID)
	default:
		fmt.Fprintf(w, "No codeforge session started this, so the result is not posted anywhere: `codeforge job wait %s` shows it.\n", j.ID)
	}
	return nil
}

// startRunner starts `codeforge job run ID` in a session of its own, so it outlives the terminal and the Empryo session
// that scheduled it. Its output goes to the job's log.
// startRunner is a variable so tests never start a copy of the test binary.
var startRunner = startRunnerProcess

// jobWait shows the job's countdown until it ends, then its result. Run in the job's own live session (as a background
// command), it takes the result for that session, so the runner does not post it a second time.
func jobWait(ctx context.Context, st jobs.Store, id string, w io.Writer) error {
	tty := false
	if f, ok := w.(*os.File); ok {
		tty = term.IsTerminal(int(f.Fd()))
	}
	last, lastAt := "", time.Time{}
	for {
		j, err := st.Load(id)
		if err != nil {
			return err //nolint:wrapcheck // says which job
		}
		now := time.Now()
		line := j.Status(now)
		switch {
		case tty:
			fmt.Fprintf(w, "\r\x1b[2K%s", line)
		case statusKey(j) != last || now.Sub(lastAt) >= time.Minute:
			// Off a terminal (an agent's background command), one line per change and one a minute, not a flood.
			fmt.Fprintln(w, line)
			last, lastAt = statusKey(j), now
		}
		if j.State.Final() {
			if tty {
				fmt.Fprintln(w)
			}
			if _, err := fmt.Fprint(w, "\n"+j.Result()); err != nil {
				return fmt.Errorf("showing the result: %w", err)
			}
			// In the job's own live session the agent reads this result, so the runner must not post it again. Marked only
			// once it was written, and never from a review session, which writes nothing.
			if s, ok := callingSession(ctx, j.Dir); ok && s.id == j.Session && sameDir(s.workspace, j.Workspace) &&
				os.Getenv("CODEFORGE_GUARD") != "review" {
				// Under the session's lock, so a runner never posts it in the same moment.
				if unlock, err := st.LockSession(j.Workspace, j.Session); err == nil {
					st.MarkDelivered(j.ID)
					unlock()
				}
			}
			if j.State != jobs.Done {
				return errJobFailed
			}
			return nil
		}
		if j.RunnerPID != 0 && !st.RunnerActive(j.ID) && !finishedSince(st, j.ID) {
			return fmt.Errorf("%w (%s); `codeforge job log %s` shows why", errJobRunnerGone, j.Status(now), j.ID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // Ctrl-C
		case <-time.After(time.Second):
		}
	}
}

// finishedSince reports a job that reached its end after it was last read: its runner exits then, and is not "gone".
func finishedSince(st jobs.Store, id string) bool {
	j, err := st.Load(id)
	return err == nil && j.State.Final()
}

// statusKey is the part of the status that changes on its own, not with the clock.
func statusKey(j *jobs.Job) string {
	var b strings.Builder
	b.WriteString(string(j.State))
	for _, c := range slices.Concat(j.Until, j.While) {
		fmt.Fprintf(&b, "|%v", c.Passing)
	}
	return b.String()
}

func jobList(st jobs.Store, w io.Writer, now time.Time) error {
	list, err := st.List()
	if err != nil {
		return err //nolint:wrapcheck // says what failed
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "codeforge job: no jobs.")
	}
	for _, j := range list {
		fmt.Fprintln(w, j.Status(now))
	}
	return nil
}

func jobDetail(j *jobs.Job, now time.Time) string {
	var b strings.Builder
	b.WriteString(j.Status(now) + "\n")
	fmt.Fprintf(&b, "command: %s\ndir: %s\n", j.Command, j.Dir)
	for _, c := range j.Until {
		fmt.Fprintf(&b, "until: %s (%s)\n", c.Command, checkState(c))
	}
	for _, c := range j.While {
		fmt.Fprintf(&b, "while: %s (%s)\n", c.Command, checkState(c))
	}
	if j.Session != "" {
		fmt.Fprintf(&b, "posts back to session %s in %s\n", j.Session, j.Workspace)
	}
	if j.State.Final() {
		b.WriteString(j.Result())
	}
	return strings.TrimRight(b.String(), "\n")
}

func checkState(c jobs.Check) string {
	switch {
	case c.At.IsZero():
		return "not run yet"
	case c.Passing:
		return "passing"
	}
	return fmt.Sprintf("failing, exit %d", c.Exit)
}

// reviveJobs starts a runner again for each job that needs one and has none, such as after a restart: a waiting job
// picks up where its file says it was (its hold starts over, since nobody watched the checks meanwhile), and a finished
// one whose result was never posted back gets it posted. A job whose command was running when its runner died is not
// run again, since running a command twice can do harm that waiting cannot: what is left of the command is stopped, and
// the job is marked failed. Each decision is made under the job's runner lock, so it never overwrites a runner's work.
// skip is a session whose results are not posted now (the one being resumed: its person is about to open it).
func reviveJobs(ctx context.Context, st jobs.Store, skip string) {
	list, err := st.List()
	if err != nil {
		return
	}
	for _, listed := range list {
		if listed.State.Final() && (!needsDelivery(st, listed) || listed.Session == skip) {
			continue
		}
		// Just scheduled: its own runner is still starting.
		if listed.State == jobs.Waiting && listed.RunnerPID == 0 && time.Since(listed.Created) < runnerStartGrace {
			continue
		}
		unlock, err := st.TryLockRunner(listed.ID)
		if err != nil {
			continue // a runner has it
		}
		j, err := st.Load(listed.ID)
		if err != nil || j.State.Final() && !needsDelivery(st, j) {
			unlock()
			continue
		}
		settleOrphan(ctx, st, j)
		unlock()
		if j.State.Final() && (!needsDelivery(st, j) || j.Session == skip) {
			continue
		}
		_ = startRunner(ctx, st, j) //nolint:errcheck // codeforge job wait reports a runner that is gone
	}
}

// settleOrphan ends a job whose command was running when its runner died: what is left of the command is stopped and
// the job is marked failed, never run again. The caller holds the job's runner lock. It reports whether it did.
func settleOrphan(ctx context.Context, st jobs.Store, j *jobs.Job) bool {
	if j.State != jobs.Running {
		return false
	}
	reason := "its runner stopped while the command ran (a restart?); it was not run again"
	if commandGroupAlive(ctx, j) {
		stopGroup(j.CommandPID, jobStopGrace)
		reason = "its runner stopped while the command ran; the command was stopped and not run again"
	}
	j.State, j.Reason, j.Finished, j.Exit = jobs.Failed, reason, time.Now(), -1
	_ = st.Save(j) //nolint:errcheck // shown as it is otherwise
	return true
}

// commandGroupAlive reports whether the job's command group is still running: the recorded pid still leads its own
// group and started at the exact time recorded when the command started, so a pid reused since (after a reboot, say) is
// never taken for it.
//
// The shell that led the group may be gone while a child it left still runs. A process group lives as long as any member
// does, and its id cannot be handed to a new process meanwhile, so on the same boot a group with the recorded id and no
// leader of another start time is the command's.
func commandGroupAlive(ctx context.Context, j *jobs.Job) bool {
	if j.CommandPID <= 0 || j.CommandStart == "" || j.CommandBoot == "" || bootID(ctx) != j.CommandBoot {
		return false
	}
	pid := strconv.Itoa(j.CommandPID)
	if out, err := exec.CommandContext(ctx, "ps", "-o", "pgid=", "-p", pid).Output(); err == nil {
		// The leader is there: it must be the shell that started, leading its group.
		return strings.TrimSpace(string(out)) == pid && processStart(j.CommandPID) == j.CommandStart
	}
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pgid=").Output()
	if err != nil {
		return false
	}
	return slices.Contains(strings.Fields(string(out)), pid)
}

// bootID names the current boot, so a process recorded before a restart is never taken for one running now.
func bootID(ctx context.Context) string {
	if data, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		return strings.TrimSpace(string(data))
	}
	out, err := exec.CommandContext(ctx, "sysctl", "-n", "kern.boottime").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// printJobNotice tells a person resuming a session about its jobs: those still waiting or running, and those that
// finished since it was last opened. Each finished job is reported once.
// A read-only (review) launch shows the notice without marking it seen, so it writes nothing.
func printJobNotice(w io.Writer, st jobs.Store, workspace, session string, now time.Time, readOnly bool) {
	list, err := st.List()
	if err != nil || session == "" {
		return
	}
	for _, j := range list {
		if j.Session == "" || j.Session != session || !sameDir(j.Workspace, workspace) {
			continue
		}
		if !j.State.Final() {
			fmt.Fprintf(w, "codeforge: job %s\n", j.Status(now))
			continue
		}
		if readOnly {
			if st.Seen(j.ID) {
				continue
			}
		} else if !st.MarkSeen(j.ID) {
			continue
		}
		how := "its result was posted into this conversation"
		if !st.Delivered(j.ID) {
			how = "its result is not in this conversation yet: `codeforge job show " + j.ID + "`"
		}
		fmt.Fprintf(w, "codeforge: while you were away, job %s; %s\n", j.Status(now), how)
	}
}

func jobCancel(ctx context.Context, st jobs.Store, id string, w io.Writer) error {
	j, err := st.Load(id)
	if err != nil {
		return err //nolint:wrapcheck // says which job
	}
	if j.State.Final() {
		fmt.Fprintf(w, "codeforge job: %s is already %s.\n", id, j.State)
		return nil
	}
	// The request is a file first: the runner reads it before every round, right before the command starts and every
	// second while it waits or runs, so no pid (which may be stale) is ever signalled.
	if err := st.RequestCancel(id); err != nil {
		return err //nolint:wrapcheck // says which job
	}
	unlock, err := st.TryLockRunner(id)
	if err != nil {
		// The runner stops the command, records the cancel and posts it back.
		fmt.Fprintf(w, "codeforge job: cancelling %s.\n", id)
		return nil //nolint:nilerr // a busy lock means a runner is on it
	}
	// No runner: decided here, under the lock, from the job as it is now.
	if j, err = st.Load(id); err != nil {
		unlock()
		return err //nolint:wrapcheck // says which job
	}
	if j.State.Final() {
		unlock()
		fmt.Fprintf(w, "codeforge job: %s is already %s.\n", id, j.State)
		return nil
	}
	if j.State == jobs.Running && commandGroupAlive(ctx, j) {
		stopGroup(j.CommandPID, jobStopGrace)
	}
	j.State, j.Reason, j.Finished = jobs.Cancelled, "cancelled by request", time.Now()
	err = st.Save(j)
	unlock()
	if err != nil {
		return err //nolint:wrapcheck // says which job
	}
	// With no runner left, one is started just to post the cancel back into its session.
	if needsDelivery(st, j) {
		_ = startRunner(ctx, st, j) //nolint:errcheck // the resume notice still says it
	}
	fmt.Fprintf(w, "codeforge job: cancelled %s.\n", id)
	return nil
}
