package review

import (
	"context"
	"time"
)

// The review clock. HardLimit is the longest `codeforge review` may take from the moment it starts, for any reason.
// These are variables only so tests can shrink them.
var (
	HardLimit = 120 * time.Second
	// Reserve is kept after the reviewers' deadline for killing stragglers, writing the draft and opening the session.
	Reserve = 15 * time.Second
	// LoadLimit caps the gh and git calls that collect the change.
	LoadLimit = 15 * time.Second
	// KillGrace is how long before the hard kill a reviewer is told to stop on its own, so a slow one still returns the
	// text it has instead of being killed mid-sentence.
	KillGrace = 8 * time.Second
	// PanelWindow is how long the parallel jurors get, counted from when they start.
	PanelWindow = 35 * time.Second
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

// PanelDeadline is when every reviewer is killed: Reserve before the end of the review.
func PanelDeadline(ctx context.Context) time.Time {
	return Deadline(ctx).Add(-Reserve)
}

// Elapsed is how long the review has been running.
func Elapsed(ctx context.Context) time.Duration {
	return HardLimit - time.Until(Deadline(ctx))
}
