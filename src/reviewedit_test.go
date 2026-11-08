package src

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
)

const editableReview = "- issue (blocking): a.go:3 — AlwaysSample sends every span\n" +
	"- nitpick: a.go:9 — wrap the error\n" +
	"  with %w, not %v\n\n" +
	"- nitpick: b.go:2 — rename h\n\n" +
	"Looks close. Fix the sampler first.\n\nDecision: Request changes"

func TestApplyEdits_RewritesDeletesAndCollectsQuestions(t *testing.T) {
	out, questions, edited := applyEdits(editableReview, []forms.Change{
		{Kind: forms.ChangeEdit, Note: "f1", Text: "nitpick: a.go:9 — wrap it: fmt.Errorf(\"shutdown: %w\", err)"},
		{Kind: forms.ChangeDelete, Note: "f2"},
		{Kind: forms.ChangeAsk, Note: "f0", Text: "It is off in prod. Still blocking?"},
		{Kind: forms.ChangeAsk, Section: "b.go", Text: "anything else here?"},
		{Kind: forms.ChangeAsk, Text: "You missed the retry path."},
	})
	require.Equal(t, "- issue (blocking): a.go:3 — AlwaysSample sends every span\n"+
		"- nitpick: a.go:9 — wrap it: fmt.Errorf(\"shutdown: %w\", err)\n"+
		"  with %w, not %v\n\n"+
		"Looks close. Fix the sampler first.\n\nDecision: Request changes", out,
		"a rewrite replaces the finding's line and keeps its list marker; a deletion takes the whole finding and its gap")
	require.Equal(t, []string{"nitpick: a.go:9 — wrap it: fmt.Errorf(\"shutdown: %w\", err)"}, edited)
	require.Equal(t, []question{
		{About: "`a.go:3` (issue (blocking): a.go:3 — AlwaysSample sends every span)", Place: "a.go:3", Nth: 1, Raw: "issue (blocking): a.go:3 — AlwaysSample sends every span", Text: "It is off in prod. Still blocking?"},
		{About: "b.go", Text: "anything else here?"},
		{About: "the whole review", Text: "You missed the retry path."},
	}, questions)
}

func TestReviseReview_OnlyEditsNeedNoReviewer(t *testing.T) {
	turn := func(context.Context, string) (string, error) { t.Fatal("no question, no turn"); return "", nil }
	round, err := reviseReview(t.Context(), editableReview, []forms.Change{{Kind: forms.ChangeEdit, Note: "f2", Text: "nitpick: b.go:2 — rename h to handler"}}, turn, nil)
	require.NoError(t, err)
	require.Contains(t, round.Text, "- nitpick: b.go:2 — rename h to handler")
	require.Equal(t, map[string]noteMark{"f2": {Tag: markEdited}}, round.Marks)
}

func TestReviseReview_TheReviewerAnswersAndRevises(t *testing.T) {
	var prompt string
	turn := func(_ context.Context, p string) (string, error) {
		prompt = p
		return "- suggestion: a.go:3 — use ParentBased so turning it on stays cheap\n" +
			"- nitpick: a.go:9 — wrap the error\n  with %w, not %v\n\n" +
			"- issue (blocking): c.go:57 — a retry re-sends the read body\n\n" +
			"Close; fix the retry.\n\nDecision: Request changes\n\n" +
			"```codeforge-answers\n{\"answers\":[{\"question\":1,\"reply\":\"Agreed, the flag gates it.\"},{\"question\":2,\"reply\":\"You were right.\"}]}\n```", nil
	}
	round, err := reviseReview(t.Context(), editableReview, []forms.Change{
		{Kind: forms.ChangeDelete, Note: "f2"},
		{Kind: forms.ChangeAsk, Note: "f0", Text: "Still blocking?"},
		{Kind: forms.ChangeAsk, Text: "You missed the retry path."},
	}, turn, nil)
	require.NoError(t, err)
	require.Contains(t, prompt, "1. About `a.go:3` (issue (blocking): a.go:3 — AlwaysSample sends every span): Still blocking?")
	require.Contains(t, prompt, "2. About the whole review: You missed the retry path.")
	require.Contains(t, prompt, "do not bring back what they deleted")
	require.NotContains(t, prompt, "rename h", "the reviewer sees the review with the user's changes")
	require.NotContains(t, round.Text, "codeforge-answers", "the answers never become review text")
	require.Equal(t, map[string]noteMark{
		"f0": {Tag: markRevised, Reply: "Agreed, the flag gates it."},
		"f2": {Tag: markNew},
	}, round.Marks)
	require.Equal(t, []string{"About the whole review: You were right."}, round.Answers, "an answer about no finding is shown above the files")
}

func TestReviseReview_NoReviewBackChangesNothing(t *testing.T) {
	turn := func(context.Context, string) (string, error) { return "```codeforge-answers\n{}\n```", nil }
	_, err := reviseReview(t.Context(), editableReview, []forms.Change{{Kind: forms.ChangeAsk, Text: "why?"}}, turn, nil)
	require.ErrorIs(t, err, errNoRevision)
}

func TestSplitAnswers_WithoutABlockItIsAllReview(t *testing.T) {
	text, answers := splitAnswers("Fine.\n\nDecision: Approve")
	require.Equal(t, "Fine.\n\nDecision: Approve", text)
	require.Nil(t, answers)
}

func TestReviewTurn_ResumesTheReviewSessionWithTheNewPrompt(t *testing.T) {
	f := newPostFixture(t, `printf '%s' '{"output":"Revised.","steps":1,"cost":0.01,"duration":10}'`, 0)
	argv := []string{f.engine, "--profile", "codeforge-review", "--mode", "plan", "--session-id", "rev-1", "--system", "be brief", "--headless", "--save-session", "--json", "the original brief"}
	reply, err := reviewTurn(argv, os.Environ())(t.Context(), "the questions")
	require.NoError(t, err)
	require.Equal(t, "Revised.", reply)
	args, err := os.ReadFile(f.engineArgs)
	require.NoError(t, err)
	require.Equal(t, "--session rev-1 --profile codeforge-review --mode plan --system be brief --headless --save-session --json the questions ", string(args))
	require.NotContains(t, string(args), "the original brief")
}

func TestReviewTurn_TheReviewsClockDoesNotCutIt(t *testing.T) {
	f := newPostFixture(t, `printf '%s' '{"output":"Revised."}'`, 0)
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	reply, err := reviewTurn([]string{f.engine, "--session-id", "rev-1", "brief"}, os.Environ())(ctx, "q")
	require.NoError(t, err, "the user sent the questions after the review's own time ran out")
	require.Equal(t, "Revised.", strings.TrimSpace(reply))
}

func TestRoundIntro(t *testing.T) {
	st := codeStyle{width: 100}
	tests := []struct {
		name  string
		round reviewRound
		want  string
	}{
		{name: "a round that changed nothing says nothing", round: reviewRound{}},
		{
			name:  "the changed findings are named by place",
			round: reviewRound{Marks: map[string]noteMark{"f0": {Tag: markRevised, Reply: "it blocks"}, "f1": {Tag: markEdited}, "f2": {Reply: "yes"}}},
			want:  "This round  revised a.go:3 · edited by you a.go:9 · answered b.go:2",
		},
		{
			name:  "answers about no finding follow",
			round: reviewRound{Marks: map[string]noteMark{"f2": {Tag: markNew}}, Answers: []string{"About the whole review: fine"}},
			want:  "This round  new b.go:2\n\nThe reviewer's answers\n  ↳ About the whole review: fine",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, roundIntro(st, editableReview, tt.round))
		})
	}
}

func TestWithoutPrompt(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "the prompt goes, flags stay", args: []string{"--mode", "plan", "--headless", "the brief", "--json"}, want: []string{"--mode", "plan", "--headless", "--json"}},
		{name: "a flag codeforge does not know keeps its value", args: []string{"--team", "core", "the brief"}, want: []string{"--team", "core"}},
		{name: "no prompt keeps everything", args: []string{"--headless"}, want: []string{"--headless"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, withoutPrompt(tt.args))
		})
	}
}

func TestReviseReview_TheRevisedReplyIsCleaned(t *testing.T) {
	turn := func(context.Context, string) (string, error) {
		return "NOTE\n\n" + editableReview + "\n\n```codeforge-answers\n{\"answers\":[{\"question\":1,\"reply\":\"ok\"}]}\n```", nil
	}
	clean := func(s string) string { return strings.TrimPrefix(s, "NOTE\n\n") }
	round, err := reviseReview(t.Context(), editableReview, []forms.Change{{Kind: forms.ChangeAsk, Text: "why?"}}, turn, clean)
	require.NoError(t, err)
	require.NotContains(t, round.Text, "NOTE", "what the clean step drops is never review text")
	require.Empty(t, round.Marks, "nothing changed, so nothing is marked")
}

func TestCompareFindings(t *testing.T) {
	two := "- nitpick: a.go:3 — one\n- nitpick: a.go:3 — two\n"
	tests := []struct {
		name          string
		before, after string
		want          map[string]noteMark
	}{
		{name: "two findings at one place are told apart", before: two, after: two, want: map[string]noteMark{}},
		{name: "the second at a place reworded", before: two, after: "- nitpick: a.go:3 — one\n- nitpick: a.go:3 — 2\n", want: map[string]noteMark{"f1": {Tag: markRevised}}},
		{name: "a new place", before: two, after: two + "- nitpick: b.go:1 — new\n", want: map[string]noteMark{"f2": {Tag: markNew}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, compareFindings(tt.before, tt.after))
		})
	}
}

func TestApplyEdits_ARewriteTypedWithAListMarkerKeepsOne(t *testing.T) {
	out, _, edited := applyEdits(editableReview, []forms.Change{{Kind: forms.ChangeEdit, Note: "f2", Text: "- nitpick: b.go:2 — rename h to handler"}})
	require.Contains(t, out, "\n- nitpick: b.go:2 — rename h to handler")
	require.NotContains(t, out, "- - ")
	require.Equal(t, []string{"nitpick: b.go:2 — rename h to handler"}, edited)
}

func TestReviseReview_AFailedTurnKeepsTheUsersOwnChanges(t *testing.T) {
	errDown := errors.New("engine down") //nolint:err113 // a test stand-in for any engine failure
	turn := func(context.Context, string) (string, error) { return "", errDown }
	round, err := reviseReview(t.Context(), editableReview, []forms.Change{
		{Kind: forms.ChangeDelete, Note: "f2"},
		{Kind: forms.ChangeEdit, Note: "f1", Text: "nitpick: a.go:9 — wrap it"},
		{Kind: forms.ChangeAsk, Text: "why?"},
	}, turn, nil)
	require.Error(t, err)
	require.NotContains(t, round.Text, "rename h", "the deletion stands")
	require.Contains(t, round.Text, "nitpick: a.go:9 — wrap it")
	require.Equal(t, map[string]noteMark{"f1": {Tag: markEdited}}, round.Marks)
}

func TestReviseReview_AnAnswerGoesToTheFindingAskedAbout(t *testing.T) {
	const text = "- nitpick: a.go:3 — one\n- nitpick: a.go:3 — two\n\nOk."
	const answer = "\n\n```codeforge-answers\n{\"answers\":[{\"question\":1,\"reply\":\"about two\"}]}\n```"
	tests := []struct {
		name    string
		changes []forms.Change
		revised string
		want    string
	}{
		{name: "the second of two at one place", changes: []forms.Change{{Kind: forms.ChangeAsk, Note: "f1", Text: "?"}}, revised: text, want: "f1"},
		{
			name:    "the first deleted, the second is now the first",
			changes: []forms.Change{{Kind: forms.ChangeDelete, Note: "f0"}, {Kind: forms.ChangeAsk, Note: "f1", Text: "?"}},
			revised: "- nitpick: a.go:3 — two\n\nOk.", want: "f0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn := func(context.Context, string) (string, error) { return tt.revised + answer, nil }
			round, err := reviseReview(t.Context(), text, tt.changes, turn, nil)
			require.NoError(t, err)
			require.Equal(t, "about two", round.Marks[tt.want].Reply)
		})
	}
}

func TestReviseReview_AnAnswerWhoseFindingIsGoneNamesItsPlace(t *testing.T) {
	turn := func(context.Context, string) (string, error) {
		return "Ok.\n\n```codeforge-answers\n{\"answers\":[{\"question\":1,\"reply\":\"dropped it\"}]}\n```", nil
	}
	round, err := reviseReview(t.Context(), editableReview, []forms.Change{{Kind: forms.ChangeAsk, Note: "f2", Text: "?"}}, turn, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"About b.go:2: dropped it"}, round.Answers)
}

func TestMarkRewrites_EachRewriteTagsOneFinding(t *testing.T) {
	round := reviewRound{Text: "- nitpick: a.go:3 — same\n- nitpick: a.go:3 — same\n", Marks: map[string]noteMark{}}
	markRewrites(round, []string{"nitpick: a.go:3 — same"})
	require.Equal(t, map[string]noteMark{"f0": {Tag: markEdited}}, round.Marks)
}

func TestReviseReview_AnAnswerIsNeverPutOnAnotherFinding(t *testing.T) {
	const text = "- nitpick: a.go:3 — one\n- nitpick: a.go:9 — mid\n- nitpick: a.go:3 — two\n\nOk.\n\nDecision: Comment"
	const answer = "\n\n```codeforge-answers\n{\"answers\":[{\"question\":1,\"reply\":\"r\"}]}\n```"
	tests := []struct {
		name     string
		changes  []forms.Change
		revised  string
		wantMark string
		wantNote string
	}{
		{
			name:    "a deleted finding's answer is shown by its place",
			changes: []forms.Change{{Kind: forms.ChangeDelete, Note: "f0"}, {Kind: forms.ChangeAsk, Note: "f0", Text: "?"}},
			revised: "- nitpick: a.go:9 — mid\n- nitpick: a.go:3 — two\n\nOk.", wantNote: "About a.go:3: r",
		},
		{
			name:    "the finding asked about is dropped by the reviewer",
			changes: []forms.Change{{Kind: forms.ChangeAsk, Note: "f2", Text: "?"}},
			revised: "- nitpick: a.go:3 — one\n- nitpick: a.go:9 — mid\n\nOk.", wantNote: "About a.go:3: r",
		},
		{
			name:    "counted at its place past a deletion and another place",
			changes: []forms.Change{{Kind: forms.ChangeDelete, Note: "f0"}, {Kind: forms.ChangeAsk, Note: "f2", Text: "?"}},
			revised: "- nitpick: a.go:9 — mid\n- nitpick: a.go:3 — two\n\nOk.", wantMark: "f1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn := func(context.Context, string) (string, error) { return tt.revised + answer, nil }
			round, err := reviseReview(t.Context(), text, tt.changes, turn, nil)
			require.NoError(t, err)
			if tt.wantMark != "" {
				require.Equal(t, "r", round.Marks[tt.wantMark].Reply)
				require.Empty(t, round.Answers)
				return
			}
			for id, m := range round.Marks {
				require.Empty(t, m.Reply, "no finding carries the answer: %s", id)
			}
			require.Equal(t, []string{tt.wantNote}, round.Answers)
		})
	}
}

func TestApplyEdits_TheDecisionLineIsNeverTouched(t *testing.T) {
	out, _, _ := applyEdits(editableReview, []forms.Change{{Kind: forms.ChangeDelete, Note: "f0"}, {Kind: forms.ChangeDelete, Note: "f1"}, {Kind: forms.ChangeDelete, Note: "f2"}})
	require.True(t, strings.HasSuffix(out, "Decision: Request changes"), out)
}

func TestRewriteText(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "  - nitpick: a.go:3 — x  ", want: "nitpick: a.go:3 — x"},
		{in: "1. nitpick: a.go:3 — x", want: "nitpick: a.go:3 — x"},
		{in: "1. first do this", want: "1. first do this"},
		{in: "nitpick: a.go:3 — x", want: "nitpick: a.go:3 — x"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) { require.Equal(t, tt.want, rewriteText(tt.in)) })
	}
}

func TestApplyEdits_ADecisionRightUnderADeletedFindingStays(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "- issue (blocking): a.go:3 — x\nDecision: Request changes", want: "Decision: Request changes"},
		{in: "- issue (blocking): a.go:3 — x\n**Decision: Approve**\n", want: "**Decision: Approve**"},
		{in: "- issue (blocking): a.go:3 — x\n  Decision: Approve\n  more of the finding", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			out, _, _ := applyEdits(tt.in, []forms.Change{{Kind: forms.ChangeDelete, Note: "f0"}})
			require.Equal(t, tt.want, out)
		})
	}
}

func TestReviseReview_TheAnswerFollowsTheFindingAsTheUserLeftIt(t *testing.T) {
	const answer = "\n\n```codeforge-answers\n{\"answers\":[{\"question\":1,\"reply\":\"r\"}]}\n```"
	tests := []struct {
		name     string
		text     string
		changes  []forms.Change
		revised  string
		wantMark string
		wantNote string
	}{
		{
			name:    "a rewrite moved another finding to the asked one's place",
			text:    "- nitpick: a.go:9 — mid\n- nitpick: a.go:3 — one\n\nOk.",
			changes: []forms.Change{{Kind: forms.ChangeEdit, Note: "f0", Text: "nitpick: a.go:3 — moved"}, {Kind: forms.ChangeAsk, Note: "f1", Text: "?"}},
			revised: "- nitpick: a.go:3 — moved\n- nitpick: a.go:3 — one\n\nOk.", wantMark: "f1",
		},
		{
			name:    "the reviewer dropped the asked finding and kept its sibling",
			text:    "- nitpick: a.go:3 — one\n- nitpick: a.go:3 — two\n\nOk.",
			changes: []forms.Change{{Kind: forms.ChangeAsk, Note: "f0", Text: "?"}},
			revised: "- nitpick: a.go:3 — two\n\nOk.", wantNote: "About a.go:3: r",
		},
		{
			name:    "the reviewer reworded the asked finding in place",
			text:    "- nitpick: a.go:3 — one\n- nitpick: a.go:3 — two\n\nOk.",
			changes: []forms.Change{{Kind: forms.ChangeAsk, Note: "f1", Text: "?"}},
			revised: "- nitpick: a.go:3 — one\n- suggestion: a.go:3 — two, reworded\n\nOk.", wantMark: "f1",
		},
		{
			name:    "a rewrite that is no finding any more",
			text:    "- nitpick: a.go:3 — one\n\nOk.",
			changes: []forms.Change{{Kind: forms.ChangeEdit, Note: "f0", Text: "just prose"}, {Kind: forms.ChangeAsk, Note: "f0", Text: "?"}},
			revised: "just prose\n\nOk.", wantNote: "About a.go:3: r",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn := func(context.Context, string) (string, error) { return tt.revised + answer, nil }
			round, err := reviseReview(t.Context(), tt.text, tt.changes, turn, nil)
			require.NoError(t, err)
			if tt.wantMark != "" {
				require.Equal(t, "r", round.Marks[tt.wantMark].Reply)
				require.Empty(t, round.Answers)
				return
			}
			for id, m := range round.Marks {
				require.Empty(t, m.Reply, "no finding carries the answer: %s", id)
			}
			require.Equal(t, []string{tt.wantNote}, round.Answers)
		})
	}
}
