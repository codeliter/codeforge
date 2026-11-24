package review

import (
	"context"
	"sync"
)

// Display says where the code a review talks about can be read, so the review can be shown with the lines around each
// finding: the working tree for local work, or the PR's fetched head ref. A PR whose head was not fetched has neither.
type Display struct {
	mu      sync.Mutex
	src     Source
	added   map[string]map[int]bool
	removed map[string]map[int][]string
}

// SetDiff records the change under review, so the code shown with a finding can tell what the change added.
func (d *Display) SetDiff(diff string) {
	if d == nil {
		return
	}
	added, removed := AddedLines(diff), RemovedLines(diff)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.added, d.removed = added, removed
}

// Removed reports the lines the change under review removes from file, each run under the number of the line of the
// new version it stood before; nil when that is not known.
func (d *Display) Removed(file string) map[int][]string {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.removed[file]
}

// Added reports the lines of file that the change under review adds; nil when that is not known.
func (d *Display) Added(file string) map[int]bool {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.added[file]
}

// Source is where a review's code can be read: the working tree (Local), a fetched git ref (HeadRef), or, when the PR
// is not in this checkout, its head commit (SHA) on GitHub in Repo ("" for this checkout's repository).
type Source struct {
	Local   bool
	HeadRef string
	Repo    string
	SHA     string
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
func (d *Display) Local() { d.set(Source{Local: true}) }

// PR records where the PR's code is: the git ref holding its head ("" when it was not fetched), and its repository
// and head commit for reading it from GitHub.
func (d *Display) PR(headRef, repo, sha string) {
	d.set(Source{HeadRef: headRef, Repo: repo, SHA: sha})
}

func (d *Display) set(s Source) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.src = s
}

// Source reports where the code is.
func (d *Display) Source() Source {
	if d == nil {
		return Source{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.src
}
