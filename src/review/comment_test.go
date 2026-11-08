//nolint:err113 // dynamic error messages are okay in tests
package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
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
	run := &inputRunner{out: `{"html_url":"https://github.com/acme/widgets/pull/42#pullrequestreview-1"}`}
	got, err := c.Post(t.Context(), run, "  Verdict: Request changes.\n\n### Final comment\n\nData loss on retry.\n\nDecision: Request changes", "", "")
	require.NoError(t, err)
	require.Equal(t, Posted{URL: "https://github.com/acme/widgets/pull/42#pullrequestreview-1", Decision: DecisionRequestChanges}, got)
	require.Equal(t, []string{"api", "--method", "POST", "repos/acme/widgets/pulls/42/reviews", "--input"}, run.args[:5])
	req := decodeReview(t, run.input)
	require.Equal(t, DecisionRequestChanges, req.Event)
	require.True(t, strings.HasPrefix(req.Body, "Data loss on retry."), "the final comment is the body: no verdict label, heading or decision line")
	require.True(t, strings.HasSuffix(req.Body, "Review assisted by Codeforge</sub>"))
	require.Empty(t, req.Comments)
}

// reviewRequest is the json codeforge sends to create a review.
type reviewRequest struct {
	Event    Decision        `json:"event"`
	Body     string          `json:"body"`
	Comments []reviewComment `json:"comments"`
}

func decodeReview(t *testing.T, data []byte) reviewRequest {
	t.Helper()
	var req reviewRequest
	require.NoError(t, json.Unmarshal(data, &req))
	return req
}

// ownPRRunner refuses Approve and Request changes, as GitHub does on your own pull request.
type ownPRRunner struct{ events []Decision }

func (r *ownPRRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	data, err := os.ReadFile(args[len(args)-1])
	if err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}
	var req reviewRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("decoding the request: %w", err)
	}
	r.events = append(r.events, req.Event)
	if req.Event != DecisionComment {
		return nil, errors.New("HTTP 422: Can not request changes on your own pull request")
	}
	return []byte(`{"html_url":"https://github.com/o/r/pull/7#pullrequestreview-2"}`), nil
}

func TestPostRequest_OnYourOwnPRTheDecisionBecomesAComment(t *testing.T) {
	var c PostRequest
	c.Target("", "7")
	run := &ownPRRunner{}
	got, err := c.Post(t.Context(), run, "### Final comment\n\nFix it.\n\nDecision: Request changes", "", "")
	require.NoError(t, err)
	require.Equal(t, []Decision{DecisionRequestChanges, DecisionComment}, run.events)
	require.Equal(t, DecisionComment, got.Decision)
	require.ErrorContains(t, got.Downgraded, "own pull request")
	require.NoError(t, got.Fallback)
}

func TestPostRequest_PostErrors(t *testing.T) {
	var unset PostRequest
	_, err := unset.Post(t.Context(), &recordingRunner{}, "x", "", "")
	require.ErrorIs(t, err, ErrNoPR)

	var c PostRequest
	c.Target("", "7")
	_, err = c.Post(t.Context(), &recordingRunner{err: errors.New("HTTP 403")}, "x", "", "")
	require.ErrorContains(t, err, "posting the review on PR 7")
	require.ErrorContains(t, err, "HTTP 403")
}

func TestParseDecision(t *testing.T) {
	for in, want := range map[string]Decision{
		"text\n\nDecision: Approve":                                    DecisionApprove,
		"text\n**Decision:** Request changes.":                         DecisionRequestChanges,
		"text\ndecision: comment\n":                                    DecisionComment,
		"text without a decision":                                      DecisionComment,
		"issue (blocking): a.go:3 — drops data\n\nDecision: Approve":   DecisionRequestChanges,
		"nitpick (non-blocking): a.go:3 — Nit: x\n\nDecision: Approve": DecisionApprove,
	} {
		got, rest := ParseDecision(in)
		require.Equal(t, want, got, in)
		require.NotContains(t, strings.ToLower(rest), "decision:", in)
	}
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
	_, err = c.Post(t.Context(), run, "Approve.", blockers.String(), "")
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

func TestFileComments_ACodeBlockStaysWithItsFinding(t *testing.T) {
	in := "suggestion: search/serper/main.go:72 — Close the body on every path, as in:\n\n" +
		"```go\n# not a heading\nsuggestion: x.go:1 — not a finding\ndefer resp.Body.Close()\n```\n\n" +
		"suggestion: common/go/telemetry/telemetry.go:2 — Add a test for Init(ctx, nil).\n" +
		"  ~~~sh\n  go test ./...\n  ~~~\n\n" +
		"Ship it once the body is closed."
	files, rest := FileComments(in)
	require.Equal(t, []FileComment{
		{Path: "search/serper/main.go", Body: "`search/serper/main.go:72`: Close the body on every path, as in:\n\n" +
			"```go\n# not a heading\nsuggestion: x.go:1 — not a finding\ndefer resp.Body.Close()\n```"},
		{Path: "common/go/telemetry/telemetry.go", Body: "`common/go/telemetry/telemetry.go:2`: Add a test for Init(ctx, nil).\n~~~sh\ngo test ./...\n~~~"},
	}, files, "after a blank line or not, and whatever it holds, a code block is the finding's")
	require.Equal(t, "Ship it once the body is closed.", rest)

	files, rest = FileComments("nitpick: a.go:1 — Tidy:\n\n```\nnever closed\n\nShip it.")
	require.Len(t, files, 1)
	require.Contains(t, files[0].Body, "never closed\n\nShip it.", "an unclosed block runs to the end, as Markdown shows it")
	require.Empty(t, rest)
}

func TestFileComments_InlineCodeAndCodeOutsideFindingsAreNoBlocks(t *testing.T) {
	files, rest := FileComments("suggestion: a.go:1 — x\n```foo``` bar\n\nShip it.")
	require.Equal(t, []FileComment{{Path: "a.go", Body: "`a.go:1`: x\n```foo``` bar"}}, files, "inline code opens no block")
	require.Equal(t, "Ship it.", rest)

	files, rest = FileComments("suggestion: a.go:1 — x\n\nShip it, as in:\n\n    ok\n\n```sh\n# keep this\nsuggestion: b.go:2 — not a finding\n```")
	require.Len(t, files, 1, "a line in a code block of the final comment is no finding")
	require.Contains(t, rest, "# keep this\nsuggestion: b.go:2 — not a finding")

	files, _ = FileComments("suggestion: a.go:1 — Use:\n  - one\n  - two\n```go\nx()\n```")
	require.Equal(t, "`a.go:1`: Use:\n- one\n- two\n```go\nx()\n```", files[0].Body, "the list and the block are dedented apart")

	require.Equal(t, "Fine.\n```sh\n# a comment\n```", withoutHeadings("### Final comment\nFine.\n```sh\n# a comment\n```"),
		"a # line in a code block is code, not a heading")

	_, rest = FileComments("suggestion: a.go:1 — x\n\nRun:\n\n```sh\n# step one\n# step two\n\n\nmake\ngo.mod\ngo.sum\n```")
	require.Equal(t, "Run:\n\n```sh\n# step one\n# step two\n\n\nmake\ngo.mod\ngo.sum\n```", rest,
		"no line of a code block in the final comment is dropped as an empty heading, and its blank lines stay")
}

func TestCutAtLine_ClosesACodeBlockItCutsInto(t *testing.T) {
	body := "See:\n```go\n" + strings.Repeat("x()\n", 50) + "```\nAfter."
	got := cutAtLine(body, 40)
	require.True(t, strings.HasSuffix(got, "\n```"+cutNote), "the block is closed before the note: %q", got)
	require.Equal(t, "See:\n```go\n"+strings.Repeat("x()\n", 6)+"x()", strings.TrimSuffix(got, "\n```"+cutNote), "cut at the last line break within 40 bytes")
	require.Equal(t, "One.\nTwo."+cutNote, cutAtLine("One.\nTwo.\nThree.", 12), "text with no block gets no fence")
}

func TestFileComments_ACodeBlockAfterTheLastFindingIsItsExample(t *testing.T) {
	// Pinned: a block that directly follows a finding is that finding's, even when it is the last one; the final
	// comment is plain text (codeforge-review skill, section 7).
	files, rest := FileComments("suggestion: a.go:1 — Close it, as in:\n\n```go\ndefer f.Close()\n```\n\nShip it.")
	require.Contains(t, files[0].Body, "defer f.Close()")
	require.Equal(t, "Ship it.", rest)
}

func TestSplitFindings_PutsEachFindingOnItsLine(t *testing.T) {
	summary, inline := SplitFindings(StripVerdict(inlineReview), inlineDiff)
	require.Equal(t, []InlineComment{
		{Path: "search/serper/main.go", Line: 72, Body: "Remove the commented-out code."},
		{Path: "common/go/telemetry/telemetry.go", Line: 2, Body: "Init has no tests. Add focused tests for:\n- Init(ctx, nil)\n- the error path"},
	}, inline, "a posted comment reads as written, without its label")
	require.True(t, strings.HasPrefix(summary, "Correlation-only tracing"), "no verdict label: %q", summary)
	require.Contains(t, summary, "### search/serper/main.go\n\n`search/serper/main.go:300`: Off the diff, so it stays in the summary.",
		"a finding off the diff keeps its file heading and is named by its place, without its label")
	require.NotContains(t, summary, "### common/go/telemetry/telemetry.go", "a heading left empty is dropped")
	require.Contains(t, summary, "### Across files\n\nsuggestion: go.mod")
}

func TestStripVerdict(t *testing.T) {
	for _, in := range []string{"Verdict: Comment. Fine.", "**Verdict:** Comment. Fine.", "1. **Verdict:** Request changes — Fine.", "Verdict: Approve\n\nFine."} {
		require.Equal(t, "Fine.", StripVerdict(in), in)
	}
	require.Equal(t, "The verdict is pending.", StripVerdict("The verdict is pending."))
}

// commentsRunner records the body of every `gh pr comment` and answers each with the next comment URL.
type commentsRunner struct {
	bodies []string
	failAt int // the 1-based call that fails, 0 for none
}

func (r *commentsRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) < 6 || args[0] != "pr" || args[1] != "comment" {
		return nil, fmt.Errorf("not a PR comment: %v", args)
	}
	if len(r.bodies)+1 == r.failAt {
		return nil, errors.New("HTTP 502")
	}
	r.bodies = append(r.bodies, strings.TrimPrefix(args[5], "--body="))
	return fmt.Appendf(nil, "https://github.com/acme/w/pull/42#issuecomment-%d\n", len(r.bodies)), nil
}

func TestPostRequest_PlainPostsACommentPerFileThenTheFinalOne(t *testing.T) {
	var c PostRequest
	c.Target("acme/w", "42")
	c.SetDiff(inlineDiff)
	run := &commentsRunner{}
	got, err := c.Post(t.Context(), run, inlineReview, "", DecisionPlain)
	require.NoError(t, err)
	require.Equal(t, Posted{URL: "https://github.com/acme/w/pull/42#issuecomment-3", Inline: 2, Decision: DecisionPlain, Final: true}, got)
	require.Len(t, run.bodies, 3, "one comment per file, then the final comment")
	require.Equal(t, "`search/serper/main.go:72`: Remove the commented-out code.\n\n`search/serper/main.go:300`: Off the diff, so it stays in the summary.",
		run.bodies[0], "a file's findings, named by their place and without labels, on or off the diff")
	require.Equal(t, "`common/go/telemetry/telemetry.go:2`: Init has no tests. Add focused tests for:\n- Init(ctx, nil)\n- the error path", run.bodies[1])
	require.True(t, strings.HasPrefix(run.bodies[2], "Correlation-only"), "the final comment comes last: %q", run.bodies[2])
	require.NotContains(t, run.bodies[2], "main.go:72", "findings are not repeated in it")
	require.NotContains(t, run.bodies[2], "###", "without headings")
	require.True(t, strings.HasSuffix(run.bodies[2], commentFooter))
	require.NotContains(t, run.bodies[0], "Codeforge", "the footer is said once")
}

func TestPostRequest_PlainSaysHowFarItGot(t *testing.T) {
	var c PostRequest
	c.Target("acme/w", "42")
	run := &commentsRunner{failAt: 2}
	got, err := c.Post(t.Context(), run, inlineReview, "", DecisionPlain)
	require.ErrorContains(t, err, "posted 1 of 2 file comments")
	require.Equal(t, 1, got.Inline)
	require.Equal(t, "https://github.com/acme/w/pull/42#issuecomment-1", got.URL, "what is already on the PR")

	run = &commentsRunner{}
	got, err = c.Post(t.Context(), run, "suggestion: a.go:1 — Use x.\n\nDecision: Comment", "", DecisionPlain)
	require.NoError(t, err)
	require.False(t, got.Final)
	require.Equal(t, []string{"`a.go:1`: Use x." + commentFooter}, run.bodies, "with no final comment, the last file comment carries the footer")

	run = &commentsRunner{}
	_, err = c.Post(t.Context(), run, "Decision: Comment", "", DecisionPlain)
	require.ErrorIs(t, err, ErrNoReviewText, "an empty review posts nothing, not a bare footer")
	require.Empty(t, run.bodies)
}

func TestPostRequest_PlainFoldsFilesPastTheCapIntoTheFinalComment(t *testing.T) {
	var c PostRequest
	c.Target("acme/w", "42")
	var review strings.Builder
	for i := range MaxPlainFileComments + 2 {
		fmt.Fprintf(&review, "suggestion: f%02d.go:1 — Fix f%02d.\n", i, i)
	}
	review.WriteString("\nShip it after the fixes.\n")
	run := &commentsRunner{}
	got, err := c.Post(t.Context(), run, review.String(), "", DecisionPlain)
	require.NoError(t, err)
	require.Equal(t, MaxPlainFileComments, got.Inline)
	require.Len(t, run.bodies, MaxPlainFileComments+1)
	last := run.bodies[MaxPlainFileComments]
	require.True(t, strings.HasPrefix(last, "Ship it after the fixes.\n\n---\n\n`f10.go:1`: Fix f10.\n\n`f11.go:1`: Fix f11."+commentFooter),
		"the files past the cap follow the reviewer's own words, which a cut never reaches first: %q", last)
}

func TestPostRequest_PlainSaysWhenTheFinalCommentFailed(t *testing.T) {
	var c PostRequest
	c.Target("acme/w", "42")
	run := &commentsRunner{failAt: 3}
	got, err := c.Post(t.Context(), run, inlineReview, LeadsHeader+"- `a.go:3` drops data\n", DecisionPlain)
	require.ErrorContains(t, err, "posted 2 file comments; the final comment with the unconfirmed blocking leads is not posted")
	require.Equal(t, Posted{URL: "https://github.com/acme/w/pull/42#issuecomment-2", Inline: 2, Decision: DecisionPlain}, got)

	run = &commentsRunner{failAt: 2}
	_, err = c.Post(t.Context(), run, inlineReview, "", DecisionPlain)
	require.ErrorContains(t, err, "the comments on common/go/telemetry/telemetry.go and the final comment are not posted", "names what is still to post")
}

// deadlineRunner records how long each call had.
type deadlineRunner struct{ left []time.Duration }

func (r *deadlineRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	d, ok := ctx.Deadline()
	if !ok {
		return nil, errors.New("no deadline")
	}
	r.left = append(r.left, time.Until(d))
	return []byte("https://github.com/acme/w/pull/42#issuecomment-1"), nil
}

// slowRunner fails like gh killed at its deadline.
type slowRunner struct{}

func (slowRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, errors.New("signal: killed")
}

func TestPostRequest_PlainWarnsATimedOutCommentMayBeUp(t *testing.T) {
	var c PostRequest
	c.Target("acme/w", "42")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := c.Post(ctx, slowRunner{}, inlineReview, "", DecisionPlain)
	require.ErrorIs(t, err, ErrMayBePosted)
}

func TestPostRequest_PlainPostsNothingWhenTheLeadsDoNotFit(t *testing.T) {
	var c PostRequest
	c.Target("acme/w", "42")
	run := &commentsRunner{}
	_, err := c.Post(t.Context(), run, inlineReview, LeadsHeader+strings.Repeat("- `a.go:3` drops data\n", maxCommentBytes/20), DecisionPlain)
	require.ErrorIs(t, err, ErrNoRoom)
	require.Empty(t, run.bodies, "nothing goes up when the leads cannot")
}

func TestPostRequest_PlainGivesEveryCommentItsOwnTime(t *testing.T) {
	var c PostRequest
	c.Target("acme/w", "42")
	run := &deadlineRunner{}
	_, err := c.Post(t.Context(), run, inlineReview, "", DecisionPlain)
	require.NoError(t, err)
	require.Len(t, run.left, 3)
	for _, left := range run.left {
		require.Greater(t, left, PostCallLimit-time.Second, "a call never shares its time with the ones before it")
		require.LessOrEqual(t, left, PostCallLimit)
	}
}

func TestPostRequest_PostsInlineCommentsAsOneReview(t *testing.T) {
	var c PostRequest
	c.Target("acme/w", "42")
	c.SetDiff(inlineDiff)
	run := &inputRunner{out: `{"html_url":"https://github.com/acme/w/pull/42#pullrequestreview-1"}`}
	got, err := c.Post(t.Context(), run, inlineReview, LeadsHeader+"- `a.go:3` drops data\n", "")
	require.NoError(t, err)
	require.Equal(t, Posted{URL: "https://github.com/acme/w/pull/42#pullrequestreview-1", Inline: 2, Decision: DecisionComment}, got)
	require.Equal(t, []string{"api", "--method", "POST", "repos/acme/w/pulls/42/reviews", "--input"}, run.args[:5])
	require.NoFileExists(t, run.args[5], "the request file is removed after the call")

	var req struct {
		Event    string          `json:"event"`
		Body     string          `json:"body"`
		Comments []reviewComment `json:"comments"`
	}
	require.NoError(t, json.Unmarshal(run.input, &req))
	require.Equal(t, "COMMENT", req.Event)
	require.True(t, strings.HasPrefix(req.Body, "Correlation-only"))
	require.Contains(t, req.Body, "Unconfirmed blocking leads")
	require.Equal(t, []reviewComment{
		{Path: "search/serper/main.go", Line: 72, Side: "RIGHT", Body: "Remove the commented-out code."},
		{Path: "common/go/telemetry/telemetry.go", Line: 2, Side: "RIGHT", Body: "Init has no tests. Add focused tests for:\n- Init(ctx, nil)\n- the error path"},
	}, req.Comments, "every comment keeps its own path, line and body: the 422 came from gh mixing them up")
}

// inputRunner records a gh call and the request file it was given with --input, read while the call runs.
type inputRunner struct {
	args  []string
	input []byte
	out   string
}

func (r *inputRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.args = args
	for i, a := range args {
		if a == "--input" && i+1 < len(args) {
			data, err := os.ReadFile(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("reading the request: %w", err)
			}
			r.input = data
		}
	}
	return []byte(r.out), nil
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
	got, err := c.Post(t.Context(), run, inlineReview, "", "")
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
