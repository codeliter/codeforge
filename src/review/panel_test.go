//nolint:err113 // dynamic error messages are okay in tests
package review

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

type fixedReviewer struct {
	name, out string
	err       error
	wait      time.Duration
}

func (f fixedReviewer) Name() string { return f.name }

func (f fixedReviewer) Review(ctx context.Context, _ string) (string, error) {
	select {
	case <-time.After(f.wait):
		return f.out, f.err
	case <-ctx.Done():
		return "", fmt.Errorf("juror stopped: %w", ctx.Err())
	}
}

const failVerdict = "```json\n{\"verdict\":\"fail\",\"summary\":\"bad\",\"blocking\":[{\"file\":\"a.go\",\"line\":3,\"what\":\"drops data\"}],\"should\":[]}\n```"

func TestPanel_RunsJurorsInParallelAndRendersFindings(t *testing.T) {
	p := Panel{Reviewers: []Reviewer{
		fixedReviewer{name: "a", out: failVerdict, wait: 150 * time.Millisecond},
		fixedReviewer{name: "b", out: failVerdict, wait: 150 * time.Millisecond},
	}}
	start := time.Now()
	got := p.Run(t.Context(), "brief", 5*time.Second)
	require.Less(t, time.Since(start), 280*time.Millisecond, "jurors overlap")
	require.Contains(t, got, "blocking a.go:3 drops data")
	require.Contains(t, got, "do not dispatch jurors yourself")
	require.Contains(t, got, "stays a blocking finding")
	require.Contains(t, got, "the change is not approved")
}

func TestPanel_SlowJurorIsNamedNotAwaited(t *testing.T) {
	p := Panel{Reviewers: []Reviewer{
		fixedReviewer{name: "fast", out: failVerdict},
		fixedReviewer{name: "slow", wait: time.Minute},
		fixedReviewer{name: "broken", err: errBoom},
	}}
	start := time.Now()
	got := p.Run(t.Context(), "brief", 200*time.Millisecond)
	require.Less(t, time.Since(start), 2*time.Second)
	require.Contains(t, got, "slow: no verdict in time")
	require.Contains(t, got, "broken: no verdict in time")
	require.Contains(t, got, "fast: fail")
}

func TestPanel_NoReviewersNoSection(t *testing.T) {
	require.Empty(t, Panel{}.Run(t.Context(), "brief", time.Second))
}
