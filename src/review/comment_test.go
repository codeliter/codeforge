//nolint:err113 // dynamic error messages are okay in tests
package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

type recordingRunner struct {
	name string
	args []string
	out  string
	err  error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name, r.args = name, args
	return []byte(r.out), r.err
}

func TestPostRequest_PostRunsGhWithTheTargetAndFooter(t *testing.T) {
	var c PostRequest
	require.False(t, c.Enabled())
	require.False(t, (*PostRequest)(nil).Enabled(), "no request means no posting")

	c.Target("acme/widgets", "42")
	require.True(t, c.Enabled())
	run := &recordingRunner{out: "https://github.com/acme/widgets/pull/42#issuecomment-1\n"}
	url, err := c.Post(t.Context(), run, "  Request changes.\n\n- issue: a.go:3 drops data  ", "")
	require.NoError(t, err)
	require.Equal(t, "https://github.com/acme/widgets/pull/42#issuecomment-1", url)
	require.Equal(t, "gh", run.name)
	require.Equal(t, []string{"pr", "comment", "42", "--repo", "acme/widgets"}, run.args[:5])
	require.True(t, strings.HasPrefix(run.args[5], "--body=Request changes."), "the body is one --body= argument")
	require.True(t, strings.HasSuffix(run.args[5], "Reviewed with codeforge.</sub>"))
}

func TestPostRequest_PostWithoutRepoUsesTheCheckoutAndKeepsADashBody(t *testing.T) {
	var c PostRequest
	c.Target("", "7")
	run := &recordingRunner{}
	_, err := c.Post(t.Context(), run, "- first point", "")
	require.NoError(t, err)
	require.Equal(t, []string{"pr", "comment", "7"}, run.args[:3])
	require.Len(t, run.args, 4, "no --repo when none was given")
	require.True(t, strings.HasPrefix(run.args[3], "--body=- first point"))
}

func TestPostRequest_PostErrors(t *testing.T) {
	var unset PostRequest
	_, err := unset.Post(t.Context(), &recordingRunner{}, "x", "")
	require.ErrorIs(t, err, ErrNoPR)

	var c PostRequest
	c.Target("", "7")
	_, err = c.Post(t.Context(), &recordingRunner{err: errors.New("HTTP 403")}, "x", "")
	require.ErrorContains(t, err, "posting the review on PR 7")
	require.ErrorContains(t, err, "HTTP 403")
}

func TestCommentBody_CutsALongReviewAtALine(t *testing.T) {
	long := strings.Repeat("a finding about the change\n", 4000)
	body, err := CommentBody(long, "")
	require.NoError(t, err)
	require.LessOrEqual(t, len(body), 65_000)
	require.Contains(t, body, "The rest was cut")
	require.True(t, strings.HasSuffix(body, commentFooter))
	require.NotContains(t, strings.TrimSuffix(strings.SplitN(body, "\n\n_The rest", 2)[0], "\n"), "a finding about the chang\n\n", "cut at a whole line")

	short, err := CommentBody("Approve.", "")
	require.NoError(t, err)
	require.Equal(t, "Approve."+commentFooter, short)
}

func TestCommentBody_KeepsTheSecondOpinionWhenTheReviewIsLong(t *testing.T) {
	review := "Approve.\n" + strings.Repeat("a long line of the review\n", 4000)
	second := "- juror: fail. bad\n  blocking a.go:3 drops data\n"
	body, err := CommentBody(review, second)
	require.NoError(t, err)
	require.LessOrEqual(t, len(body), 65_000)
	require.Contains(t, body, "blocking a.go:3 drops data", "the juror's blocker survives the cut")
	require.Contains(t, body, "Second opinion, unverified")
	require.Contains(t, body, "The rest was cut")
	require.True(t, strings.HasSuffix(body, commentFooter))
	require.Less(t, strings.Index(body, "Approve."), strings.Index(body, "Second opinion"), "the review comes first")

	// Noise in the second opinion is shortened and dropped, never allowed to push the whole over GitHub's limit.
	huge, err := CommentBody(review, strings.Repeat("- juror: pass. fine\n  should a.go:1 a small thing\n", 3000))
	require.NoError(t, err)
	require.LessOrEqual(t, len(huge), 65_000)
}

func TestCommentBody_RefusesToPostWhenTheBlockersDoNotFit(t *testing.T) {
	var blockers strings.Builder
	for i := range 100 {
		fmt.Fprintf(&blockers, "- j%d: fail. bad\n  blocking f%d.go:%d %s\n", i, i, i, strings.Repeat("w", 700))
	}
	_, err := CommentBody("Approve.", blockers.String())
	require.ErrorIs(t, err, ErrNoRoom, "cutting a juror's blocker would publish an Approve without it")

	var c PostRequest
	c.Target("", "7")
	run := &recordingRunner{}
	_, err = c.Post(t.Context(), run, "Approve.", blockers.String())
	require.ErrorIs(t, err, ErrNoRoom)
	require.Empty(t, run.name, "gh is not called")
}

func TestCommentBody_AShortReviewLeavesRoomForManyBlockers(t *testing.T) {
	var blockers strings.Builder
	for i := range 30 {
		fmt.Fprintf(&blockers, "- j%d: fail. bad\n  blocking f%d.go:%d %s\n", i, i, i, strings.Repeat("w", 700))
	}
	body, err := CommentBody("Approve.", blockers.String())
	require.NoError(t, err)
	require.Contains(t, body, "blocking f29.go:29", "thirty 700-byte blockers fit when the review is short")
	require.LessOrEqual(t, len(body), 65_000)
}

func TestEngineOutput(t *testing.T) {
	text, err := EngineOutput([]byte(`{"output":"Approve.","steps":3}`))
	require.NoError(t, err)
	require.Equal(t, "Approve.", text)

	_, err = EngineOutput([]byte(`{"output":"  \n"}`))
	require.ErrorIs(t, err, ErrNoReviewText)
	_, err = EngineOutput([]byte(`{"output":"","error":"Timeout after 2m 54s"}`))
	require.ErrorContains(t, err, "Timeout after 2m 54s")
	_, err = EngineOutput([]byte("not json"))
	require.ErrorIs(t, err, errEngineOutput)
}

func TestPostOf(t *testing.T) {
	require.Nil(t, PostOf(t.Context()))
	c := &PostRequest{}
	require.Same(t, c, PostOf(WithPost(t.Context(), c)))
}

func TestCommentNote_NamesThePersonaAndWhoPosts(t *testing.T) {
	require.Contains(t, CommentNote, "codeforge-persona")
	require.Contains(t, CommentNote, "you post nothing")
}

func TestFitSecondOpinion_NeverLosesABlocker(t *testing.T) {
	// A first juror with a 15,000-byte summary, then another juror's blocker, after should-fix noise.
	long := "- first: fail. " + strings.Repeat("s", 15_000) + "\n  should x.go:1 " + strings.Repeat("n", 3000) + "\n"
	blocker := "- second: fail. bad\n  blocking a.go:3 drops data\n"
	const limit = 16_000
	fit, ok := fitSecondOpinion(long+blocker, limit)
	require.True(t, ok)
	require.LessOrEqual(t, len(fit), limit)
	require.Contains(t, fit, "blocking a.go:3 drops data", "the blocker after a long summary survives")
	require.NotContains(t, fit, "should x.go:1", "should-fix lines go first")

	// Summaries and long lines are shortened before any blocker is lost.
	var many strings.Builder
	for i := range 12 {
		fmt.Fprintf(&many, "- j%d: fail. %s\n  blocking f%d.go:%d %s\n", i, strings.Repeat("s", 900), i, i, strings.Repeat("w", 600))
	}
	fit, ok = fitSecondOpinion(many.String(), limit)
	require.True(t, ok)
	require.LessOrEqual(t, len(fit), limit)
	for _, want := range []string{"blocking f0.go:0", "blocking f11.go:11"} {
		require.Contains(t, fit, want, "shortening the summaries was enough to keep every blocker")
	}

	// Blockers alone beyond the limit cannot be kept, and nothing is cut silently.
	var crowd strings.Builder
	for i := range 100 {
		fmt.Fprintf(&crowd, "  blocking f%d.go:%d %s\n", i, i, strings.Repeat("w", 700))
	}
	_, ok = fitSecondOpinion(crowd.String(), limit)
	require.False(t, ok)

	short := "- a: pass. ok\n"
	same, ok := fitSecondOpinion(short, limit)
	require.True(t, ok)
	require.Equal(t, short, same, "nothing is changed when it fits")
}

func TestShorten(t *testing.T) {
	require.Equal(t, "abc", shorten("abc", 5))
	require.Equal(t, "ab…", shorten("abcdef", 2))
	require.True(t, utf8.ValidString(shorten(strings.Repeat("é", 10), 5)), "never inside a character")
}

func TestCommentBody_NeverCutsInsideACharacter(t *testing.T) {
	long := strings.Repeat("é", maxCommentBytes) // two bytes each, and no newline to cut at
	body, err := CommentBody(long, "")
	require.NoError(t, err)
	require.True(t, utf8.ValidString(body))
	require.Contains(t, body, "The rest was cut")
}

func TestPostRequest_Preapproval(t *testing.T) {
	var c PostRequest
	require.False(t, c.Preapproved())
	c.Preapprove()
	require.True(t, c.Preapproved())
}
