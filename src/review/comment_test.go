//nolint:err113 // dynamic error messages are okay in tests
package review

import (
	"cmp"
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

// findingsOf splits review into its findings, each named by its place, and the rest, the final comment.
func findingsOf(review string) (findings []string, rest string) {
	var kept []string
	eachFinding(review, func(l string) { kept = append(kept, l) }, func(f postedFinding) { findings = append(findings, Named(f.place, f.text)) })
	return findings, tidy(kept)
}

func TestFindings_ACodeBlockStaysWithItsFinding(t *testing.T) {
	in := "suggestion: search/serper/main.go:72 — Close the body on every path, as in:\n\n" +
		"```go\n# not a heading\nsuggestion: x.go:1 — not a finding\ndefer resp.Body.Close()\n```\n\n" +
		"suggestion: common/go/telemetry/telemetry.go:2 — Add a test for Init(ctx, nil).\n" +
		"  ~~~sh\n  go test ./...\n  ~~~\n\n" +
		"Ship it once the body is closed."
	findings, rest := findingsOf(in)
	require.Equal(t, []string{
		"`search/serper/main.go:72`: Close the body on every path, as in:\n\n" +
			"```go\n# not a heading\nsuggestion: x.go:1 — not a finding\ndefer resp.Body.Close()\n```",
		"`common/go/telemetry/telemetry.go:2`: Add a test for Init(ctx, nil).\n~~~sh\ngo test ./...\n~~~",
	}, findings, "after a blank line or not, and whatever it holds, a code block is the finding's")
	require.Equal(t, "Ship it once the body is closed.", rest)

	findings, rest = findingsOf("nitpick: a.go:1 — Tidy:\n\n```\nnever closed\n\nShip it.")
	require.Len(t, findings, 1)
	require.Contains(t, findings[0], "never closed\n\nShip it.", "an unclosed block runs to the end, as Markdown shows it")
	require.Empty(t, rest)
}

func TestFindings_InlineCodeAndCodeOutsideFindingsAreNoBlocks(t *testing.T) {
	findings, rest := findingsOf("suggestion: a.go:1 — x\n```foo``` bar\n\nShip it.")
	require.Equal(t, []string{"`a.go:1`: x\n```foo``` bar"}, findings, "inline code opens no block")
	require.Equal(t, "Ship it.", rest)

	findings, rest = findingsOf("suggestion: a.go:1 — x\n\nShip it, as in:\n\n    ok\n\n```sh\n# keep this\nsuggestion: b.go:2 — not a finding\n```")
	require.Len(t, findings, 1, "a line in a code block of the final comment is no finding")
	require.Contains(t, rest, "# keep this\nsuggestion: b.go:2 — not a finding")

	findings, _ = findingsOf("suggestion: a.go:1 — Use:\n  - one\n  - two\n```go\nx()\n```")
	require.Equal(t, "`a.go:1`: Use:\n- one\n- two\n```go\nx()\n```", findings[0], "the list and the block are dedented apart")

	require.Equal(t, "Fine.\n```sh\n# a comment\n```", withoutHeadings("### Final comment\nFine.\n```sh\n# a comment\n```"),
		"a # line in a code block is code, not a heading")

	_, rest = findingsOf("suggestion: a.go:1 — x\n\nRun:\n\n```sh\n# step one\n# step two\n\n\nmake\ngo.mod\ngo.sum\n```")
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

func TestSplitFindings_ASuggestionOnARangeCoversTheRange(t *testing.T) {
	review := "suggestion: search/serper/main.go:71-72 — Drop both lines:\n```suggestion\n```\n\n" +
		"suggestion: search/serper/main.go:72-75 — Past the hunk, so on its first line only.\n\n" +
		"suggestion: search/serper/main.go:72-74 — Replace all three:\n```suggestion\nx()\n```\n"
	for _, bad := range []string{"72-0", "72-9999999999999999999999999", "72-70", "72--1", "72-+73", "72–-1", "72-74foo", "72-74.5", "72-74-75",
		"72-74–75", "72-74,75", "72-74;75", "72-74:75", "72– 74foo", "72- 74.5", "72- 74-75"} {
		summary, inline := SplitFindings("suggestion: search/serper/main.go:"+bad+" — Fix:\n```suggestion\nx()\n```", inlineDiff)
		require.Empty(t, inline, "a suggestion on an invalid range never becomes a one-line replacement: %s", bad)
		require.Contains(t, summary, "```\nx()\n```", bad)
	}
	for _, spaced := range []string{"72- 74", "72– 74"} {
		summary, got := SplitFindings("suggestion: search/serper/main.go:"+spaced+" — Fix:\n```suggestion\nx()\n```", inlineDiff)
		require.Empty(t, got, "a dangling range delimiter is still a range: %s", spaced)
		require.Equal(t, "`search/serper/main.go:72-74`: Fix:\n```\nx()\n```", summary, "its end is the number after the space, not the text")
	}
	for in, want := range map[string]InlineComment{
		"suggestion: search/serper/main.go:71-72: Replace:\n```suggestion\nx()\n```": {Path: "search/serper/main.go", StartLine: 71, Line: 72, Body: "Replace:\n```suggestion\nx()\n```"},
		"suggestion: search/serper/main.go:71-72—Drop the call.":                     {Path: "search/serper/main.go", StartLine: 71, Line: 72, Body: "Drop the call."},
		"suggestion: search/serper/main.go:72–Close the body.":                       {Path: "search/serper/main.go", Line: 72, Body: "Close the body."},
		"suggestion: search/serper/main.go:72- Close the body.":                      {Path: "search/serper/main.go", Line: 72, Body: "Close the body."},
	} {
		_, got := SplitFindings(in, inlineDiff)
		require.Equal(t, []InlineComment{want}, got, "the separator and the text keep their place: %s", in)
	}
	for _, loose := range []string{"72foo", "72.5", "72,74", "72- +74", "72– +74", "72- 74 -75"} {
		for _, fence := range []string{"```suggestion", "```suggestion:-0+0", "~~~ Suggestion:-1+1"} {
			summary, got := SplitFindings("suggestion: search/serper/main.go:"+loose+" — Fix:\n"+fence+"\nx()\n"+fence[:3], inlineDiff)
			require.Empty(t, got, "a loosely written place never carries an applicable suggestion: %s %s", loose, fence)
			require.NotContains(t, strings.ToLower(summary), "suggestion", "it is plain code: %s %s", loose, fence)
		}
	}
	summary, open := SplitFindings("suggestion: search/serper/main.go:72 — Fix:\n```suggestion\nx()", inlineDiff)
	require.Empty(t, open, "a suggestion never closed is never applicable: the footer would land in the author's code")
	require.Equal(t, "`search/serper/main.go:72`: Fix:\n```\nx()", summary)
	for _, closing := range []string{"    ```", "\t```", "```\u00a0", "\u00a0```", "```\u2003", "\v```"} {
		_, got := SplitFindings("suggestion: search/serper/main.go:72 — Fix:\n```suggestion\nx()\n"+closing+"\nAfter.", inlineDiff)
		require.Empty(t, got, "a closing fence indented four columns is code, so the block is still open: %q", closing)
	}
	for _, offset := range []string{"```suggestion:-1+1", "```suggestion:-1+0", "```suggestion:-0+1"} {
		_, got := SplitFindings("suggestion: search/serper/main.go:72 — Fix:\n"+offset+"\nx()\n```", inlineDiff)
		require.Empty(t, got, "a suggestion with an offset reaches lines the finding did not cite: %s", offset)
	}
	plain := "Use:\n```\nsuggestion := pick()\nreturn suggestion\n```"
	require.Equal(t, "`a.go:1`: "+plain, Named("a.go:1", plain), "a plain block whose code starts with suggestion is left whole")
	require.False(t, hasSuggestion(plain))
	for _, kept := range []string{"Use: ```suggestion``` is inline code.", "Use:\n````md\n```suggestion\nx()\n```\n````"} {
		require.Equal(t, "`a.go:1`: "+kept, Named("a.go:1", kept), "inline code and quoted examples are left as written")
		require.False(t, hasSuggestion(kept), kept)
	}
	_, closed := SplitFindings("suggestion: search/serper/main.go:72 — Fix:\n```suggestion\nx()\n   ```", inlineDiff)
	require.Len(t, closed, 1, "three columns of indent still close it")
	require.Equal(t, "See:\n```go\nx()\n```", closeFence("See:\n```go\nx()"))
	require.Equal(t, "Fine.", closeFence("Fine."))
	for _, fence := range []string{"```suggestion:-0+0", "```suggestion:-1+1"} {
		require.NotContains(t, fitComment("Fix:\n"+fence+"\n"+strings.Repeat("x()\n", 50)+"```", 40), "suggestion", fence)
		require.Equal(t, "`a.go:1`: Fix:\n```\nx()\n```", Named("a.go:1", "Fix:\n"+fence+"\nx()\n```"), fence)
	}
	_, three := SplitFindings("suggestion: search/serper/main.go:72- 3 callers break.", inlineDiff)
	require.Equal(t, "3 callers break.", three[0].Body, "a number below the line is the text, not a range end")
	_, year := SplitFindings("suggestion: search/serper/main.go:72- 2024 migrations break.", inlineDiff)
	require.Equal(t, "2024 migrations break.", year[0].Body, "a number with no separator after it is the text")
	_, verbatim := SplitFindings("suggestion: search/serper/main.go:72 — Keep the spaces:\n```suggestion\nv := `keep  \nnext`\n```", inlineDiff)
	require.Equal(t, "Keep the spaces:\n```suggestion\nv := `keep  \nnext`\n```", verbatim[0].Body, "a suggestion's lines are applied as written, trailing spaces too")
	_, single := SplitFindings("suggestion: search/serper/main.go:72-72 — Fix:\n```suggestion\nx()\n```", inlineDiff)
	require.Equal(t, []InlineComment{{Path: "search/serper/main.go", Line: 72, Body: "Fix:\n```suggestion\nx()\n```"}}, single, "a range of one line is that line")
	summary, inline := SplitFindings(review, inlineDiff)
	require.Equal(t, "`search/serper/main.go:72-74`: Replace all three:\n```\nx()\n```", summary,
		"a suggestion for a range the diff does not show whole stays off the lines, as plain code: on line 72 alone it would rewrite the wrong code")
	require.Equal(t, []InlineComment{
		{Path: "search/serper/main.go", StartLine: 71, Line: 72, Body: "Drop both lines:\n```suggestion\n```"},
		{Path: "search/serper/main.go", Line: 72, Body: "Past the hunk, so on its first line only."},
	}, inline, "a range the diff shows is one comment over it, so its suggestion replaces the whole range")
	require.Equal(t, reviewComment{Path: "search/serper/main.go", Line: 72, Side: "RIGHT", StartLine: 71, StartSide: "RIGHT", Body: inline[0].Body}, commentOn(inline[0]))
	require.Equal(t, "search/serper/main.go:71-72", inline[0].Place())
}

func TestFindings_ACodeBlockAfterTheLastFindingIsItsExample(t *testing.T) {
	// Pinned: a block that directly follows a finding is that finding's, even when it is the last one; the final
	// comment is plain text (codeforge-review skill, section 7).
	findings, rest := findingsOf("suggestion: a.go:1 — Close it, as in:\n\n```go\ndefer f.Close()\n```\n\nShip it.")
	require.Contains(t, findings[0], "defer f.Close()")
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

func TestExposureLines_NamesEveryLineThatLogsAWholeValue(t *testing.T) {
	diff := "diff --git a/search/serper/main.go b/search/serper/main.go\n--- a/search/serper/main.go\n+++ b/search/serper/main.go\n@@ -139,2 +139,4 @@\n" +
		" \t}\n" +
		"+\tzaplog.Context(ctx).Info(\"grpc server created\", zap.Any(\"config\", configuration))\n" +
		"+\tzaplog.Context(ctx).Info(\"started\", zap.String(\"port\", configuration.Port))\n" +
		"+\tlog.Printf(\"cfg %+v\", cfg)\n" +
		"+\tlog.Info().Interface(\"cfg\", cfg).Msg(\"start\")\n" +
		"+\treturn fmt.Errorf(\"loading: %+v\", err)\n" +
		"diff --git a/search/serper/main_test.go b/search/serper/main_test.go\n--- a/x\n+++ b/search/serper/main_test.go\n@@ -1 +1 @@\n+\tt.Logf(\"%+v\", got)\n"
	require.Equal(t, []string{
		`search/serper/main.go:140: zaplog.Context(ctx).Info("grpc server created", zap.Any("config", configuration))`,
		`search/serper/main.go:142: log.Printf("cfg %+v", cfg)`,
		`search/serper/main.go:143: log.Info().Interface("cfg", cfg).Msg("start")`,
	}, ExposureLines(diff), "a single safe field and a wrapped error are fine, and tests are left out")
	require.Contains(t, auditSection(diff), "<check_what_is_logged>")
	require.Contains(t, auditSection(diff), "CWE-532")
}

func TestStripPreamble(t *testing.T) {
	// From real runs on ecosia/core#32569.
	for _, preamble := range []string{
		"I found no blocking issues. The review is below, written in your voice, with the inline comments first.",
		"I found two blocking issues. Details are below.",
		"Here is the review.",
	} {
		require.Equal(t, "suggestion: a.go:1 — Use x.\n\nFine.", StripPreamble(preamble+"\n\nsuggestion: a.go:1 — Use x.\n\nFine."), preamble)
	}
	for _, kept := range []string{
		"The retry below sends the payment twice after a timeout. Remove it before merging.\n\nsuggestion: a.go:1 — Use x.",
		"I checked the rollout: old clients cannot read the new records. Deploy the reader first.\n\nsuggestion: a.go:1 — Use x.",
		"I checked the rollout: old clients cannot read the new records.\n\nsuggestion: a.go:1 — Use x.",
		"I reviewed the migration and it drops the users table.\n\nsuggestion: a.go:1 — Use x.",
		"I found that retries send payments twice.\n\nsuggestion: a.go:1 — Use x.",
		"The review is below, and the migration drops the users table.\n\nsuggestion: a.go:1 — Use x.",
		"Here is my review: the retry sends the payment twice.\n\nsuggestion: a.go:1 — Use x.",
		"Written in your voice: the startup log writes SERPER_API_KEY.\n\nsuggestion: a.go:1 — Use x.",
		"The review is below; deploy the reader before the writer.\n\nsuggestion: a.go:1 — Use x.",
		"The migration drops all users, written in your voice.\n\nsuggestion: a.go:1 — Use x.",
		"I found two blocking issues: the startup log writes `SERPER_API_KEY` in plain text. Details are below.\n\nsuggestion: a.go:1 — Use x.",
		"The retry sends the payment twice. Details are below.\n\nsuggestion: a.go:1 — Use x.",
		"Moves tracing behind one seam.\n\nsuggestion: a.go:1 — Use x.",                    // words to the author
		"I found no blocking issues here; this review is below the bar for comments.",      // no findings after it
		"suggestion: a.go:1 — Use x, as in the draft below.\n\nsuggestion: b.go:2 — Tidy.", // a finding itself
		"The review is below.\n\n```\nsuggestion: a.go:1 — not a finding\n```",             // only code after it
	} {
		require.Equal(t, kept, StripPreamble(kept))
	}
}

func TestStripVerdict(t *testing.T) {
	for _, in := range []string{"Verdict: Comment. Fine.", "**Verdict:** Comment. Fine.", "1. **Verdict:** Request changes — Fine.", "Verdict: Approve\n\nFine."} {
		require.Equal(t, "Fine.", StripVerdict(in), in)
	}
	require.Equal(t, "The verdict is pending.", StripVerdict("The verdict is pending."))
}

// plainCall is one comment a plain post made: on a line (Path set) or on the PR.
type plainCall struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	CommitID string `json:"commit_id"`
	Side     string `json:"side"`
	Body     string `json:"body"`
}

// plainRunner fakes gh for a plain post: it answers the head lookup, records every comment, and fails the failAt-th.
type plainRunner struct {
	calls    []plainCall
	heads    int
	tries    int
	failAt   int    // the 1-based comment attempt that fails, 0 for none
	failWith string // how a line comment fails; "HTTP 422", GitHub refusing the line, when empty
}

func (r *plainRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	switch {
	case len(args) > 1 && args[0] == "pr" && args[1] == "view":
		r.heads++
		return []byte("headsha\n"), nil
	case len(args) > 5 && args[0] == "api" && args[3] == "repos/acme/w/pulls/42/comments" && args[4] == "--input":
		r.tries++
		if r.tries == r.failAt {
			return nil, errors.New(cmp.Or(r.failWith, "gh: Unprocessable Entity (HTTP 422)"))
		}
		data, err := os.ReadFile(args[5])
		if err != nil {
			return nil, fmt.Errorf("reading the request: %w", err)
		}
		var c plainCall
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("decoding the request: %w", err)
		}
		r.calls = append(r.calls, c)
		return fmt.Appendf(nil, `{"html_url":"https://github.com/acme/w/pull/42#discussion_r%d"}`, len(r.calls)), nil
	case len(args) > 5 && args[0] == "pr" && args[1] == "comment":
		r.tries++
		if r.tries == r.failAt {
			return nil, errors.New("HTTP 502")
		}
		r.calls = append(r.calls, plainCall{Body: strings.TrimPrefix(args[5], "--body=")})
		return fmt.Appendf(nil, "https://github.com/acme/w/pull/42#issuecomment-%d\n", len(r.calls)), nil
	}
	return nil, fmt.Errorf("unexpected gh call: %v", args)
}

// plainRequest is a plain post to acme/w#42 with the diff of inlineReview.
func plainRequest(head string) *PostRequest {
	c := &PostRequest{}
	c.Target("acme/w", "42")
	c.SetDiff(inlineDiff)
	c.SetHead(head)
	return c
}

func TestPostRequest_PlainPutsEachFindingOnItsLineThenTheFinalComment(t *testing.T) {
	run := &plainRunner{}
	got, err := plainRequest("abc123").Post(t.Context(), run, inlineReview, "", DecisionPlain)
	require.NoError(t, err)
	require.Equal(t, Posted{URL: "https://github.com/acme/w/pull/42#issuecomment-3", Inline: 2, Decision: DecisionPlain, Final: true}, got)
	require.Zero(t, run.heads, "the head from the brief is used")
	require.Len(t, run.calls, 3, "a comment on each finding's line, then the final comment")
	require.Equal(t, plainCall{Path: "search/serper/main.go", Line: 72, CommitID: "abc123", Side: "RIGHT", Body: "Remove the commented-out code."}, run.calls[0],
		"a comment on its line, so GitHub shows the code above it, and without its label")
	require.Equal(t, plainCall{Path: "common/go/telemetry/telemetry.go", Line: 2, CommitID: "abc123", Side: "RIGHT",
		Body: "Init has no tests. Add focused tests for:\n- Init(ctx, nil)\n- the error path"}, run.calls[1])
	final := run.calls[2]
	require.Empty(t, final.Path, "the final comment is an ordinary PR comment")
	require.True(t, strings.HasPrefix(final.Body, "Correlation-only"), "the final comment comes last: %q", final.Body)
	require.Contains(t, final.Body, "`search/serper/main.go:300`: Off the diff, so it stays in the summary.", "a finding off the diff goes in it")
	require.NotContains(t, final.Body, "main.go:72", "findings on lines are not repeated in it")
	require.NotContains(t, final.Body, "###", "without headings")
	require.True(t, strings.HasSuffix(final.Body, commentFooter))
	require.NotContains(t, run.calls[0].Body, "Codeforge", "the footer is said once")
}

func TestPostRequest_PlainAsksGitHubForTheHeadWhenTheBriefHadNone(t *testing.T) {
	run := &plainRunner{}
	_, err := plainRequest("").Post(t.Context(), run, inlineReview, "", DecisionPlain)
	require.NoError(t, err)
	require.Equal(t, 1, run.heads)
	require.Equal(t, "headsha", run.calls[0].CommitID)
}

func TestPostRequest_PlainSaysHowFarItGot(t *testing.T) {
	run := &plainRunner{failAt: 2}
	got, err := plainRequest("abc123").Post(t.Context(), run, inlineReview, "", DecisionPlain)
	require.NoError(t, err)
	require.Equal(t, 1, got.Inline)
	require.Len(t, got.Refused, 1)
	require.True(t, got.Final)
	require.Contains(t, run.calls[len(run.calls)-1].Body, "---\n\n`common/go/telemetry/telemetry.go:2`: Init has no tests.",
		"a finding GitHub will not put on its line joins the final comment, named by its place")

	run = &plainRunner{}
	got, err = plainRequest("abc123").Post(t.Context(), run, "suggestion: search/serper/main.go:72 — Use x.\n\nDecision: Comment", "", DecisionPlain)
	require.NoError(t, err)
	require.False(t, got.Final)
	require.Len(t, run.calls, 1)
	require.Equal(t, "Use x."+commentFooter, run.calls[0].Body, "with no final comment, the last line comment carries the footer")

	run = &plainRunner{}
	_, err = plainRequest("abc123").Post(t.Context(), run, "Decision: Comment", "", DecisionPlain)
	require.ErrorIs(t, err, ErrNoReviewText, "an empty review posts nothing, not a bare footer")
	require.Empty(t, run.calls)
}

func TestPostRequest_PlainFoldsFindingsPastTheCapIntoTheFinalComment(t *testing.T) {
	var review, diff strings.Builder
	for i := range MaxPlainLineComments + 2 {
		fmt.Fprintf(&review, "suggestion: f%02d.go:1 — Fix f%02d.\n", i, i)
		fmt.Fprintf(&diff, "diff --git a/f%02d.go b/f%02d.go\n--- a/f%02d.go\n+++ b/f%02d.go\n@@ -0,0 +1 @@\n+x\n", i, i, i, i)
	}
	review.WriteString("\nShip it after the fixes.\n")
	c := plainRequest("abc123")
	c.SetDiff(diff.String())
	run := &plainRunner{}
	got, err := c.Post(t.Context(), run, review.String(), "", DecisionPlain)
	require.NoError(t, err)
	require.Equal(t, MaxPlainLineComments, got.Inline)
	require.Len(t, run.calls, MaxPlainLineComments+1)
	last := run.calls[MaxPlainLineComments].Body
	want := fmt.Sprintf("Ship it after the fixes.\n\n---\n\n`f%02d.go:1`: Fix f%02d.\n\n`f%02d.go:1`: Fix f%02d.", MaxPlainLineComments, MaxPlainLineComments, MaxPlainLineComments+1, MaxPlainLineComments+1)
	require.True(t, strings.HasPrefix(last, want+commentFooter),
		"the findings past the cap follow the reviewer's own words, which a cut never reaches first: %q", last)
}

func TestPostRequest_PlainStopsOnAnErrorThatIsNoRefusal(t *testing.T) {
	run := &plainRunner{failAt: 2, failWith: "HTTP 502"}
	got, err := plainRequest("abc123").Post(t.Context(), run, inlineReview, "", DecisionPlain)
	require.ErrorContains(t, err, "posted 1 line comment; the comments on common/go/telemetry/telemetry.go:2 and the final comment are not posted",
		"a 502 may have created the comment, so it is not posted again in the final one")
	require.Empty(t, got.Refused)
	require.Len(t, run.calls, 1)
}

func TestPostLineComment_ReadsARefusalFromRealGhOutput(t *testing.T) {
	// gh's own wording for a 422, as it printed it on ecosia/core#32569.
	dir := t.TempDir()
	gh := "#!/bin/sh\necho 'gh: Unprocessable Entity (HTTP 422)' >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(dir+"/gh", []byte(gh), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := postLineComment(t.Context(), ExecRunner{}, "acme/w", "42", "abc", InlineComment{Path: "a.go", Line: 1, Body: "x"})
	require.ErrorIs(t, err, errRefused)

	require.NoError(t, os.WriteFile(dir+"/gh", []byte("#!/bin/sh\necho 'gh: Bad Gateway (HTTP 502)' >&2\nexit 1\n"), 0o700))
	_, err = postLineComment(t.Context(), ExecRunner{}, "acme/w", "42", "abc", InlineComment{Path: "a.go", Line: 1, Body: "x"})
	require.Error(t, err)
	require.NotErrorIs(t, err, errRefused)
}

func TestFitComment_ACutSuggestionIsNoLongerOne(t *testing.T) {
	body := "Replace:\n```suggestion\n" + strings.Repeat("x()\n", 50) + "```"
	got := fitComment(body, 60)
	require.NotContains(t, got, "suggestion", "an incomplete replacement must never be one click away")
	require.Contains(t, got, cutNote)
	require.Equal(t, body, fitComment(body, len(body)), "one that fits is kept")
}

func TestPostRequest_PlainSaysWhenTheFinalCommentFailed(t *testing.T) {
	run := &plainRunner{failAt: 3}
	got, err := plainRequest("abc123").Post(t.Context(), run, inlineReview, LeadsHeader+"- `a.go:3` drops data\n", DecisionPlain)
	require.ErrorContains(t, err, "posted 2 line comments; the final comment with the unconfirmed blocking leads is not posted")
	require.Equal(t, Posted{URL: "https://github.com/acme/w/pull/42#discussion_r2", Inline: 2, Decision: DecisionPlain}, got)
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
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := plainRequest("abc123").Post(ctx, slowRunner{}, inlineReview, "", DecisionPlain)
	require.ErrorIs(t, err, ErrMayBePosted)
}

func TestPostRequest_PlainPostsNothingWhenTheLeadsDoNotFit(t *testing.T) {
	run := &plainRunner{}
	_, err := plainRequest("abc123").Post(t.Context(), run, inlineReview, LeadsHeader+strings.Repeat("- `a.go:3` drops data\n", maxCommentBytes/20), DecisionPlain)
	require.ErrorIs(t, err, ErrNoRoom)
	require.Empty(t, run.calls, "nothing goes up when the leads cannot")
}

func TestPostRequest_PlainGivesEveryCommentItsOwnTime(t *testing.T) {
	run := &deadlineRunner{}
	_, err := plainRequest("abc123").Post(t.Context(), run, inlineReview, "", DecisionPlain)
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
