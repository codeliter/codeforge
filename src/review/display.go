package review

import (
	"context"
	"sync"
)

// Display says where the code a review talks about can be read, so the review can be shown with the lines around each
// finding: the working tree for local work, or the PR's fetched head ref. A PR whose head was not fetched has neither.
type Display struct {
	mu      sync.Mutex
	headRef string
	local   bool
}

type displayKey struct{}

// WithDisplay attaches d to ctx.
func WithDisplay(ctx context.Context, d *Display) context.Context {
	return context.WithValue(ctx, displayKey{}, d)
}

// DisplayOf returns the review's display, or nil outside a review.
func DisplayOf(ctx context.Context) *Display {
	if d, ok := ctx.Value(displayKey{}).(*Display); ok {
		return d
	}
	return nil
}

// Local records that the review is of the working tree.
func (d *Display) Local() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.local, d.headRef = true, ""
}

// PR records the git ref holding the PR's head; "" when it was not fetched.
func (d *Display) PR(headRef string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.local, d.headRef = false, headRef
}

// Source reports where the code is: the PR's head ref, or local for the working tree.
func (d *Display) Source() (headRef string, local bool) {
	if d == nil {
		return "", false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.headRef, d.local
}
