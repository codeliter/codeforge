package review

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var respondFeedback = Feedback{Number: 7, Threads: []Thread{
	{ID: "T1", RootID: 101, Path: "a.go", Line: 3},
	{ID: "T2", RootID: 102, Path: "b.go", Line: 9},
}}

const respondOutput = "Fixed the nil check; pushed back on the rename.\n\n```json\n" +
	`{"threads":[` +
	`{"thread":"T1","verdict":"Valid","evidence":"a_test.go fails without it","commit":"abc1234","reply":"Fixed in abc1234: guard nil."},` +
	`{"thread":"T2","verdict":"not valid","evidence":"the name matches the API","commit":"not a sha; rm -rf","reply":"The name follows the public API."},` +
	`{"thread":"T9","verdict":"valid","reply":"to a thread nobody opened"},` +
	`{"thread":"T1","verdict":"valid","reply":"a second answer for T1"}]}` +
	"\n```\n"

func TestParseAnswers(t *testing.T) {
	prose, answers, err := ParseAnswers(respondOutput, respondFeedback)
	require.NoError(t, err)
	require.Equal(t, "Fixed the nil check; pushed back on the rename.", prose)
	require.Equal(t, []Answer{
		{Thread: "T1", Verdict: "valid", Evidence: "a_test.go fails without it", Commit: "abc1234", Reply: "Fixed in abc1234: guard nil."},
		{Thread: "T2", Verdict: "not valid", Evidence: "the name matches the API", Reply: "The name follows the public API."},
	}, answers, "unknown and repeated threads are dropped, and so is a commit that is not a sha")

	_, _, err = ParseAnswers("Just prose, no block.", respondFeedback)
	require.ErrorIs(t, err, ErrNoAnswers)
	_, _, err = ParseAnswers("x\n```json\n{not json}\n```", respondFeedback)
	require.ErrorIs(t, err, ErrNoAnswers)
}

type replyRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *replyRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return []byte(`{"html_url":"https://github.com/o/r/pull/7#discussion_r1"}`), nil
}

func TestPostReplies_AnswersEachThreadAtItsRoot(t *testing.T) {
	_, answers, err := ParseAnswers(respondOutput, respondFeedback)
	require.NoError(t, err)
	answers = append(answers, Answer{Thread: "T2", Reply: ""})
	r := &replyRunner{}
	got := PostReplies(t.Context(), r, respondFeedback, answers[:2])
	require.Len(t, got, 2)
	require.ElementsMatch(t, []string{
		"gh api --method POST repos/{owner}/{repo}/pulls/7/comments/101/replies --raw-field=body=Fixed in abc1234: guard nil.",
		"gh api --method POST repos/{owner}/{repo}/pulls/7/comments/102/replies --raw-field=body=The name follows the public API.",
	}, r.calls)
	for _, rep := range got {
		require.NoError(t, rep.Err)
		require.Equal(t, "https://github.com/o/r/pull/7#discussion_r1", rep.URL)
	}
	require.Empty(t, PostReplies(t.Context(), r, respondFeedback, []Answer{{Thread: "T2"}}), "an empty reply is not posted")
}

func TestPromptFeedback_AsksForTheAnswersBlock(t *testing.T) {
	p := PromptFeedback(respondFeedback)
	require.Contains(t, p, "post nothing and push nothing")
	require.Contains(t, p, `"thread":"<thread id from the feedback>"`)
	require.Less(t, strings.Index(p, "</feedback>"), strings.Index(p, AnswersNote), "the contract comes after the reviewers' text")
}
