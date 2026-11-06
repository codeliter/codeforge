package review

import (
	"context"
	"time"
)

// The review clock. HardLimit is the longest `codeforge review` may take in all, from the moment its clock starts, for any
// reason; setup before that moment is not counted. AgentLimit is the longest any one reviewer or juror runs, from its
// own start, and is always cut short by HardLimit. These are variables only so tests can shrink them.
var (
	HardLimit  = 270 * time.Second
	AgentLimit = 180 * time.Second
	// Reserve is kept after the agents' deadline for killing stragglers, writing the draft and opening the session.
	Reserve = 10 * time.Second
	// LoadLimit caps all the gh and git calls that collect the change together.
	LoadLimit = 20 * time.Second
	// KillGrace is how long before a hard kill an agent is told to stop on its own, so a slow one still saves its
	// session and returns the text it has instead of being killed mid-sentence.
	KillGrace = 5 * time.Second
	// MinRun is the least time worth launching a reviewer for: Empryo's start-up alone takes several seconds.
	MinRun = 20 * time.Second
)

type deadlineKey struct{}

// WithDeadline records when the review must be finished. It is a value next to the context's own deadline, so code
// that runs after the draft (the interactive session) can drop the cancellation and still read the clock.
func WithDeadline(ctx context.Context, d time.Time) context.Context {
	return context.WithValue(ctx, deadlineKey{}, d)
}

// HasDeadline reports whether a review clock was started on ctx.
func HasDeadline(ctx context.Context) bool {
	_, ok := ctx.Value(deadlineKey{}).(time.Time)
	return ok
}

// Deadline is the end of the review's HardLimit. Without a recorded clock it is HardLimit from now.
func Deadline(ctx context.Context) time.Time {
	if d, ok := ctx.Value(deadlineKey{}).(time.Time); ok {
		return d
	}
	return time.Now().Add(HardLimit)
}

// WorkDeadline is when every agent is over: Reserve before the end of the review.
func WorkDeadline(ctx context.Context) time.Time {
	return Deadline(ctx).Add(-Reserve)
}

// Elapsed is how long the review has been running.
func Elapsed(ctx context.Context) time.Duration {
	return HardLimit - time.Until(Deadline(ctx))
}

// AgentEnd is when an agent that starts now must be over: AgentLimit from now, never past WorkDeadline.
func AgentEnd(ctx context.Context) time.Time {
	return minTime(time.Now().Add(AgentLimit), WorkDeadline(ctx))
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
