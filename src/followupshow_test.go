package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/review"
)

func TestReviewArgs_FollowUpFlags(t *testing.T) {
	_, err := reviewArgs(t.Context(), "", config.ReviewConfig{}, nil, nil, []string{"42", "--follow-up", "--fresh"})
	require.ErrorIs(t, err, errFollowUpFresh)
	_, err = reviewArgs(t.Context(), "", config.ReviewConfig{}, nil, nil, []string{"--follow-up"})
	require.ErrorIs(t, err, errFollowUpNeedsPR, "a follow-up is on a PR")
}

var errUnexpectedCall = errors.New("unexpected call")

// mapRunner answers each gh call by the longest key its arguments start with.
type mapRunner map[string]string

func (m mapRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	best := ""
	for k := range m {
		if strings.HasPrefix(key, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return nil, fmt.Errorf("%w: %s", errUnexpectedCall, key)
	}
	return []byte(m[best]), nil
}

// followUpWith is a follow-up round on acme/w#42 with record rec: the user's thread T1 on a.go:1, which the author
// answered.
func followUpWith(t *testing.T, rec review.Record) *review.FollowUp {
	t.Helper()
	r := mapRunner{
		"gh api user": "me\n",
		"gh api --paginate --slurp repos/acme/w/pulls/42/reviews":   `[[{"user":{"login":"me"},"state":"CHANGES_REQUESTED","commit_id":"old","submitted_at":"2026-11-08T10:00:00Z"}]]`,
		"gh api --paginate --slurp repos/acme/w/issues/42/comments": `[[]]`,
		"gh api graphql": `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
{"id":"T1","isResolved":false,"isOutdated":false,"path":"a.go","line":1,"originalLine":1,"root":{"nodes":[{"databaseId":11,"author":{"login":"me"}}]},
"comments":{"nodes":[{"databaseId":11,"author":{"login":"me"},"body":"Drop x.","createdAt":"2026-11-08T10:00:00Z"},{"databaseId":12,"author":{"login":"alice"},"body":"Dropped.","createdAt":"2026-11-08T11:00:00Z"}]}}]}}}}}`,
		"gh api repos/acme/w/compare/old...new --jq .status": "identical",
	}
	fu, found, err := review.LoadFollowUp(t.Context(), r, "acme/w", "42", "new", rec, true)
	require.NoError(t, err)
	require.True(t, found)
	return fu
}

// activeFollowUp is followUpWith a record that says T1 blocks.
func activeFollowUp(t *testing.T) *review.FollowUp {
	t.Helper()
	return followUpWith(t, review.Record{Findings: []review.RecordFinding{{Path: "a.go", Line: 1, Blocking: true}}})
}

// followUpEngine answers a follow-up round: no new findings, a final comment, and the verdict on T1.
const followUpEngine = `printf '%s' '{"output":"Good now.\n\nDecision: Approve\n\n` + "```json" + `\n{\"threads\":[{\"thread\":\"T1\",\"verdict\":\"fixed\",\"reply\":\"Confirmed, thanks.\",\"resolve\":true}]}\n` + "```" + `","steps":2,"cost":0.1,"duration":1000}'`

func TestRecordPost_NothingKnownIsLostWhileItsThreadIsOpen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rs := reviewRecords()
	require.NoError(t, rs.Update(t.Context(), "me", "acme/w", "42", func(review.Record, bool) review.Record {
		return review.Record{Repo: "acme/w", Number: "42", Head: "old", Posted: []int64{100},
			Findings: []review.RecordFinding{{Path: "a.go", Line: 1, Blocking: true}, {Path: "b.go", Line: 9, Blocking: true}},
			Threads:  map[string]bool{"T9": true}}
	}))
	post := &review.PostRequest{}
	post.Target("acme/w", "42")
	post.SetHead("new")

	// A fresh review keeps what the earlier one knew, and the review it posted is known by its id.
	recordPost(t.Context(), "me", post, review.Posted{ReviewID: 200}, review.DecisionComment, "suggestion: c.go:3 — New.", nil, nil, true)
	rec, ok := rs.Load(t.Context(), "me", "acme/w", "42")
	require.True(t, ok)
	require.Equal(t, "new", rec.Head)
	require.Len(t, rec.Findings, 3)
	require.True(t, rec.Threads["T9"])
	require.Equal(t, []int64{200, 100}, rec.Posted)
	recordPost(t.Context(), "me", post, review.Posted{ReviewID: 200}, review.DecisionComment, "suggestion: c.go:3 — New.", nil, nil, true)
	rec, _ = rs.Load(t.Context(), "me", "acme/w", "42")
	require.Equal(t, []int64{200, 100}, rec.Posted, "the same review recorded twice is recorded once")
	_, other := rs.Load(t.Context(), "someone-else", "acme/w", "42")
	require.False(t, other, "another account has no history here")

	// A follow-up drops only what GitHub confirmed resolved, and learns the rest by thread id.
	fu := activeFollowUp(t)
	recordPost(t.Context(), "me", post, review.Posted{}, review.DecisionApprove, "Good.", []review.ThreadOutcome{{Thread: "T1", Resolved: true}}, fu, true)
	rec, _ = rs.Load(t.Context(), "me", "acme/w", "42")
	require.NotContains(t, rec.Findings, review.RecordFinding{Path: "a.go", Line: 1, Blocking: true}, "T1 on a.go:1 is resolved")
	require.Contains(t, rec.Findings, review.RecordFinding{Path: "b.go", Line: 9, Blocking: true})
	require.NotContains(t, rec.Threads, "T1")
	require.True(t, rec.Threads["T9"])

	recordPost(t.Context(), "me", post, review.Posted{}, review.DecisionComment, "Still.", []review.ThreadOutcome{{Thread: "T1", Err: errUnexpectedCall}}, activeFollowUp(t), true)
	rec, _ = rs.Load(t.Context(), "me", "acme/w", "42")
	require.True(t, rec.Threads["T1"], "a resolve that failed keeps the thread a blocker")

	post.SetHead("newer")
	recordPost(t.Context(), "me", post, review.Posted{}, review.DecisionComment, "nitpick (non-blocking): b.go:9 — Tidy.", nil, activeFollowUp(t), false)
	rec, _ = rs.Load(t.Context(), "me", "acme/w", "42")
	require.Equal(t, "old", rec.Head, "a round that did not judge every thread keeps the commit it followed up on")
	require.Contains(t, rec.Findings, review.RecordFinding{Path: "b.go", Line: 9, Blocking: true}, "a new finding at the same place never hides an earlier blocker")
}

func TestMergeRecord_AStaleRoundNeverLowersABlocker(t *testing.T) {
	stale := followUpWith(t, review.Record{Findings: []review.RecordFinding{{Path: "a.go", Line: 1}}}) // T1 read as non-blocking
	require.False(t, stale.Threads()[0].Blocking)
	got := mergeRecord(review.Record{}, review.Record{Threads: map[string]bool{"T1": true}}, true, nil, stale, true)
	require.True(t, got.Threads["T1"], "a blocker another run recorded by id stays one")

	fresh := review.Record{Findings: []review.RecordFinding{{Path: "a.go", Line: 1, Blocking: true}}}
	got = mergeRecord(review.Record{}, fresh, true, nil, stale, true)
	require.True(t, got.Threads["T1"], "a blocking finding another run recorded where the thread was posted keeps it a blocker")
}

func TestRecordPost_AnUnjudgedRoundWithNoRecordKeepsItsBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	post := &review.PostRequest{}
	post.Target("acme/w", "42")
	post.SetHead("new")
	recordPost(t.Context(), "me", post, review.Posted{}, review.DecisionComment, "Still.", nil, activeFollowUp(t), false)
	rec, ok := reviewRecords().Load(t.Context(), "me", "acme/w", "42")
	require.True(t, ok)
	require.Equal(t, "old", rec.Head, "the commit the round followed up on, taken from GitHub when no record had it")
	require.True(t, rec.Threads["T1"])
}

func TestRunReviewEngine_ADraftFollowUpIsShownWithoutPosting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newPostFixture(t, followUpEngine, 0)
	onTerminal(t, false)
	held := &review.FollowUp{}
	held.Set(activeFollowUp(t))
	stdout, _, err := captured(t, func() error {
		// No posting request at all: the round is only read.
		_, err := runReviewEngine(review.WithFollowUp(t.Context(), held), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
		return err
	})
	require.NoError(t, err)
	require.Contains(t, stdout, "a.go:1 — fixed")
	require.Contains(t, stdout, "Good now.")
	_, err = os.Stat(f.ghArgs)
	require.ErrorIs(t, err, os.ErrNotExist, "nothing is posted")
}

func TestRunReviewEngine_AFollowUpIsNeverPostedAsAnotherAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newPostFixture(t, followUpEngine, 0)
	withHead(t, f, "new")
	old := codeRunner
	codeRunner = mapRunner{"gh api user": "bob\n"} // the round was read as me
	t.Cleanup(func() { codeRunner = old })
	onTerminal(t, true)
	pressing(t, "1", nil)
	held := &review.FollowUp{}
	held.Set(activeFollowUp(t))
	_, err := runReviewEngine(review.WithFollowUp(postingContext(t), held), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
	require.ErrorIs(t, err, errAccountChanged)
	require.ErrorContains(t, err, "read as me, but gh is now signed in as bob")
	if posted, err := os.ReadFile(f.ghArgs); !errors.Is(err, os.ErrNotExist) {
		require.NoError(t, err)
		require.NotContains(t, string(posted), "pulls/42/reviews", "no review goes up")
		require.NotContains(t, string(posted), "replies")
	}
	_, ok := reviewRecords().Load(t.Context(), "me", "acme/w", "42")
	require.False(t, ok)
	_, ok = reviewRecords().Load(t.Context(), "bob", "acme/w", "42")
	require.False(t, ok)

	codeRunner = mapRunner{} // gh cannot tell who is signed in
	pressing(t, "1", nil)
	_, err = runReviewEngine(review.WithFollowUp(postingContext(t), held), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
	require.ErrorIs(t, err, errAccountChanged, "a follow-up whose account cannot be confirmed is not posted")
	_, ok = reviewRecords().Load(t.Context(), "me", "acme/w", "42")
	require.False(t, ok)
}

func TestMergeRecord_ARoundThatStartedEarlierNeverMovesTheBaselineBack(t *testing.T) {
	early, late := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 8, 11, 0, 0, 0, time.UTC)
	got := mergeRecord(review.Record{Head: "h1", At: early}, review.Record{Head: "h2", At: late}, true, nil, nil, true)
	require.Equal(t, "h2", got.Head, "the round that read h2 started later, though this one finished last")
	require.Equal(t, late, got.At)
}

func TestMergeRecord_AResolvedThreadTakesItsFindingWhenNoOpenThreadSitsInIt(t *testing.T) {
	fu := followUpWith(t, review.Record{})
	old := review.Record{Findings: []review.RecordFinding{{Path: "a.go", Line: 1, End: 3, Blocking: true}}}
	got := mergeRecord(review.Record{}, old, true, []review.ThreadOutcome{{Thread: "T1", Resolved: true}}, fu, true)
	require.Empty(t, got.Findings, "T1 sat on the range's first line and is resolved, and no other thread sits in it")
}

func TestMergeRecord_APartPostKeepsTheEarlierBaseline(t *testing.T) {
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	got := mergeRecord(review.Record{Head: "new", At: time.Now()}, review.Record{Head: "old", At: at}, true, nil, nil, false)
	require.Equal(t, "old", got.Head)
	require.Equal(t, at, got.At, "so a later review on GitHub still wins the baseline")
	got = mergeRecord(review.Record{Head: "new"}, review.Record{}, false, nil, nil, false)
	require.Empty(t, got.Head, "with no review before it, the next round starts from scratch")
}

func TestFollowUpPlain_ShowsWhatWillBePosted(t *testing.T) {
	fu := activeFollowUp(t)
	got := followUpPlain(fu, []review.ThreadAnswer{{Thread: "T1", Verdict: "fixed", Reply: "Confirmed.", Resolve: true}})
	require.Equal(t, "Your earlier threads:\n\na.go:1 — fixed, resolve\n  reply: Confirmed.\n\n", got)
	require.Empty(t, followUpPlain(nil, nil))
}

// withHead makes the fixture's gh answer that the PR's head is head.
func withHead(t *testing.T, f postFixture, head string) {
	t.Helper()
	gh := "#!/bin/sh\nprintf '%s ' \"$@\" >> \"" + f.ghArgs + "\"\necho >> \"" + f.ghArgs + "\"\n" +
		"prev=\nfor a in \"$@\"; do if [ \"$prev\" = --input ]; then cat \"$a\" >> \"" + f.ghArgs + "\"; fi; prev=$a; done\n" +
		"if [ \"$1\" = pr ] && [ \"$2\" = view ]; then echo " + head + "; exit 0; fi\n" +
		"if [ \"$1\" = api ] && [ \"$2\" = user ]; then echo me; exit 0; fi\n" +
		"if [ \"$1\" = api ] && [ \"$2\" = graphql ]; then echo '{\"data\":{\"resolveReviewThread\":{\"thread\":{\"isResolved\":true}}}}'; exit 0; fi\n" +
		"echo https://github.com/acme/w/pull/42#issuecomment-9\n"
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(f.ghArgs), "gh"), []byte(gh), execPerm))
}

func TestRunReviewEngine_ANewPushResolvesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newPostFixture(t, followUpEngine, 0)
	withHead(t, f, "newer")
	onTerminal(t, true)
	pressing(t, "1", nil)
	held := &review.FollowUp{}
	held.Set(activeFollowUp(t))
	_, stderr, err := captured(t, func() error {
		_, err := runReviewEngine(review.WithFollowUp(postingContext(t), held), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
		return err
	})
	require.NoError(t, err)
	posted, err := os.ReadFile(f.ghArgs)
	require.NoError(t, err)
	require.Contains(t, string(posted), "comments/11/replies", "the reply still goes up")
	require.NotContains(t, string(posted), "resolveReviewThread", "the author pushed while the round ran")
	require.Contains(t, stderr, "no thread is resolved")
}

func TestRunReviewEngine_AFollowUpShowsTheThreadsAndPostsTheReplies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newPostFixture(t, followUpEngine, 0)
	withHead(t, f, "new")
	onTerminal(t, true)
	shown := pressing(t, "1", nil)
	held := &review.FollowUp{}
	held.Set(activeFollowUp(t))
	ctx := review.WithFollowUp(postingContext(t), held)
	_, stderr, err := captured(t, func() error {
		_, err := runReviewEngine(ctx, []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
		return err
	})
	require.NoError(t, err)
	require.Contains(t, shown.Items[0].Title, "a.go")
	require.Contains(t, shown.Items[0].Title, "your threads", "the earlier threads come first")
	require.Equal(t, "1 fixed", shown.Items[0].Summary)
	require.Contains(t, shown.Items[0].Body, "@alice Dropped.")
	require.Contains(t, shown.Items[0].Body, "blocking · resolve the thread")
	require.Contains(t, shown.Items[0].Body, "Confirmed, thanks.")
	for _, it := range shown.Items {
		require.NotContains(t, it.Body, "```json", "the verdicts block is never shown as text")
	}

	posted, err := os.ReadFile(f.ghArgs)
	require.NoError(t, err)
	require.Contains(t, string(posted), `"event":"APPROVE"`, "the review of the round")
	require.NotContains(t, string(posted), "thread\\\":\\\"T1", "nor posted")
	require.Contains(t, string(posted), "api --method POST repos/acme/w/pulls/42/comments/11/replies --raw-field=body=Confirmed, thanks.")
	require.Contains(t, string(posted), "resolveReviewThread")
	require.Contains(t, string(posted), "id=T1")
	require.Contains(t, stderr, "replied on a.go:1 and resolved it")

	rec, ok := reviewRecords().Load(t.Context(), "me", "acme/w", "42")
	require.True(t, ok, "what was posted is kept for the next round")
	require.Equal(t, review.DecisionApprove, rec.Decision)
	_, err = os.Stat(filepath.Join(os.Getenv("HOME"), ".codeforge", "sessions", "sessions.db"))
	require.NoError(t, err, "in the session database")
}
