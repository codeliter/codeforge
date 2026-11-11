package jobs

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Deps is what the runner needs from the world, so it can be driven by a test clock.
type Deps struct {
	Now func() time.Time
	// Sleep waits d, or until ctx is done.
	Sleep func(ctx context.Context, d time.Duration) error
	// Check runs one check command in dir and returns its exit status and output.
	Check func(ctx context.Context, dir, command string) (exit int, output string)
	// Start runs the job's command and returns a channel that gets its exit status, and a function that stops it.
	Start func(ctx context.Context, j *Job) (done <-chan Exit, stop func(), err error)
	Save  func(*Job) error
	Log   io.Writer
	// Cancelled reports a cancel asked for outside ctx (codeforge job cancel's file); it is read before every check
	// round and right before the command starts. Nil is never.
	Cancelled func() bool
	// Allowed says why one command line may not run now (a script it names changed into one the guard refuses); it is
	// asked right before each check and right before the command starts. Nil allows everything.
	Allowed func(dir, command string) error
}

func (d Deps) refused(j *Job, command string) error {
	if d.Allowed == nil {
		return nil
	}
	return d.Allowed(j.Dir, command)
}

func (d Deps) cancelled(ctx context.Context) bool {
	return ctx.Err() != nil || d.Cancelled != nil && d.Cancelled()
}

// Exit is how the command ended and the tail of what it printed.
type Exit struct {
	Code   int
	Output string
}

// Run takes a waiting job to a final state: it waits for the start time and the Until checks (held for Hold), runs the
// command, and cancels it the moment a While check fails, ctx is cancelled (codeforge job cancel) or the job expires
// before it starts. Every change is saved, so codeforge job wait can show it as it happens.
func Run(ctx context.Context, j *Job, d Deps) error {
	every := max(j.Every, MinEvery)
	// A hold counts only what this runner watched: after a restart nobody saw the checks meanwhile, so it starts over.
	j.ReadySince = time.Time{}
	for j.State == Waiting {
		if d.cancelled(ctx) {
			return finish(j, d, Cancelled, "cancelled before it started", Exit{})
		}
		if !d.Now().Before(j.Expire) {
			return finish(j, d, Expired, "its preconditions did not hold before it gave up", Exit{})
		}
		// A failing While check ends the job at once; the Until checks are not run after it.
		c, whileFails, refusal := runChecks(ctx, j, j.While, d)
		untilFails := false
		if !whileFails && refusal == nil {
			_, untilFails, refusal = runChecks(ctx, j, j.Until, d)
		}
		ready := false
		now := d.Now()
		switch {
		case refusal != nil:
			return finish(j, d, Cancelled, refusal.Error(), Exit{})
		case d.cancelled(ctx):
			// A check killed by the cancel says nothing about the condition.
			return finish(j, d, Cancelled, "cancelled before it started", Exit{})
		case whileFails:
			return finish(j, d, Cancelled, fmt.Sprintf("the check `%s` failed (exit %d)", c.Command, c.Exit), Exit{})
		case !now.Before(j.Expire):
			// A check that passed only after the deadline is too late.
			return finish(j, d, Expired, "its preconditions did not hold before it gave up", Exit{})
		case untilFails:
			j.ReadySince = time.Time{}
		case j.ReadySince.IsZero():
			j.ReadySince = now
		}
		ready = !untilFails && !now.Before(j.At) && now.Sub(j.ReadySince) >= j.Hold
		if ready && len(j.Until) > 0 && len(j.While) > 0 {
			// The Until checks took time: every While check must still hold the moment the command starts.
			c, whileFails, refusal = runChecks(ctx, j, j.While, d)
			switch {
			case refusal != nil:
				return finish(j, d, Cancelled, refusal.Error(), Exit{})
			case d.cancelled(ctx):
				return finish(j, d, Cancelled, "cancelled before it started", Exit{})
			case whileFails:
				return finish(j, d, Cancelled, fmt.Sprintf("the check `%s` failed (exit %d)", c.Command, c.Exit), Exit{})
			}
		}
		if ready {
			break
		}
		// Woken at the next check, the start time or the end of the hold, whichever comes first, but a check round never
		// follows another sooner than MinEvery unless it is due at the start time.
		wait := every
		if now.Before(j.At) {
			wait = min(wait, j.At.Sub(now))
		}
		if left := j.Hold - now.Sub(j.ReadySince); !j.ReadySince.IsZero() && left > 0 {
			wait = min(wait, max(left, MinEvery))
		}
		wait = min(max(wait, time.Second), j.Expire.Sub(now)+time.Second)
		j.NextCheck = now.Add(wait)
		if err := d.Save(j); err != nil {
			return err
		}
		if err := d.Sleep(ctx, wait); err != nil {
			return finish(j, d, Cancelled, "cancelled before it started", Exit{})
		}
	}
	return execute(ctx, j, d, every)
}

func execute(ctx context.Context, j *Job, d Deps, every time.Duration) error {
	// The last look before anything runs.
	if d.cancelled(ctx) {
		return finish(j, d, Cancelled, "cancelled before it started", Exit{})
	}
	if err := d.refused(j, j.Command); err != nil {
		return finish(j, d, Cancelled, err.Error(), Exit{})
	}
	// Checks take time: a job whose preconditions finished passing after its deadline is too late.
	if !d.Now().Before(j.Expire) {
		return finish(j, d, Expired, "its preconditions did not hold before it gave up", Exit{})
	}
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	j.State, j.Started, j.NextCheck = Running, d.Now(), time.Time{}
	if err := d.Save(j); err != nil {
		return err
	}
	fmt.Fprintf(d.Log, "%s starting: %s\n", d.Now().Format(time.RFC3339), j.Command)
	done, stop, err := d.Start(runCtx, j)
	if err != nil {
		if d.cancelled(ctx) {
			// Start looks once more right before it releases the command, and held it back.
			return finish(j, d, Cancelled, "cancelled before it started", Exit{})
		}
		return finish(j, d, Failed, "could not start: "+err.Error(), Exit{Code: -1})
	}
	// Saved with the command's pid, so a runner that dies does not leave it unseen.
	if err := d.Save(j); err != nil {
		stop()
		<-done
		return err
	}
	for {
		tick := make(chan error, 1)
		sleepCtx, cancelSleep := context.WithCancel(runCtx)
		go func() { tick <- d.Sleep(sleepCtx, every) }()
		select {
		case ex := <-done:
			cancelSleep()
			if ex.Code == 0 {
				return finish(j, d, Done, "", ex)
			}
			return finish(j, d, Failed, "", ex)
		case err := <-tick:
			cancelSleep()
			if err != nil || d.cancelled(ctx) {
				stop()
				return finish(j, d, Cancelled, "cancelled while running", <-done)
			}
			c, failed, refusal := runChecks(ctx, j, j.While, d)
			if refusal != nil {
				stop()
				return finish(j, d, Cancelled, "stopped: "+refusal.Error(), <-done)
			}
			if failed && ctx.Err() == nil {
				// A command that ended while the check ran ended as it did; the check came too late to stop it.
				select {
				case ex := <-done:
					if ex.Code == 0 {
						return finish(j, d, Done, "", ex)
					}
					return finish(j, d, Failed, "", ex)
				default:
				}
				stop()
				return finish(j, d, Cancelled, fmt.Sprintf("stopped: the check `%s` failed (exit %d) while it ran", c.Command, c.Exit), <-done)
			}
			if err := d.Save(j); err != nil {
				stop()
				<-done
				return err
			}
		}
	}
}

// runChecks runs the checks in order and records each answer, stopping at the first that fails or at a cancel; it
// returns the one that failed.
// A check the guard now refuses is not run; its refusal is returned.
func runChecks(ctx context.Context, j *Job, cs []Check, d Deps) (Check, bool, error) {
	for i := range cs {
		if d.cancelled(ctx) {
			return Check{}, false, nil
		}
		if err := d.refused(j, cs[i].Command); err != nil {
			return cs[i], false, err
		}
		code, out := d.Check(ctx, j.Dir, cs[i].Command)
		cs[i].Exit, cs[i].Passing, cs[i].Output, cs[i].At = code, code == 0, tail(out, checkTail), d.Now()
		if code != 0 {
			return cs[i], true, nil
		}
	}
	return Check{}, false, nil
}

func finish(j *Job, d Deps, s State, reason string, ex Exit) error {
	j.State, j.Reason, j.Finished, j.NextCheck = s, reason, d.Now(), time.Time{}
	j.Exit, j.Output = ex.Code, tail(ex.Output, outputTail)
	fmt.Fprintf(d.Log, "%s %s\n", d.Now().Format(time.RFC3339), j.Status(d.Now()))
	return d.Save(j)
}
