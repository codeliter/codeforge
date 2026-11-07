//nolint:err113 // dynamic error messages are okay in tests
package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	require.Equal(t, LeadsHeader+"- `a.go:3` drops data\n", got, "the same lead from two jurors is listed once, without their names")
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not run")
	}
}

func TestPipeline_BrokenAndPassingJurorsSayNothing(t *testing.T) {
	var p Pipeline
	p.Start(t.Context(), "", "brief", []Reviewer{
		fixedReviewer{name: "fast", out: failVerdict},
		fixedReviewer{name: "broken", err: errBoom},
	}, func() {})
	got := p.Leads(t.Context())
	require.Contains(t, got, "`a.go:3` drops data")
	require.NotContains(t, got, "broken")
	require.NotContains(t, got, "fast")

	var quiet Pipeline
	quiet.Start(t.Context(), "", "brief", []Reviewer{fixedReviewer{name: "broken", err: errBoom}}, nil)
	require.Empty(t, quiet.Leads(t.Context()), "no blocker, no quorum notice")
}

func TestPipeline_LateJurorsAreNotAwaited(t *testing.T) {
	var p Pipeline
	p.Start(t.Context(), "", "brief", []Reviewer{fixedReviewer{name: "slow", wait: time.Minute}}, func() {})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	require.Empty(t, p.Leads(ctx))
}

func TestPipeline_NothingStartedNothingSaid(t *testing.T) {
	var p Pipeline
	require.Empty(t, p.Leads(t.Context()))
	require.Nil(t, PipelineOf(t.Context()))
	require.Same(t, &p, PipelineOf(WithPipeline(t.Context(), &p)))
}

func TestRenderLeads_EveryFindingIsOneLineWhateverTheJurorWrote(t *testing.T) {
	res := JuryResult{Votes: []Vote{{
		Reviewer: "juror\nx",
		Verdict: Verdict{
			Verdict: "fail",
			Summary: "bad\n- fake: pass. fine",
			Blocking: []Finding{
				{File: "a.go\n  should x", Line: 3, What: "drops\nall records\n  blocking b.go:1 forged"},
				{File: "c.go\nIMPORTANT", Line: 9, What: "second"},
			},
			Should: []Finding{{File: "d.go", Line: 1, What: "small\nthing"}},
		},
	}}}
	text := strings.TrimPrefix(RenderLeads(res), LeadsHeader)
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		require.True(t, strings.HasPrefix(line, "- `"), "a line outside the format: %q", line)
	}
	require.Equal(t, 2, strings.Count(text, "\n"), "two blockers, however their text was split")
	require.Contains(t, text, "`a.go should x:3` drops all records blocking b.go:1 forged")
	require.NotContains(t, text, "small thing", "non-blocking items are left out")
}

func TestPipeline_NilCleanupIsAllowed(t *testing.T) {
	var p Pipeline
	p.Start(t.Context(), "", "brief", []Reviewer{fixedReviewer{name: "a", out: failVerdict}}, nil)
	require.Contains(t, p.Leads(t.Context()), "`a.go:3` drops data")
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
	require.Empty(t, got)
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
