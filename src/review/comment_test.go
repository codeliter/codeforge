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

	"github.com/codeliter/codeforge/src/text"
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
	got, err := c.Post(t.Context(), run, "  Verdict: Request changes.\n\nData loss on retry.\n\n- issue: a.go:3 drops data  ", "")
	require.NoError(t, err)
	require.Equal(t, "https://github.com/acme/widgets/pull/42#issuecomment-1", got.URL)
	require.Zero(t, got.Inline, "no diff, so nothing goes inline")
	require.Equal(t, "gh", run.name)
	require.Equal(t, []string{"pr", "comment", "42", "--repo", "acme/widgets"}, run.args[:5])
	require.True(t, strings.HasPrefix(run.args[5], "--body=Data loss on retry."), "the body is one --body= argument, without the verdict label")
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

func TestCommentBody_KeepsTheLeadsWhenTheReviewIsLong(t *testing.T) {
	review := "Looks fine.\n" + strings.Repeat("a long line of the review\n", 4000)
	leads := LeadsHeader + "- `a.go:3` drops data\n"
	body, err := CommentBody(review, leads)
	require.NoError(t, err)
	require.LessOrEqual(t, len(body), 65_000)
	require.Contains(t, body, "`a.go:3` drops data", "the lead survives the cut")
	require.Contains(t, body, "The rest was cut")
	require.True(t, strings.HasSuffix(body, commentFooter))
	require.Less(t, strings.Index(body, "Looks fine."), strings.Index(body, "Unconfirmed"), "the review comes first")

	only, err := CommentBody("", leads)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(only, "**Unconfirmed"), "no rule above the leads when the summary is empty")
}

func TestCommentBody_RefusesToPostWhenTheBlockersDoNotFit(t *testing.T) {
	var blockers strings.Builder
	for i := range 100 {
		fmt.Fprintf(&blockers, "- `f%d.go:%d` %s\n", i, i, strings.Repeat("w", 700))
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
		fmt.Fprintf(&blockers, "- `f%d.go:%d` %s\n", i, i, strings.Repeat("w", 700))
	}
	body, err := CommentBody("Approve.", blockers.String())
	require.NoError(t, err)
	require.Contains(t, body, "`f29.go:29`", "thirty 700-byte blockers fit when the review is short")
	require.LessOrEqual(t, len(body), 65_000)
}

func TestEngineOutput(t *testing.T) {
	out, err := EngineOutput([]byte(`{"output":"Approve.","steps":3}`))
	require.NoError(t, err)
	require.Equal(t, "Approve.", out)

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

const inlineDiff = `diff --git a/search/serper/main.go b/search/serper/main.go
--- a/search/serper/main.go
+++ b/search/serper/main.go
@@ -70,3 +70,4 @@ func main() {
 	ctx := context.Background()
-	// old
+	// gprclogging.PayloadReceived,
+	client := kit.InstrumentHTTPClient(nil, nil)
 	run(ctx)
diff --git a/common/go/telemetry/telemetry.go b/common/go/telemetry/telemetry.go
new file mode 100644
--- /dev/null
+++ b/common/go/telemetry/telemetry.go
@@ -0,0 +1,2 @@
+package telemetry
+func Init() {}
`

const inlineReview = `Verdict: Comment. Correlation-only tracing behind one seam.

### search/serper/main.go

suggestion: search/serper/main.go:72 — Remove the commented-out code.
nitpick (non-blocking): search/serper/main.go:300 — Off the diff, so it stays in the summary.

### common/go/telemetry/telemetry.go

- **suggestion:** ` + "`common/go/telemetry/telemetry.go:2`" + ` — Init has no tests. Add focused tests for:
  - Init(ctx, nil)
  - the error path

### Across files

suggestion: go.mod — split the unrelated bumps.`

func TestDiffLines_OnlyTheNewSideOfEachHunk(t *testing.T) {
	got := diffLines(inlineDiff)
	require.Equal(t, map[int]bool{70: true, 71: true, 72: true, 73: true}, got["search/serper/main.go"])
	require.Equal(t, map[int]bool{1: true, 2: true}, got["common/go/telemetry/telemetry.go"])
}

func TestSplitFindings_PutsEachFindingOnItsLine(t *testing.T) {
	summary, inline := SplitFindings(StripVerdict(inlineReview), inlineDiff)
	require.Equal(t, []InlineComment{
		{Path: "search/serper/main.go", Line: 72, Body: "suggestion: Remove the commented-out code."},
		{Path: "common/go/telemetry/telemetry.go", Line: 2, Body: "suggestion: Init has no tests. Add focused tests for:\n- Init(ctx, nil)\n- the error path"},
	}, inline)
	require.True(t, strings.HasPrefix(summary, "Correlation-only tracing"), "no verdict label: %q", summary)
	require.Contains(t, summary, "### search/serper/main.go\n\nnitpick (non-blocking): search/serper/main.go:300", "a finding off the diff keeps its file heading")
	require.NotContains(t, summary, "### common/go/telemetry/telemetry.go", "a heading left empty is dropped")
	require.Contains(t, summary, "### Across files\n\nsuggestion: go.mod")
}

func TestStripVerdict(t *testing.T) {
	for _, in := range []string{"Verdict: Comment. Fine.", "**Verdict:** Comment. Fine.", "1. **Verdict:** Request changes — Fine.", "Verdict: Approve\n\nFine."} {
		require.Equal(t, "Fine.", StripVerdict(in), in)
	}
	require.Equal(t, "The verdict is pending.", StripVerdict("The verdict is pending."))
}

func TestPostRequest_PostsInlineCommentsAsOneReview(t *testing.T) {
	var c PostRequest
	c.Target("acme/w", "42")
	c.SetDiff(inlineDiff)
	run := &recordingRunner{out: `{"html_url":"https://github.com/acme/w/pull/42#pullrequestreview-1"}`}
	got, err := c.Post(t.Context(), run, inlineReview, LeadsHeader+"- `a.go:3` drops data\n")
	require.NoError(t, err)
	require.Equal(t, Posted{URL: "https://github.com/acme/w/pull/42#pullrequestreview-1", Inline: 2}, got)
	require.Equal(t, []string{"api", "--method", "POST", "repos/acme/w/pulls/42/reviews", "--raw-field=event=COMMENT"}, run.args[:5])
	require.True(t, strings.HasPrefix(run.args[5], "--raw-field=body=Correlation-only"))
	require.Contains(t, run.args[5], "Unconfirmed blocking leads")
	require.Equal(t, []string{
		"--raw-field=comments[][path]=search/serper/main.go",
		"--field=comments[][line]=72",
		"--raw-field=comments[][side]=RIGHT",
		"--raw-field=comments[][body]=suggestion: Remove the commented-out code.",
	}, run.args[6:10])
	require.Len(t, run.args, 14)
}

type failFirstRunner struct{ calls [][]string }

func (r *failFirstRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	if len(r.calls) == 1 {
		return nil, errors.New("HTTP 422: Line could not be resolved")
	}
	return []byte("https://github.com/o/r/pull/7#issuecomment-2\n"), nil
}

func TestPostRequest_FallsBackToOneCommentWhenGitHubRefusesTheLines(t *testing.T) {
	var c PostRequest
	c.Target("", "7")
	c.SetDiff(inlineDiff)
	run := &failFirstRunner{}
	got, err := c.Post(t.Context(), run, inlineReview, "")
	require.NoError(t, err)
	require.ErrorContains(t, got.Fallback, "422")
	require.Equal(t, "repos/{owner}/{repo}/pulls/7/reviews", run.calls[0][3])
	require.Equal(t, []string{"pr", "comment", "7"}, run.calls[1][:3])
	require.Contains(t, run.calls[1][3], "search/serper/main.go:72", "the fallback keeps every finding")
}

func TestShorten(t *testing.T) {
	require.Equal(t, "abc", text.Shorten("abc", 5))
	require.Equal(t, "ab…", text.Shorten("abcdef", 2))
	require.True(t, utf8.ValidString(text.Shorten(strings.Repeat("é", 10), 5)), "never inside a character")
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
