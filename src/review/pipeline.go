package review

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type pipelineKey struct{}

// Pipeline runs the second-opinion jurors alongside the main reviewer. The reviewer starts at once and never waits for
// them; what they find is shown after its review as leads, because a juror's word alone is not a verdict. Measured on
// a 146 KB diff with six injected bugs, this found more (0.64 of the bugs against 0.58) than running the jurors first,
// in 1m22s instead of 2m35s, and never lost a verdict to the wait.
type Pipeline struct {
	mu   sync.Mutex
	done chan struct{}
	res  JuryResult
	err  error
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
	p.mu.Lock()
	p.done = done
	p.mu.Unlock()
	go func() {
		defer cleanup()
		defer close(done)
		jctx, cancel := context.WithDeadline(ctx, AgentEnd(ctx))
		defer cancel()
		res, err := Jury(jctx, repo, brief, reviewers, JuryOptions{})
		p.mu.Lock()
		p.res, p.err = res, err
		p.mu.Unlock()
	}()
}

// Wait waits for the jurors until they finish or the review's deadline passes. It reports whether they finished; a
// false result carries nothing, because a half-run jury is not a result.
func (p *Pipeline) Wait(ctx context.Context) (JuryResult, bool) {
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done == nil {
		return JuryResult{}, false
	}
	if HasDeadline(ctx) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, Deadline(ctx))
		defer cancel()
	}
	select {
	case <-done:
	case <-ctx.Done():
		return JuryResult{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.res, true
}

// Leads renders what the jurors found, or says they did not finish. It is empty when no jurors were started.
func (p *Pipeline) Leads(ctx context.Context) string {
	p.mu.Lock()
	started := p.done != nil
	p.mu.Unlock()
	if !started {
		return ""
	}
	res, ok := p.Wait(ctx)
	if !ok {
		return "Jurors: did not finish before the review's time limit; no second opinion.\n"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return RenderLeads(res, p.err)
}

// RenderLeads lists what the jurors reported. Every blocking item is a lead for the author, not a confirmed defect.
func RenderLeads(res JuryResult, err error) string {
	if len(res.Votes) == 0 {
		return ""
	}
	var s strings.Builder
	blocking := 0
	for _, v := range res.Votes {
		if v.Err != nil || v.Verdict.Verdict == "" {
			fmt.Fprintf(&s, "- %s: no verdict in time\n", v.Reviewer)
			continue
		}
		fmt.Fprintf(&s, "- %s: %s. %s\n", v.Reviewer, v.Verdict.Verdict, v.Verdict.Summary)
		for _, f := range v.Verdict.Blocking {
			fmt.Fprintf(&s, "  blocking %s:%d %s\n", f.File, f.Line, f.What)
			blocking++
		}
		for _, f := range v.Verdict.Should {
			fmt.Fprintf(&s, "  should %s:%d %s\n", f.File, f.Line, f.What)
		}
	}
	head := "Jurors reviewed the change in parallel with the reviewer above and did not see its review. Their findings are unverified leads: check each before acting.\n"
	if blocking > 0 {
		head += "A juror reported blocking leads, so the change is not approved until each is checked.\n"
	} else if err != nil {
		head += "The jurors did not reach a quorum.\n"
	}
	return head + s.String()
}
