package src

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
)

var respondFB = review.Feedback{
	Number:  7,
	HeadSHA: "f00dfeed",
	Threads: []review.Thread{
		{ID: "T1", RootID: 101, Path: "a.go", Line: 4, Comments: []review.Comment{{Author: "bob", Body: "`p.n` panics when `p` is nil."}}},
		{ID: "T2", RootID: 102, Path: "a.go", Line: 2, Comments: []review.Comment{{Author: "bob", Body: "Rename `F`?"}}},
	},
	Reviews: []review.Summary{{Author: "bob", State: "CHANGES_REQUESTED", Body: "Two things."}},
}

const respondAnswer = "Guarded the nil case; kept the name.\n\n```json\n" +
	`{"threads":[{"thread":"T1","verdict":"valid","evidence":"a_test.go panics without it","commit":"abc1234","reply":"Fixed in abc1234: return 0 for a nil ` + "`p`" + `."},` +
	`{"thread":"T2","verdict":"not valid","evidence":"F is the public name","reply":"F matches the public API, so it stays."}]}` + "\n```"

// respondRunner answers the git and gh calls of a respond pass and records them.
type respondRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *respondRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	switch {
	case call == "git show f00dfeed:a.go":
		return []byte("package a\n\nfunc F(p *T) int {\n\treturn p.n\n}\n"), nil
	case strings.HasPrefix(call, "git show -s"):
		return []byte("abc1234 Return 0 for a nil T\n"), nil
	case strings.HasPrefix(call, "git show --format="):
		return []byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -3,2 +3,5 @@\n func F(p *T) int {\n+\tif p == nil {\n+\t\treturn 0\n+\t}\n \treturn p.n\n"), nil
	case strings.HasPrefix(call, "gh api"):
		return []byte(`{"html_url":"https://github.com/o/r/pull/7#discussion_r9"}`), nil
	}
	return nil, nil
}

func respondFixture(t *testing.T, key string) (*respondRunner, *forms.Accordion, postFixture) {
	t.Helper()
	out, err := json.Marshal(map[string]any{"output": respondAnswer, "steps": 4, "cost": 0.2, "duration": 3000})
	require.NoError(t, err)
	result := filepath.Join(t.TempDir(), "result.json")
	require.NoError(t, os.WriteFile(result, out, 0o600))
	f := newPostFixture(t, "cat "+result, 0)
	onTerminal(t, true)
	shown := pressing(t, key, nil)
	r := &respondRunner{}
	old := codeRunner
	codeRunner = r
	t.Cleanup(func() { codeRunner = old })
	return r, shown, f
}

func runRespond(t *testing.T, f postFixture) (bool, string) {
	t.Helper()
	fb := respondFB
	ctx := review.WithRespond(t.Context(), &fb)
	var chat bool
	out, _, err := captured(t, func() error {
		var err error
		chat, err = runReviewEngine(ctx, []string{f.engine, "the brief"}, os.Environ(), time.Time{})
		return err
	})
	require.NoError(t, err)
	return chat, out
}

func TestRespond_ShowsEachThreadWithItsCodeVerdictFixAndReply(t *testing.T) {
	r, shown, f := respondFixture(t, "")
	chat, out := runRespond(t, f)
	require.False(t, chat)
	require.Equal(t, "Guarded the nil case; kept the name.", shown.Intro)
	require.Len(t, shown.Items, 2, "one row for a.go, one for the review summaries")
	require.Equal(t, "a.go", shown.Items[0].Title)
	require.Equal(t, "1 valid · 1 not valid", shown.Items[0].Summary)
	body := ansi.Strip(shown.Items[0].Body)
	for _, want := range []string{
		"  > 4 |     return p.n", // the code the thread is on, as the reviewer saw it
		"@bob p.n panics when p is nil.",
		"valid: a_test.go panics without it",
		"fixed in abc1234 Return 0 for a nil T",
		"+    if p == nil {",
		"↳ reply Fixed in abc1234: return 0 for a nil p.",
		"not valid: F is the public name",
		"↳ reply F matches the public API, so it stays.",
	} {
		require.Contains(t, body, want)
	}
	require.Less(t, strings.Index(body, "return p.n"), strings.Index(body, "@bob p.n panics"), "the code comes before the comment")
	require.Equal(t, "Review summaries", shown.Items[1].Title)
	require.Equal(t, []forms.Action{{Key: "p", Label: "push fixes and post replies"}, {Key: "c", Label: "continue in chat"}}, shown.Keys)
	require.Contains(t, out, "  a.go  1 valid · 1 not valid\n", "a recap stays in the terminal")
	for _, c := range r.calls {
		require.NotContains(t, c, "push", "q pushes nothing")
		require.NotContains(t, c, "replies", "q posts nothing")
	}
}

func TestRespond_PPushesThenPostsEveryReply(t *testing.T) {
	r, _, f := respondFixture(t, keyPublish)
	chat, _ := runRespond(t, f)
	require.False(t, chat)
	push := -1
	var replies []int
	for i, c := range r.calls {
		if c == "git push origin HEAD" {
			push = i
		}
		if strings.Contains(c, "/replies") {
			replies = append(replies, i)
		}
	}
	require.GreaterOrEqual(t, push, 0, "the fixes are pushed")
	require.Len(t, replies, 2)
	for _, i := range replies {
		require.Greater(t, i, push, "replies name commits, so they go up after the push")
	}
}

func TestRespond_WithoutTheAnswersBlockItContinuesInTheChat(t *testing.T) {
	result := filepath.Join(t.TempDir(), "result.json")
	require.NoError(t, os.WriteFile(result, []byte(`{"output":"I fixed things but forgot the block.","steps":2}`), 0o600))
	f := newPostFixture(t, "cat "+result, 0)
	onTerminal(t, true)
	shown := pressing(t, keyPublish, nil)
	chat, out := runRespond(t, f)
	require.True(t, chat, "the user can ask for the verdicts in the chat")
	require.Contains(t, out, "I fixed things but forgot the block.")
	require.Empty(t, shown.Items, "no view without answers, so p cannot post anything")
	require.NoFileExists(t, f.ghArgs)
}

func TestRespond_COpensTheChat(t *testing.T) {
	_, _, f := respondFixture(t, keyChat)
	chat, _ := runRespond(t, f)
	require.True(t, chat)
}
