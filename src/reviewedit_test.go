package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
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
	require.Contains(t, prompt, "is not a problem and the explanation holds, drop that finding")
	require.Contains(t, prompt, "suggest Approve")
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

func TestCheckLeads(t *testing.T) {
	leads := review.LeadsHeader + "- `a.go:3` The ranking assumes the winner bonus always wins, and it does not. Duel example: ...\n- `b.go:9` drops data\n"
	tests := []struct {
		name  string
		reply string
		err   error
		want  string
	}{
		{
			name:  "rewritten in the user's words",
			reply: "- `a.go:3` A finisher can rank below a player who never finished. Sort by finished first.\n- `b.go:9` The retry drops the body. Buffer it.",
			want:  review.LeadsHeader + "- `a.go:3` A finisher can rank below a player who never finished. Sort by finished first.\n- `b.go:9` The retry drops the body. Buffer it.\n",
		},
		{name: "a lead left out keeps the original", reply: "- `a.go:3` Short.", want: leads},
		{name: "a place changed keeps the original", reply: "- `a.go:4` x\n- `b.go:9` y", want: leads},
		{name: "a failed turn keeps the original", err: errors.New("down"), want: leads}, //nolint:err113 // a stand-in failure
		{
			name:  "a lead the code rules out is left off",
			reply: "- RULED OUT `a.go:3` The winner bonus is applied after the sort in rank().\n- `b.go:9` The retry drops the body. Buffer it.",
			want:  review.LeadsHeader + "- `b.go:9` The retry drops the body. Buffer it.\n",
		},
		{name: "every lead ruled out leaves none", reply: "- RULED OUT `a.go:3` sorted later.\n- RULED OUT `b.go:9` the body is buffered in send().", want: ""},
		{name: "a lead both kept and ruled out keeps the original", reply: "- RULED OUT `a.go:3` fine.\n- `a.go:3` A loser can outrank the winner here.\n- `b.go:9` The retry drops the body here.", want: leads},
		{name: "a ruled-out place that is not a lead keeps the original", reply: "- RULED OUT `c.go:1` fine.\n- `a.go:3` A loser can outrank the winner here.", want: leads},
		{name: "a place that is not a lead keeps the original", reply: "- RULED OUT `a.go:3` sorted later.\n- `c.go:1` Something else breaks here, buffer it.\n- `b.go:9` The retry drops the body here.", want: leads},
		{name: "a ruling without a reason does not count", reply: "- RULED OUT `a.go:3`\n- `b.go:9` The retry drops the body here.", want: leads},
		{
			name:  "prose around the lines is dropped",
			reply: "Here they are:\n- `a.go:3` A loser can outrank the winner here.\n- `b.go:9` The retry drops the body here.\nDone.",
			want:  review.LeadsHeader + "- `a.go:3` A loser can outrank the winner here.\n- `b.go:9` The retry drops the body here.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var prompt string
			turn := func(_ context.Context, p string) (string, error) { prompt = p; return tt.reply, tt.err }
			require.Equal(t, tt.want, checkLeads(t.Context(), leads, turn))
			require.Contains(t, prompt, "codeforge-persona")
			require.Contains(t, prompt, "RULED OUT", "the reviewer is asked to check each lead against the code")
			require.NotContains(t, prompt, review.LeadsHeader, "only the leads are sent, not the header")
		})
	}
	require.Empty(t, checkLeads(t.Context(), "", func(context.Context, string) (string, error) { t.Fatal("no leads, no turn"); return "", nil }))
}

func TestCheckLeads_TwoLeadsAtOnePlaceAreNotChecked(t *testing.T) {
	leads := review.LeadsHeader + "- `a.go:3` the retry drops the body\n- `a.go:3` the lock is held across the call\n"
	require.Equal(t, leads, checkLeads(t.Context(), leads, func(context.Context, string) (string, error) {
		t.Fatal("leads that share a place cannot be told apart in an answer, so none is checked")
		return "", nil
	}))
}

func TestDropCoveredLeads(t *testing.T) {
	text := "- issue (blocking): a.go:520 — the winner can rank below a loser\n\nFix the ranking.\n\nDecision: Request changes"
	tests := []struct{ name, leads, want string }{
		{name: "a lead the review makes itself goes", leads: review.LeadsHeader + "- `a.go:519` ranking is wrong\n", want: ""},
		{name: "one far from any finding stays", leads: review.LeadsHeader + "- `a.go:519` x\n- `a.go:900` y\n", want: review.LeadsHeader + "- `a.go:900` y\n"},
		{name: "another file stays", leads: review.LeadsHeader + "- `b.go:520` z\n", want: review.LeadsHeader + "- `b.go:520` z\n"},
		{name: "no leads", leads: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { require.Equal(t, tt.want, dropCoveredLeads(tt.leads, text)) })
	}
}

func TestLongComments(t *testing.T) {
	words := func(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }
	tests := []struct {
		name string
		text string
		want []longComment
	}{
		{name: "short comments pass", text: "- nitpick: a.go:1 — " + words(30) + "\n\n" + words(50) + "\n\nDecision: Comment"},
		{
			name: "a long finding, its continuation counted",
			text: "- issue (blocking): a.go:3 — " + words(40) + "\n  " + words(30) + "\n\nFine.\n\nDecision: Request changes",
			want: []longComment{{Place: "a.go:3", Words: 70}},
		},
		{
			name: "a code suggestion is not counted",
			text: "- suggestion: a.go:3 — " + words(20) + "\n```suggestion\n" + words(80) + "\n```\n\nOk.",
		},
		{name: "a long final comment", text: "- nitpick: a.go:1 — x\n\n" + words(130) + "\n\nDecision: Comment", want: []longComment{{Words: 130}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { require.Equal(t, tt.want, longComments(tt.text)) })
	}
}

func TestTightenReview(t *testing.T) {
	long := "- issue (blocking): a.go:3 — " + strings.Repeat("too many words ", 30) + "\n- nitpick: b.go:1 — fine\n\nOk.\n\nDecision: Request changes"
	short := "- issue (blocking): a.go:3 — The retry drops the body. Buffer it.\n- nitpick: b.go:1 — fine\n\nOk.\n\nDecision: Request changes"
	tests := []struct {
		name  string
		text  string
		reply string
		err   error
		want  string
	}{
		{name: "nothing long, no turn", text: short, want: short},
		{name: "cut", text: long, reply: short, want: short},
		{name: "a finding lost keeps the original", text: long, reply: "- issue (blocking): a.go:3 — Buffer it.\n\nOk.", want: long},
		{name: "a failed turn keeps the original", text: long, err: errors.New("down"), want: long}, //nolint:err113 // a stand-in failure
		{name: "not shorter keeps the original", text: long, reply: long + " more", want: long},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var prompt string
			turn := func(_ context.Context, p string) (string, error) {
				if tt.reply == "" && tt.err == nil {
					t.Fatal("no long comment, no turn")
				}
				prompt = p
				return tt.reply, tt.err
			}
			require.Equal(t, tt.want, tightenReview(t.Context(), tt.text, turn, nil))
			if prompt != "" {
				require.Contains(t, prompt, "the finding at `a.go:3`: 90 words; keep it under 40")
			}
		})
	}
}

func TestDropCoveredLeads_OnlyABlockingFindingCoversALead(t *testing.T) {
	text := "- nitpick: a.go:10 — rename x\n\nOk."
	leads := review.LeadsHeader + "- `a.go:12` nil deref crashes\n"
	require.Equal(t, leads, dropCoveredLeads(leads, text))
}

func TestTightenReview_ACutNeverChangesALabelTheDecisionOrAnotherFinding(t *testing.T) {
	long := "- issue (blocking): a.go:3 — " + strings.Repeat("too many words ", 30) + "\n- nitpick: b.go:1 — fine as it is\n\nOk.\n\nDecision: Request changes"
	for name, reply := range map[string]string{
		"a label softened":       "- suggestion: a.go:3 — Buffer it.\n- nitpick: b.go:1 — fine as it is\n\nOk.\n\nDecision: Request changes",
		"another finding edited": "- issue (blocking): a.go:3 — Buffer it.\n- nitpick: b.go:1 — fine\n\nOk.\n\nDecision: Request changes",
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, long, tightenReview(t.Context(), long, func(context.Context, string) (string, error) { return reply, nil }, nil))
		})
	}
}

func TestCheckLeads_ARewriteThatSaysNothingKeepsTheOriginal(t *testing.T) {
	leads := review.LeadsHeader + "- `a.go:3` the retry re-sends the read body, so the upload is empty\n"
	require.Equal(t, leads, checkLeads(t.Context(), leads, func(context.Context, string) (string, error) { return "- `a.go:3` Looks fine.", nil }))
}

func TestLeads_ALeadThatCannotBePlacedIsNeverLost(t *testing.T) {
	leads := review.LeadsHeader + "- `a.go:3` \n- `b.go:9` the retry drops the body\n"
	require.Equal(t, leads, checkLeads(t.Context(), leads, func(context.Context, string) (string, error) {
		t.Fatal("a lead that cannot be checked after a rewrite is not rewritten")
		return "", nil
	}))
	require.Contains(t, dropCoveredLeads(leads, "- issue (blocking): b.go:9 — x\n\nOk."), "- `a.go:3`", "kept though it cannot be placed")
}

func TestReviewView_SaysWhyItCannotPost(t *testing.T) {
	old := reviewPostNote
	t.Cleanup(func() { reviewPostNote = old })
	reviewPostNote = noPostNote(false, false)
	a := reviewView(codeStyle{width: 80}, nil, editableReview, "", review.DecisionComment, reviewRound{}, nil, false)
	require.Contains(t, a.Intro, "only a review of a pull request can be posted")
	a = reviewView(codeStyle{width: 80}, nil, editableReview, "", review.DecisionComment, reviewRound{}, nil, true)
	require.NotContains(t, a.Intro, "Posting is off", "a view that can post says nothing")
	require.Contains(t, noPostNote(true, true), "could not be read")
}

func TestApplyEdits_TheFinalCommentCanBeRewrittenOrDeleted(t *testing.T) {
	out, _, edited := applyEdits(editableReview, []forms.Change{{Kind: forms.ChangeEdit, Note: finalNoteID, Text: "Close.\n- Fix the sampler."}})
	require.Contains(t, out, "- nitpick: b.go:2 — rename h\n\nClose.\n- Fix the sampler.\n\nDecision: Request changes")
	require.NotContains(t, out, "Looks close.")
	require.Equal(t, []string{"Close.\n- Fix the sampler."}, edited)

	out, _, _ = applyEdits(editableReview, []forms.Change{{Kind: forms.ChangeDelete, Note: finalNoteID}})
	require.NotContains(t, out, "Looks close.")
	require.True(t, strings.HasSuffix(out, "rename h\n\nDecision: Request changes"), out)

	_, questions, _ := applyEdits(editableReview, []forms.Change{{Kind: forms.ChangeAsk, Note: finalNoteID, Text: "too harsh?"}})
	require.Equal(t, "your final comment", questions[0].About)
}

func TestReviewTurn_RetriesPlainerOnlyWhenTheCommandLineWasWrong(t *testing.T) {
	for _, code := range []int{64, 1} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			dir := t.TempDir()
			engine := filepath.Join(dir, "engine")
			calls := filepath.Join(dir, "calls")
			script := fmt.Sprintf("#!/bin/sh\necho x >> %s\ncase \" $* \" in *\" --odd \"*) exit %d ;; esac\nprintf '%%s' '{\"output\":\"Plain.\"}'\n", calls, code)
			require.NoError(t, os.WriteFile(engine, []byte(script), 0o700))
			reply, err := reviewTurn([]string{engine, "--odd", "--session-id", "rev-1", "--profile", "codeforge-review", "brief"}, os.Environ())(t.Context(), "q")
			n, rerr := os.ReadFile(calls)
			require.NoError(t, rerr)
			if code == exitUsage {
				require.NoError(t, err)
				require.Equal(t, "Plain.", reply)
				require.Equal(t, 2, strings.Count(string(n), "x"), "once as the review ran, once plainer")
				return
			}
			require.Error(t, err)
			require.Equal(t, 1, strings.Count(string(n), "x"), "another failure is not run again")
		})
	}
}

func TestCompactLead(t *testing.T) {
	long := "- `a.go:533` `winnerBonus` sets `rivalTop` from active players only, so a player who left can outrank the winner. " +
		"Example 1, duel: the rival walks 3 tokens home (300) and leaves. The host has 0 and wins by default, so the bonus adds 200. " +
		"The recap then lists the rival first at 300, while `winner_participant_id` names the host. Example 2, party: A reaches 610 and leaves. " +
		"Fix: in `standings()`, sort `finished_participant_ids` first, or take `rivalTop` over every dealt seat."
	require.Equal(t, "- `a.go:533` `winnerBonus` sets `rivalTop` from active players only, so a player who left can outrank the winner. "+
		"In `standings()`, sort `finished_participant_ids` first, or take `rivalTop` over every dealt seat.", compactLead(long))
	short := "- `a.go:3` the retry drops the body. Buffer it."
	require.Equal(t, short, compactLead(short), "a short lead stays as written")
}

func TestCheckLeads_AFailedTurnStillShortensTheLeads(t *testing.T) {
	long := review.LeadsHeader + "- `a.go:3` It breaks. " + strings.Repeat("More detail here. ", 20) + "Fix: buffer the body.\n"
	got := checkLeads(t.Context(), long, func(context.Context, string) (string, error) { return "", errors.New("down") }) //nolint:err113 // a stand-in failure
	require.Equal(t, review.LeadsHeader+"- `a.go:3` It breaks. Buffer the body.\n", got)
}

func TestCompactLead_TheFixIsKeptHoweverItIsWritten(t *testing.T) {
	walk := strings.Repeat("The walkthrough goes on here. ", 12)
	tests := []struct{ in, want string }{
		{in: "- `a.go:3` The retry drops the body. " + walk + "Buffer the body in `retry` before the first attempt.", want: "- `a.go:3` The retry drops the body. Buffer the body in `retry` before the first attempt."},
		{in: "- `a.go:3` The retry drops the body. " + walk + "fix: buffer it. Then more.", want: "- `a.go:3` The retry drops the body. Buffer it."},
		{in: "- `a.go:3` “Retry” drops the body. " + walk + "fix: “buffer” it.", want: "- `a.go:3` “Retry” drops the body. “buffer” it."},
		{in: "- `a.go:3` Calls like e.g. `Close()` leak the fd. " + walk + "Defer it.", want: "- `a.go:3` Calls like e.g. `Close()` leak the fd. Defer it."},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) { require.Equal(t, tt.want, compactLead(tt.in)) })
	}
}

func TestSandboxPrefix(t *testing.T) {
	wrapper, cmd := sandboxPrefix([]string{sandboxExec, "-p", "(version 1)", "empryo", "--session-id", "s1", "brief"})
	require.Equal(t, []string{sandboxExec, "-p", "(version 1)"}, wrapper)
	require.Equal(t, []string{"empryo", "--session-id", "s1", "brief"}, cmd)
	wrapper, cmd = sandboxPrefix([]string{"empryo", "brief"})
	require.Nil(t, wrapper)
	require.Equal(t, []string{"empryo", "brief"}, cmd)
}

func TestReviewTurn_KeepsTheSandboxAroundTheEngine(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "engine")
	got := filepath.Join(dir, "args")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+got+"\nprintf '%s' '{\"output\":\"ok\"}'\n"), 0o700))
	// A stand-in for sandbox-exec: the engine is run as the wrapper would run it, after its own arguments.
	turn := func(ctx context.Context, argv, env []string) (string, error) {
		wrapper, cmd := sandboxPrefix(argv)
		require.Equal(t, []string{sandboxExec, "-p", "(profile)"}, wrapper, "the wrapper comes first, untouched")
		return resumedTurn(ctx, cmd, env)
	}
	argv := []string{sandboxExec, "-p", "(profile)", engine, "--profile", "codeforge-review", "--session-id", "rev-1", "brief"}
	wrapper, cmd := sandboxPrefix(argv)
	require.NotEmpty(t, wrapper)
	reply, err := turn(t.Context(), slices.Concat(wrapper, cmd[:1], []string{"--session", "rev-1"}, withoutPrompt(dropFlag(cmd[1:], "--session-id")), []string{"q"}), os.Environ())
	require.NoError(t, err)
	require.Equal(t, "ok", reply)
	args, err := os.ReadFile(got)
	require.NoError(t, err)
	require.Equal(t, "--session\nrev-1\n--profile\ncodeforge-review\nq\n", string(args))
}

func TestLeads_DeletableInTheView(t *testing.T) {
	leads := review.LeadsHeader + "- `a.go:3` the retry drops the body\n- `b.go:9` a loser can outrank the winner\n"
	a := reviewAccordion(editableReview, leads, codeStyle{width: 100}, nil, nil)
	sec := a.Items[len(a.Items)-1]
	require.Len(t, sec.Notes, 2, "each lead is a note of its own")
	require.Equal(t, "a.go:3", sec.Notes[0].Label)
	require.Empty(t, sec.Notes[0].Raw, "a lead can be deleted or asked about, not rewritten")
	require.Contains(t, sec.Body, "not confirmed them")

	got := withoutDeletedLeads(leads, []forms.Change{{Kind: forms.ChangeDelete, Note: sec.Notes[0].ID}})
	require.Equal(t, strings.TrimSpace(review.LeadsHeader)+"\n\n- `b.go:9` a loser can outrank the winner\n", got)
	require.Empty(t, withoutDeletedLeads(leads, []forms.Change{{Kind: forms.ChangeDelete, Note: sec.Notes[0].ID}, {Kind: forms.ChangeDelete, Note: sec.Notes[1].ID}}),
		"with every lead deleted, there are none")
	require.Equal(t, leads, withoutDeletedLeads(leads, []forms.Change{{Kind: forms.ChangeDelete, Note: "f0"}}), "a finding's deletion leaves the leads alone")
	require.Equal(t, sec.Notes[1].ID, leadID("- `b.go:9` a loser can outrank the winner"), "the same lead keeps its id")
}
