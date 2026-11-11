package src

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/review"
)

func TestDropAccepted_TheAcceptedFindingLeavesTheReview(t *testing.T) {
	text := "- issue (blocking): a.go:3 — AlwaysSample sends every span\n\n" +
		"- issue (blocking): c.go:57 — a retry re-sends the read body\n\n" +
		"Decision: Request changes"
	got, n := dropAccepted(text, []review.Accepted{{File: "a.go", Line: 5}})
	require.Equal(t, 1, n)
	require.Equal(t, "- issue (blocking): c.go:57 — a retry re-sends the read body\n\nDecision: Request changes", got)
	d, _ := review.ParseDecision(got)
	require.Equal(t, review.DecisionRequestChanges, d, "the other blocker still blocks")

	same, n := dropAccepted(text, nil)
	require.Zero(t, n)
	require.Equal(t, text, same)
}

func TestDropAcceptedLeads(t *testing.T) {
	leads := review.LeadsHeader + "- `a.go:4` drops data\n- `b.go:9` races\n"
	list := []review.Accepted{{File: "a.go", Line: 3}}
	require.Equal(t, review.LeadsHeader+"- `b.go:9` races\n", dropAcceptedLeads(leads, list))
	require.Empty(t, dropAcceptedLeads(review.LeadsHeader+"- `a.go:4` drops data\n", list))
}

func TestReviewAcceptCommand_AcceptListAndUnaccept(t *testing.T) {
	rec := review.Record{Findings: []review.RecordFinding{{Path: "a.go", Line: 3, Label: "issue (blocking)", Blocking: true, Text: "nil deref"}}}
	load := func() (review.Record, error) { return rec, nil }
	update := func(change func(*review.Record) error) (review.Record, error) {
		next := rec
		next.Findings = append([]review.RecordFinding(nil), rec.Findings...)
		if err := change(&next); err != nil {
			return rec, err
		}
		rec = next
		return rec, nil
	}
	var out strings.Builder
	now := time.Now()
	require.NoError(t, reviewAcceptCommand("accept", []string{"a.go:3", "by", "design"}, load, update, &out, now))
	require.False(t, rec.Findings[0].Blocking, "an accepted finding no longer blocks")
	require.Equal(t, "by design", rec.Accepted[0].Reason)

	out.Reset()
	require.NoError(t, reviewAcceptCommand("accepted", nil, load, update, &out, now))
	require.Contains(t, out.String(), "- a.go:3 nil deref (accepted: by design)")

	require.ErrorIs(t, reviewAcceptCommand("accept", []string{"b.go:1"}, load, update, &out, now), review.ErrNotRecorded)
	require.NoError(t, reviewAcceptCommand("unaccept", []string{"a.go:3"}, load, update, &out, now))
	require.True(t, rec.Findings[0].Blocking, "it blocks again as its label says")
	require.Empty(t, rec.Accepted)
	require.ErrorIs(t, reviewAcceptCommand("unaccept", []string{"a.go:3"}, load, update, &out, now), errNotAcceptedOnPR)
	require.ErrorIs(t, reviewAcceptCommand("nope", nil, load, update, &out, now), errReviewAcceptUsage)
}

func TestFollowUp_AnAcceptedThreadIsSettledAndTheBriefSaysSo(t *testing.T) {
	fu := followUpWith(t, review.Record{
		Findings: []review.RecordFinding{{Path: "a.go", Line: 1, Blocking: true}},
		Threads:  map[string]bool{"T1": true},
		Accepted: []review.Accepted{{File: "a.go", Line: 1, What: "drop x", Reason: "kept on purpose"}},
	})
	th := fu.Threads()[0]
	require.True(t, th.Known)
	require.False(t, th.Blocking, "accepted, not fixing: the thread no longer blocks")
	require.Contains(t, fu.PromptFollowUp(), "- a.go:1 drop x (why: kept on purpose)")
	require.Len(t, fu.Accepted(), 1)
}

func TestIsReviewAccept(t *testing.T) {
	require.True(t, isReviewAccept([]string{"review", "accept", "42", "a.go:3"}))
	require.False(t, isReviewAccept([]string{"review", "42"}))
}

func TestMergeRecord_KeepsWhatWasAcceptedAndItsThreadStopsBlocking(t *testing.T) {
	old := review.Record{Accepted: []review.Accepted{{File: "a.go", Line: 1}}, Threads: map[string]bool{"T1": true}}
	stale := followUpWith(t, review.Record{Findings: []review.RecordFinding{{Path: "a.go", Line: 1, Blocking: true}}})
	got := mergeRecord(review.Record{}, old, true, nil, stale, true)
	require.Equal(t, old.Accepted, got.Accepted)
	require.False(t, got.Threads["T1"], "an accepted thread never blocks again")
}
