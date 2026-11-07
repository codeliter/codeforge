package review

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type pipelineKey struct{}

// Pipeline runs the second-opinion jurors alongside the main reviewer. The reviewer starts at once and never waits for
// them; what they find is shown after its review as leads, because a juror's word alone is not a verdict. Measured on
// a 146 KB diff with six injected bugs, this found about as much as running the jurors first (0.64 of the bugs against
// 0.58, within noise) in 1m22s instead of 2m35s, and never lost a verdict to the wait.
type Pipeline struct {
	mu      sync.Mutex
	done    chan struct{}
	cancel  context.CancelFunc
	stopped bool
	taken   bool
	res     JuryResult
	err     error
}

// WithPipeline attaches p to ctx.
func WithPipeline(ctx context.Context, p *Pipeline) context.Context {
	return context.WithValue(ctx, pipelineKey{}, p)
}

// PipelineOf returns the review's pipeline, or nil outside a timed review.
func PipelineOf(ctx context.Context) *Pipeline {
	if p, ok := ctx.Value(pipelineKey{}).(*Pipeline); ok {
		return p
	}
	return nil
}

// Start runs the jurors in the background, each for at most AgentLimit and never past the review's work deadline.
// cleanup runs when they are done. ctx must outlive the call.
func (p *Pipeline) Start(ctx context.Context, repo, brief string, reviewers []Reviewer, cleanup func()) {
	if cleanup == nil {
		cleanup = func() {}
	}
	done := make(chan struct{})
	jctx, cancel := context.WithDeadline(ctx, AgentEnd(ctx))
	p.mu.Lock()
	p.done, p.cancel = done, cancel
	p.mu.Unlock()
	go func() {
		// Reverse order: the jury is cancelled, its processes and scratch directory are cleaned up, and only then is done
		// closed, so whoever sees done also knows the cleanup has run.
		defer close(done)
		defer cleanup()
		defer cancel()
		res, err := Jury(jctx, repo, brief, reviewers, JuryOptions{})
		p.mu.Lock()
		p.res, p.err = res, err
		p.mu.Unlock()
	}()
}

// Wait waits for the jurors until they finish, JurorGrace passes or the review's deadline passes. It reports whether
// they finished; a false result carries nothing, because a half-run jury is not a result, and it stops the jurors and
// waits briefly for their processes to go. Each call starts its own JurorGrace window.
func (p *Pipeline) Wait(ctx context.Context) (JuryResult, bool) {
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done == nil {
		return JuryResult{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, JurorGrace)
	defer cancel()
	if HasDeadline(ctx) {
		var dcancel context.CancelFunc
		ctx, dcancel = context.WithDeadline(ctx, Deadline(ctx))
		defer dcancel()
	}
	select {
	case <-done:
	case <-ctx.Done():
		p.Stop()
		return JuryResult{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return JuryResult{}, false
	}
	return p.res, true
}

// Stop cancels the jurors and gives their cleanup a moment to remove their processes and scratch directory, so none
// outlives a command that returns right after. A stopped jury is never a result. It is safe to call at any time.
func (p *Pipeline) Stop() {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	if cancel == nil {
		p.mu.Unlock()
		return
	}
	select {
	case <-done:
	default:
		p.stopped = true
	}
	p.mu.Unlock()
	cancel()
	timer := time.NewTimer(stopWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// stopWait bounds how long stop waits for the jurors to exit after cancelling them.
const stopWait = 3 * time.Second

// Leads renders the blocking leads the jurors found. It is empty when no jurors were started, when they did not finish
// in the time left, or when none of them reported a blocker: the reader gets findings, not the jury's bookkeeping.
func (p *Pipeline) Leads(ctx context.Context) string {
	p.mu.Lock()
	started := p.done != nil
	p.mu.Unlock()
	if !started {
		return ""
	}
	// The leads are shown once: whoever asks first (a posted comment or the terminal) has them.
	p.mu.Lock()
	taken := p.taken
	p.taken = true
	p.mu.Unlock()
	if taken {
		return ""
	}
	res, ok := p.Wait(ctx)
	if !ok {
		return ""
	}
	return RenderLeads(res)
}

// oneLine puts a juror's text on one line, so every finding is one line that starts with its label and a finding that
// spans lines cannot be mistaken for, or cut apart from, the ones around it.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// LeadsHeader opens the list of unconfirmed blocking leads.
const LeadsHeader = "**Unconfirmed blocking leads.** A second model flagged these and the review above did not confirm them. Check each before merging:\n"

// RenderLeads lists the blocking items the jurors reported, once each, as leads for the author, not confirmed defects.
// Jurors without a verdict, non-blocking items and who said what are left out.
func RenderLeads(res JuryResult) string {
	var s strings.Builder
	seen := map[string]bool{}
	for _, v := range res.Votes {
		if v.Err != nil {
			continue
		}
		for _, f := range v.Verdict.Blocking {
			line := fmt.Sprintf("- `%s:%d` %s\n", oneLine(f.File), f.Line, oneLine(f.What))
			if !seen[line] {
				seen[line] = true
				s.WriteString(line)
			}
		}
	}
	if s.Len() == 0 {
		return ""
	}
	return LeadsHeader + s.String()
}
