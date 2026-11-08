//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/review"
)

// postFixture puts a fake engine and a fake gh on the path; each records how it was called.
type postFixture struct {
	engine, engineArgs, ghArgs string
}

func newPostFixture(t *testing.T, engineBody string, ghExit int) postFixture {
	t.Helper()
	dir := t.TempDir()
	f := postFixture{engine: filepath.Join(dir, "engine"), engineArgs: filepath.Join(dir, "engine.args"), ghArgs: filepath.Join(dir, "gh.args")}
	require.NoError(t, os.WriteFile(f.engine, []byte("#!/bin/sh\nprintf '%s ' \"$@\" > \""+f.engineArgs+"\"\n"+engineBody+"\n"), execPerm))
	// The fake records its arguments and, for a review sent with --input, the request it was given.
	gh := "#!/bin/sh\nprintf '%s ' \"$@\" > \"" + f.ghArgs + "\"\nprev=\nfor a in \"$@\"; do if [ \"$prev\" = --input ]; then cat \"$a\" >> \"" + f.ghArgs + "\"; fi; prev=$a; done\n" +
		"echo https://github.com/acme/w/pull/42#issuecomment-9\nexit " + string(rune('0'+ghExit)) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(gh), execPerm))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

// runReview runs runReviewEngine for tests that only check what it printed or posted.
func runReview(ctx context.Context, argv, env []string, limit time.Time) error {
	_, err := runReviewEngine(ctx, argv, env, limit)
	return err
}

func reviewContext(t *testing.T, comment bool) context.Context {
	t.Helper()
	post := &review.PostRequest{}
	if comment {
		post.Target("acme/w", "42")
		post.Preapprove()
	}
	ctx := review.WithDisplay(review.WithPost(review.WithDeadline(t.Context(), time.Now().Add(time.Minute)), post), &review.Display{})
	return ctx
}

func TestRunReviewEngine_PostsTheReviewWhenAskedTo(t *testing.T) {
	f := newPostFixture(t, "printf '%s' '{\"output\":\"### a.go\\n\\nissue (blocking): a.go:3 — drops data\\n\\n### Final comment\\n\\nFix the drop first.\\n\\nDecision: Request changes\"}'", 0)
	require.NoError(t, runReview(reviewContext(t, true), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second)))

	args, err := os.ReadFile(f.engineArgs)
	require.NoError(t, err)
	require.Contains(t, string(args), "--json", "the run's result is read as JSON")
	posted, err := os.ReadFile(f.ghArgs)
	require.NoError(t, err)
	require.Contains(t, string(posted), "api --method POST repos/acme/w/pulls/42/reviews --input ")
	require.Contains(t, string(posted), `"event":"REQUEST_CHANGES"`, "the closing decision is the review's type")
	require.Contains(t, string(posted), "`a.go:3`: drops data", "off the diff, a finding is named by its place")
	require.NotContains(t, string(posted), "issue (blocking)", "labels are never posted")
	require.Contains(t, string(posted), "Fix the drop first.")
	require.NotContains(t, string(posted), "Decision:", "the decision line is not posted as text")
	require.NotContains(t, string(posted), "### Final comment", "the final comment is the body, without its heading")
	require.Contains(t, string(posted), "Review assisted by Codeforge")
}

func TestRunReviewEngine_PostsNothingUnlessAsked(t *testing.T) {
	f := newPostFixture(t, "echo plain output", 0)
	require.NoError(t, runReview(reviewContext(t, false), []string{f.engine}, os.Environ(), time.Now().Add(10*time.Second)))
	require.NoFileExists(t, f.ghArgs)
	args, err := os.ReadFile(f.engineArgs)
	require.NoError(t, err)
	require.NotContains(t, string(args), "--json", "without --comment the run is untouched")

	require.NoError(t, runReview(t.Context(), []string{f.engine}, os.Environ(), time.Time{}), "outside a review there is no request at all")
	require.NoFileExists(t, f.ghArgs)
}

func TestRunReviewEngine_NeverPostsAFailedOrEmptyRun(t *testing.T) {
	failed := newPostFixture(t, "printf '%s' '{\"output\":\"half a review\"}'\nexit 1", 0)
	err := runReview(reviewContext(t, true), []string{failed.engine}, os.Environ(), time.Now().Add(10*time.Second))
	var exit *EngineExitError
	require.ErrorAs(t, err, &exit, "the engine's own failure comes back unchanged")
	require.NoFileExists(t, failed.ghArgs)

	empty := newPostFixture(t, "printf '%s' '{\"output\":\"  \"}'", 0)
	err = runReview(reviewContext(t, true), []string{empty.engine}, os.Environ(), time.Now().Add(10*time.Second))
	require.ErrorIs(t, err, review.ErrNoReviewText)
	require.NoFileExists(t, empty.ghArgs)

	timedOut := newPostFixture(t, "printf '%s' '{\"output\":\"\",\"error\":\"Timeout after 2m 54s\"}'", 0)
	err = runReview(reviewContext(t, true), []string{timedOut.engine}, os.Environ(), time.Now().Add(10*time.Second))
	require.ErrorContains(t, err, "Timeout after 2m 54s")
	require.NoFileExists(t, timedOut.ghArgs)
}

func TestRunReviewEngine_ReportsAFailedPost(t *testing.T) {
	f := newPostFixture(t, "printf '%s' '{\"output\":\"Approve.\"}'", 1)
	err := runReview(reviewContext(t, true), []string{f.engine}, os.Environ(), time.Now().Add(10*time.Second))
	require.ErrorContains(t, err, "the review above was not posted")
}

func TestReviewArgs_CommentNeedsAPullRequest(t *testing.T) {
	_, err := reviewArgs(t.Context(), "", config.ReviewConfig{}, nil, nil, []string{"--comment"})
	require.ErrorIs(t, err, errCommentNeedsPR)

	_, err = reviewArgs(t.Context(), "", config.ReviewConfig{}, nil, nil, []string{"42;rm", "--comment"})
	require.ErrorIs(t, err, review.ErrNoPR, "--comment with a PR reference is parsed like any review")
}

// asked sets up what the person at the terminal does: nobody there, or answering with line.
func asked(t *testing.T, tty bool, line string) {
	t.Helper()
	oldIn, oldTTY := confirmIn, confirmTTY
	confirmIn, confirmTTY = strings.NewReader(line), func() bool { return tty }
	t.Cleanup(func() { confirmIn, confirmTTY = oldIn, oldTTY })
}

func askingContext(t *testing.T) context.Context {
	t.Helper()
	post := &review.PostRequest{}
	post.Target("acme/w", "42")
	return review.WithDisplay(review.WithPost(review.WithDeadline(t.Context(), time.Now().Add(time.Minute)), post), &review.Display{})
}

func TestRunReviewEngine_PostsOnlyAfterTheUserSaysYes(t *testing.T) {
	for _, answer := range []string{"y\n", "YES\n", " yes \n"} {
		f := newPostFixture(t, "printf '%s' '{\"output\":\"Approve.\"}'", 0)
		asked(t, true, answer)
		require.NoError(t, runReview(askingContext(t), []string{f.engine}, os.Environ(), time.Now().Add(10*time.Second)))
		require.FileExists(t, f.ghArgs, "answered "+answer)
	}
	for _, answer := range []string{"\n", "n\n", "no\n", "yep\n", "post it\n", ""} {
		f := newPostFixture(t, "printf '%s' '{\"output\":\"Approve.\"}'", 0)
		asked(t, true, answer)
		require.NoError(t, runReview(askingContext(t), []string{f.engine}, os.Environ(), time.Now().Add(10*time.Second)), "declining is not a failure")
		require.NoFileExists(t, f.ghArgs, "answered "+answer)
	}
}

func TestRunReviewEngine_CancellingTheQuestionPostsNothing(t *testing.T) {
	f := newPostFixture(t, "printf '%s' '{\"output\":\"Approve.\"}'", 0)
	reader, writer := io.Pipe() // never answered
	t.Cleanup(func() { _ = writer.Close() })
	oldIn, oldTTY := confirmIn, confirmTTY
	confirmIn, confirmTTY = reader, func() bool { return true }
	t.Cleanup(func() { confirmIn, confirmTTY = oldIn, oldTTY })

	ctx, cancel := context.WithCancel(askingContext(t))
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	require.NoError(t, runReview(ctx, []string{f.engine}, os.Environ(), time.Now().Add(10*time.Second)))
	require.Less(t, time.Since(start), 5*time.Second, "an unanswered question ends when the command is cancelled")
	require.NoFileExists(t, f.ghArgs)
}

func TestReadLine_TakesOneLineAndNothingAfterIt(t *testing.T) {
	in := strings.NewReader("yes\ntyped ahead for the TUI\n")
	require.Equal(t, "yes", readLine(in))
	rest, err := io.ReadAll(in)
	require.NoError(t, err)
	require.Equal(t, "typed ahead for the TUI\n", string(rest), "the next line is left for whoever reads next")
	require.Equal(t, "", readLine(strings.NewReader("")))
	require.Equal(t, "no newline", readLine(strings.NewReader("no newline")))
}

func TestConfirmed_ALateYesAfterCancellationIsNotAYes(t *testing.T) {
	asked(t, true, "y\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.False(t, confirmed(ctx, io.Discard, "post?"))
}

func TestRunReviewEngine_WithNobodyToAskItDoesNotPost(t *testing.T) {
	f := newPostFixture(t, "printf '%s' '{\"output\":\"Approve.\"}'", 0)
	asked(t, false, "y\n")
	require.NoError(t, runReview(askingContext(t), []string{f.engine}, os.Environ(), time.Now().Add(10*time.Second)))
	require.NoFileExists(t, f.ghArgs, "a script cannot answer for the user: use --comment=yes")
}

func TestRunReviewEngine_PostsTheSecondOpinionWithTheReview(t *testing.T) {
	f := newPostFixture(t, "printf '%s' '{\"output\":\"Approve.\"}'", 0)
	pipe := &review.Pipeline{}
	pipe.Start(t.Context(), "", "brief", []review.Reviewer{blockingJuror{}}, nil)
	ctx := review.WithPipeline(reviewContext(t, true), pipe)
	require.NoError(t, runReview(ctx, []string{f.engine}, os.Environ(), time.Now().Add(10*time.Second)))
	posted, err := os.ReadFile(f.ghArgs)
	require.NoError(t, err)
	require.Contains(t, string(posted), "Approve.")
	require.Contains(t, string(posted), "Unconfirmed blocking leads")
	require.Contains(t, string(posted), "a.go:3` drops data", "a juror's blocker must not be missing from a review that says Approve")
	require.NotContains(t, string(posted), "juror", "who raised a lead is not posted")
	require.Empty(t, pipe.Leads(t.Context()), "the leads are shown once")
}

type blockingJuror struct{}

func (blockingJuror) Name() string { return "juror" }

func (blockingJuror) Review(context.Context, string) (string, error) {
	return `{"verdict":"fail","summary":"bad","blocking":[{"file":"a.go","line":3,"what":"drops data"}],"should":[]}`, nil
}

func TestReviewArgs_CommentFlagValues(t *testing.T) {
	_, err := reviewArgs(t.Context(), "", config.ReviewConfig{}, nil, nil, []string{"42", "--comment=maybe"})
	require.ErrorIs(t, err, errCommentValue)
	_, err = reviewArgs(t.Context(), "", config.ReviewConfig{}, nil, nil, []string{"42", "--comment", "--json"})
	require.ErrorIs(t, err, errCommentJSON)
	_, err = reviewArgs(t.Context(), "", config.ReviewConfig{}, nil, nil, []string{"--comment=yes"})
	require.ErrorIs(t, err, errCommentNeedsPR)
}
