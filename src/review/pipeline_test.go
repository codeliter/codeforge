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

func TestPipeline_StartReturnsAtOnce(t *testing.T) {
	var p Pipeline
	start := time.Now()
	p.Start(t.Context(), "", "brief", []Reviewer{fixedReviewer{name: "slow", wait: time.Minute}}, func() {})
	require.Less(t, time.Since(start), 100*time.Millisecond, "the reviewer must never wait for the jurors")
}

func TestPipeline_JurorsOverlapAndLeadsAreUnverified(t *testing.T) {
	var p Pipeline
	cleaned := make(chan struct{})
	p.Start(t.Context(), "", "brief", []Reviewer{
		fixedReviewer{name: "a", out: failVerdict, wait: 150 * time.Millisecond},
		fixedReviewer{name: "b", out: failVerdict, wait: 150 * time.Millisecond},
	}, func() { close(cleaned) })
	start := time.Now()
	got := p.Leads(t.Context())
	require.Less(t, time.Since(start), 280*time.Millisecond, "jurors overlap")
	require.Contains(t, got, "blocking a.go:3 drops data")
	require.Contains(t, got, "unverified leads")
	require.Contains(t, got, "not approved until each is checked")
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not run")
	}
}

func TestPipeline_SlowAndBrokenJurorsAreNamed(t *testing.T) {
	var p Pipeline
	p.Start(t.Context(), "", "brief", []Reviewer{
		fixedReviewer{name: "fast", out: failVerdict},
		fixedReviewer{name: "broken", err: errBoom},
	}, func() {})
	got := p.Leads(t.Context())
	require.Contains(t, got, "broken: no verdict in time")
	require.Contains(t, got, "fast: fail")
}

func TestPipeline_LateJurorsAreReportedNotAwaited(t *testing.T) {
	var p Pipeline
	p.Start(t.Context(), "", "brief", []Reviewer{fixedReviewer{name: "slow", wait: time.Minute}}, func() {})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	got := p.Leads(ctx)
	require.Contains(t, got, "did not finish")
}

func TestPipeline_NothingStartedNothingSaid(t *testing.T) {
	var p Pipeline
	require.Empty(t, p.Leads(t.Context()))
	require.Nil(t, PipelineOf(t.Context()))
	require.Same(t, &p, PipelineOf(WithPipeline(t.Context(), &p)))
}

func TestPipeline_NilCleanupIsAllowed(t *testing.T) {
	var p Pipeline
	p.Start(t.Context(), "", "brief", []Reviewer{fixedReviewer{name: "a", out: failVerdict}}, nil)
	require.Contains(t, p.Leads(t.Context()), "a: fail")
}

func TestPipeline_WaitsOnlyTheGraceAfterTheReview(t *testing.T) {
	old := JurorGrace
	JurorGrace = 100 * time.Millisecond
	t.Cleanup(func() { JurorGrace = old })
	var p Pipeline
	p.Start(t.Context(), "", "brief", []Reviewer{fixedReviewer{name: "slow", wait: time.Minute}}, nil)
	start := time.Now()
	got := p.Leads(t.Context())
	require.Less(t, time.Since(start), time.Second)
	require.Contains(t, got, "did not finish")
}

func TestPipeline_GivingUpStopsTheJurorsAndRunsCleanup(t *testing.T) {
	var p Pipeline
	cleaned := make(chan struct{})
	p.Start(t.Context(), "", "brief", []Reviewer{fixedReviewer{name: "slow", wait: time.Minute}}, func() { close(cleaned) })
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, ok := p.Wait(ctx)
	require.False(t, ok)
	select {
	case <-cleaned:
	case <-time.After(2 * time.Second):
		t.Fatal("the jurors were left running after Wait gave up")
	}
	_, again := p.Wait(t.Context())
	require.False(t, again, "a stopped jury is not a result")
}

func TestPipeline_StopWithoutStartIsHarmless(t *testing.T) {
	var p Pipeline
	p.Stop()
	started := Pipeline{}
	started.Start(t.Context(), "", "brief", []Reviewer{fixedReviewer{name: "slow", wait: time.Minute}}, nil)
	started.Stop()
	started.Stop()
}
