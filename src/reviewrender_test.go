package src

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
)

const sampleReview = "Moves tracing behind one seam.\n\n" +
	"### search/serper/main.go\n\n" +
	"suggestion: search/serper/main.go:108 — `kit.InstrumentHTTPClient` sends **baggage** to the vendor.\n" +
	"  Drop propagation for vendor clients.\n\n" +
	"- **nitpick (non-blocking):** `search/serper/main.go:72` — Nit: remove the commented-out code.\n\n" +
	"```go\n\t// gprclogging.PayloadReceived,\n```\n\n" +
	"### Across files\n\n" +
	"- Split the unrelated `go.mod` bumps into their own PR.\n"

func fakeCode(file string, fc focus) string {
	line := fc.Line
	return "    CODE " + file + ":" + strings.Repeat("|", line%3) + "\n"
}

func TestRenderReview_Plain(t *testing.T) {
	got := renderReview(sampleReview, codeStyle{wrap: true, width: 60}, fakeCode)
	require.NotContains(t, got, "###")
	require.NotContains(t, got, "`")
	require.NotContains(t, got, "**")
	require.NotContains(t, got, "\x1b[", "no colour without a colour terminal")
	require.NotContains(t, got, "\n\n\n")
	require.Contains(t, got, "\nsearch/serper/main.go\n")
	require.Contains(t, got, "\n    CODE search/serper/main.go:\n\n  suggestion: kit.InstrumentHTTPClient sends baggage to the\n  vendor.\n  Drop propagation for vendor clients.\n",
		"the code comes first, then the comment without repeating the place")
	require.Contains(t, got, "- nitpick (non-blocking): search/serper/main.go:72 Nit:\n  remove the commented-out code.\n\n      | // gprclogging.PayloadReceived,\n",
		"a finding that quotes its code gets no second copy, and its code sits under it")
	require.Equal(t, 1, strings.Count(got, "CODE "))
	require.Contains(t, got, "- Split the unrelated go.mod bumps into their own PR.\n")
	for _, l := range strings.Split(got, "\n") {
		require.LessOrEqual(t, len([]rune(l)), 60, "wrapped to the width: %q", l)
	}
}

func TestRenderReview_KeepsPunctuationAgainstInlineCode(t *testing.T) {
	got := renderReview("Spreads to `codeforge respond`; see **this**, and `a`/`b`.", codeStyle{}, nil)
	require.Equal(t, "Spreads to codeforge respond; see this, and a/b.\n", got)
}

func TestRenderReview_SteelColours(t *testing.T) {
	st := codeStyle{color: true, wrap: true, width: 100, pal: paletteFor("dark")}
	got := renderReview("### a/b.go\n\nissue (blocking): a/b.go:3 — drops `rows`\nquestion: a/b.go:4 — why?\n\n```go\nfunc f() {}\n```\n", st, nil)
	require.Contains(t, got, st.pal.accent+"a/b.go"+ansiReset+"\n", "a file heading is in the accent")
	require.Contains(t, got, st.pal.issue+"issue")
	require.Contains(t, got, st.pal.question+"question:")
	require.Contains(t, got, st.pal.inline+"rows"+ansiReset)
	require.Contains(t, got, "\x1b[1m\x1b[38;2;126;168;201mfunc", "fenced code is highlighted in Steel")
}

const styledEngine = `echo "memory.embedder: Failed to wire" >&2; printf '%s' '{"output":"suggestion: a.go:1 — use x\nnitpick: a.go:2 — tidy\n\nAdds x as the description says. Fine once x is used.\n\nDecision: Approve","steps":3,"cost":0.5,"duration":2000}'`

// onTerminal pretends standard output is a terminal and says whether the accordion helper is there.
func onTerminal(t *testing.T, accordion bool) {
	t.Helper()
	oldStyled, oldAvail := stdoutStyled, accordionAvailable
	stdoutStyled = func() bool { return true }
	accordionAvailable = func() bool { return accordion }
	t.Cleanup(func() { stdoutStyled, accordionAvailable = oldStyled, oldAvail })
}

// captured runs fn with standard output and error going to pipes and returns what each received.
func captured(t *testing.T, fn func() error) (stdout, stderr string, err error) {
	t.Helper()
	r, w, perr := os.Pipe()
	require.NoError(t, perr)
	er, ew, perr := os.Pipe()
	require.NoError(t, perr)
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, ew
	read := func(f *os.File, into chan<- string) {
		data, rerr := io.ReadAll(f)
		if rerr != nil {
			data = append(data, rerr.Error()...)
		}
		into <- string(data)
	}
	outC, errC := make(chan string), make(chan string)
	go read(r, outC)
	go read(er, errC)
	err = fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	require.NoError(t, w.Close())
	require.NoError(t, ew.Close())
	return <-outC, <-errC, err
}

func TestRunReviewEngine_PrintsTheStyledReviewWithoutTheHelper(t *testing.T) {
	f := newPostFixture(t, styledEngine, 0)
	onTerminal(t, false)
	out, stderr, err := captured(t, func() error {
		return runReview(reviewContext(t, false), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
	})
	require.NoError(t, err)
	require.NotContains(t, stderr, "memory.embedder", "the engine's warnings are held back when the run succeeds")
	args, err := os.ReadFile(f.engineArgs)
	require.NoError(t, err)
	require.Contains(t, string(args), "--json", "the review is read as JSON to be laid out")
	require.NotContains(t, out, "Decision:")
	require.Contains(t, out, "- suggestion: a.go:1 use x\n")
	require.Contains(t, out, "Adds x as the description says.")
	require.Contains(t, out, "suggested: Approve")
	require.Contains(t, out, "3 steps, $0.5000, 2.0s")
	require.NoFileExists(t, f.ghArgs, "showing a review never posts it")
}

// pressing makes the review view answer as if the user pressed key (err for Ctrl+C), and records what it showed.
func pressing(t *testing.T, key string, err error) *forms.Accordion {
	t.Helper()
	shown := &forms.Accordion{}
	old := showAccordion
	showAccordion = func(_ string, a forms.Accordion) (string, error) { *shown = a; return key, err }
	t.Cleanup(func() { showAccordion = old })
	return shown
}

// postingContext is a review that may post on acme/w#42 after asking.
func postingContext(t *testing.T) context.Context {
	t.Helper()
	post := &review.PostRequest{}
	post.Target("acme/w", "42")
	return review.WithDisplay(review.WithPost(review.WithDeadline(t.Context(), time.Now().Add(time.Minute)), post), &review.Display{})
}

func TestRunReviewEngine_TheUserPicksTheDecisionThatIsPosted(t *testing.T) {
	f := newPostFixture(t, styledEngine, 0)
	onTerminal(t, true)
	shown := pressing(t, "3", nil) // Request changes, though the review suggests Approve
	var chat bool
	out, _, err := captured(t, func() error {
		var err error
		chat, err = runReviewEngine(postingContext(t), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
		return err
	})
	require.NoError(t, err)
	require.False(t, chat, "posting does not open the chat")
	require.Empty(t, shown.Intro)
	require.Len(t, shown.Items, 2)
	require.Equal(t, "a.go", shown.Items[0].Title)
	require.Equal(t, "1 suggestion · 1 nitpick", shown.Items[0].Summary)
	require.Contains(t, shown.Items[0].Body, "- suggestion: a.go:1 use x")
	require.Equal(t, "Final comment", shown.Items[1].Title)
	require.Equal(t, "suggested: Approve", shown.Items[1].Summary)
	require.True(t, shown.Items[1].Open, "the final comment is open at the start")
	require.Contains(t, shown.Items[1].Body, "Adds x as the description says.")
	require.Equal(t, []forms.Action{
		{Key: "1", Label: "post as Approve (suggested)"}, {Key: "2", Label: "post as Comment"}, {Key: "3", Label: "post as Request changes"}, {Key: "4", Label: "post as separate comments (deletable)"},
		{Key: "c", Label: "chat about it"},
	}, shown.Keys)
	require.Contains(t, out, "  a.go  1 suggestion · 1 nitpick\n", "a recap stays in the terminal")
	posted, err := os.ReadFile(f.ghArgs)
	require.NoError(t, err)
	require.Contains(t, string(posted), `"event":"REQUEST_CHANGES"`, "the user's pick is posted, not the suggestion")
	require.Contains(t, string(posted), "Adds x as the description says.")
}

func TestRunReviewEngine_APartialPlainPostSaysWhatIsOnThePR(t *testing.T) {
	f := newPostFixture(t, styledEngine, 0)
	// gh tells who is signed in, posts the first comment and fails on the next.
	gh := "#!/bin/sh\nif [ \"$1\" = api ] && [ \"$2\" = user ]; then echo me; exit 0; fi\nif [ -e \"" + f.ghArgs + "\" ]; then echo 'HTTP 502' >&2; exit 1; fi\ntouch \"" + f.ghArgs + "\"\necho '{\"html_url\":\"https://github.com/acme/w/pull/42#discussion_r1\"}'\n"
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(f.ghArgs), "gh"), []byte(gh), execPerm))
	onTerminal(t, true)
	pressing(t, "4", nil)
	_, _, err := captured(t, func() error {
		_, err := runReviewEngine(linesContext(t), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
		return err
	})
	require.ErrorContains(t, err, "only part of the review was posted, up to https://github.com/acme/w/pull/42#discussion_r1")
	require.ErrorContains(t, err, "posted 1 line comment; the comments on a.go:2 and the final comment are not posted",
		"a 502 is no refusal: it may have created the comment, so nothing more is posted")
	require.NotContains(t, err.Error(), "the review above was not posted")
}

func TestPostBound_PlainHasRoomForEveryComment(t *testing.T) {
	require.Equal(t, postLimit, postBound(review.DecisionComment))
	require.Equal(t, review.PostCallLimit*(review.MaxPlainLineComments+plainOtherCalls), postBound(review.DecisionPlain))
}

// linesContext is postingContext for a PR whose diff shows lines 1 and 2 of a.go, at head abc123.
func linesContext(t *testing.T) context.Context {
	t.Helper()
	post := &review.PostRequest{}
	post.Target("acme/w", "42")
	post.SetDiff("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -0,0 +1,2 @@\n+x\n+y\n")
	post.SetHead("abc123")
	return review.WithDisplay(review.WithPost(review.WithDeadline(t.Context(), time.Now().Add(time.Minute)), post), &review.Display{})
}

func TestRunReviewEngine_FourPostsDeletableLineCommentsAndNoReview(t *testing.T) {
	f := newPostFixture(t, styledEngine, 0)
	onTerminal(t, true)
	pressing(t, "4", nil)
	_, stderr, err := captured(t, func() error {
		_, err := runReviewEngine(linesContext(t), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
		return err
	})
	require.NoError(t, err)
	posted, err := os.ReadFile(f.ghArgs)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(posted), "api --method POST repos/acme/w/pulls/42/comments --input "), "a comment on each finding's line: %s", posted)
	require.Contains(t, string(posted), `{"path":"a.go","line":1,"side":"RIGHT","body":"use x","commit_id":"abc123"}`, "on its line, without its label")
	require.Contains(t, string(posted), `{"path":"a.go","line":2,"side":"RIGHT","body":"tidy","commit_id":"abc123"}`)
	require.Contains(t, string(posted), "pr comment 42 --repo acme/w --body=Adds x as the description says.", "then the final comment")
	require.NotContains(t, string(posted), "/reviews", "no review")
	require.Contains(t, stderr, "posted the review as separate comments you can delete one by one: 2 line comments and the final comment")
}

func TestRunReviewEngine_OnlyCOpensTheChat(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		err  error
		chat bool
	}{
		{"c", keyChat, nil, true},
		{"q", "", nil, false},
		{"ctrl+c", "", forms.ErrAborted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPostFixture(t, styledEngine, 0)
			onTerminal(t, true)
			pressing(t, tc.key, tc.err)
			var chat bool
			out, _, err := captured(t, func() error {
				var err error
				chat, err = runReviewEngine(postingContext(t), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
				return err
			})
			require.NoError(t, err)
			require.Equal(t, tc.chat, chat)
			require.Contains(t, out, "a.go", "the recap stays in the terminal")
			require.NoFileExists(t, f.ghArgs, "nothing is posted")
		})
	}
}

func TestRunReviewEngine_OtherModesPassThroughAndOpenTheChat(t *testing.T) {
	f := newPostFixture(t, `printf '%s' 'task done'`, 0)
	onTerminal(t, true)
	chat, err := runReviewEngine(t.Context(), []string{f.engine}, os.Environ(), time.Time{})
	require.NoError(t, err)
	require.True(t, chat, "chat, work and ticket always continue in the chat")
	args, err := os.ReadFile(f.engineArgs)
	require.NoError(t, err)
	require.NotContains(t, string(args), "--json", "a task outside review mode is never laid out as a review")
}

func TestReviewAccordion_GroupsFindingsByFileAndKeepsTheRestAsTheFinalComment(t *testing.T) {
	text := "suggestion: b.go:4 — first in b\n" +
		"issue (blocking): a.go:9 — breaks a\n" +
		"nitpick (non-blocking): b.go:7 — Nit: tidy\n" +
		"  and a second line\n\n" +
		"### a heading a model added anyway\n\n" +
		"Adds the retry the description promises, but also changes the timeout it does not mention.\n\nRequest changes until a.go is fixed.\n"
	a := reviewAccordion(text, "**Unconfirmed blocking leads.** Check:\n- `x.go:1` drops data\n", codeStyle{wrap: true, width: 80}, nil)
	require.Empty(t, a.Intro)
	titles := make([]string, len(a.Items))
	for i, s := range a.Items {
		titles[i] = s.Title
	}
	require.Equal(t, []string{"b.go", "a.go", "Final comment", "Unconfirmed blocking leads"}, titles, "files in the order they first come up")
	require.Equal(t, "1 suggestion · 1 nitpick", a.Items[0].Summary, "a file's findings are together wherever they were written")
	require.Contains(t, a.Items[0].Body, "and a second line")
	require.Equal(t, "1 issue", a.Items[1].Summary)
	require.Contains(t, a.Items[2].Body, "Adds the retry the description promises")
	require.NotContains(t, a.Items[2].Body, "heading", "headings are dropped")
	require.True(t, a.Items[2].Open)
	require.Contains(t, a.Items[3].Body, "x.go:1 drops data")

	only := reviewAccordion("Just prose.", "", codeStyle{}, nil)
	require.Equal(t, "Final comment", only.Items[0].Title, "a review with no findings is its final comment")
}

var errNotOnGitHub = errors.New("HTTP 404")

// codeCalls records the git and gh calls reviewCode makes and answers each with a ten-line file.
type codeCalls struct {
	mu    sync.Mutex
	calls []string
}

func (c *codeCalls) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name+" "+strings.Join(args, " "))
	if strings.Contains(strings.Join(args, " "), "missing.go") {
		return nil, errNotOnGitHub
	}
	return []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"), nil
}

func useCodeRunner(t *testing.T) *codeCalls {
	t.Helper()
	c := &codeCalls{}
	old := codeRunner
	codeRunner = c
	t.Cleanup(func() { codeRunner = old })
	return c
}

const pathsReview = "suggestion: a/b.go:5 — x\nnitpick: a/b.go:6 — y\nissue: dir/ü.go:2 — z\nfyi: missing.go:1 — w\nquestion: ../escape.go:1 — v\n"

func TestReviewCode_ReadsWhereTheReviewSays(t *testing.T) {
	st := codeStyle{width: 80}
	require.Nil(t, reviewCode(t.Context(), st, pathsReview), "outside a review there is nothing to read")
	d := &review.Display{}
	ctx := review.WithDisplay(context.Background(), d)
	d.PR("", "", "")
	require.Nil(t, reviewCode(ctx, st, pathsReview), "a PR with neither a fetched head nor a commit")
	d.Local()
	require.NotNil(t, reviewCode(ctx, st, pathsReview))
}

func TestReviewCode_ReadsAFetchedHeadWithGit(t *testing.T) {
	calls := useCodeRunner(t)
	d := &review.Display{}
	d.PR("refs/codeforge/pr-1", "acme/core", "abc123")
	code := reviewCode(review.WithDisplay(t.Context(), d), codeStyle{width: 80}, pathsReview)
	require.Contains(t, code("a/b.go", focus{Line: 5}), "> 5 | l5")
	require.Contains(t, calls.calls, "git show refs/codeforge/pr-1:a/b.go")
	require.Len(t, calls.calls, 3, "each file once (a/b.go, dir/ü.go, missing.go), never a path outside the repository")
}

func TestReviewCode_ReadsAPRFromAnotherCheckoutOnGitHub(t *testing.T) {
	calls := useCodeRunner(t)
	d := &review.Display{}
	d.PR("", "ecosia/core", "abc123")
	code := reviewCode(review.WithDisplay(t.Context(), d), codeStyle{width: 80}, pathsReview)
	require.Contains(t, code("a/b.go", focus{Line: 6}), ">  6 | l6")
	require.Contains(t, code("dir/ü.go", focus{Line: 2}), "> 2 | l2")
	require.Empty(t, code("missing.go", focus{Line: 1}), "a file GitHub does not have shows no code")
	require.Empty(t, code("../escape.go", focus{Line: 1}))
	require.Contains(t, calls.calls, "gh api -H Accept: application/vnd.github.raw repos/ecosia/core/contents/a/b.go?ref=abc123")
	require.Contains(t, calls.calls, "gh api -H Accept: application/vnd.github.raw repos/ecosia/core/contents/dir/%C3%BC.go?ref=abc123")

	d.PR("", "", "abc123")
	calls.calls = nil
	reviewCode(review.WithDisplay(t.Context(), d), codeStyle{width: 80}, "suggestion: a.go:1 — x")
	require.Equal(t, []string{"gh api -H Accept: application/vnd.github.raw repos/{owner}/{repo}/contents/a.go?ref=abc123"}, calls.calls,
		"a PR named by number is read from this checkout's repository")
}
