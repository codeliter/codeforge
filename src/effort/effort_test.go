//nolint:err113 // dynamic error messages are okay in tests
package effort

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeJudge struct {
	answer string
	err    error
	gotMsg string
}

func (f *fakeJudge) Ask(_ context.Context, _, user string) (string, error) {
	f.gotMsg = user
	return f.answer, f.err
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name string
		s    Scores
		want Level
	}{
		{"all zero", Scores{}, Low},
		{"small and clear", Scores{Blast: 1, Verify: 1}, Low},
		{"risk two wins", Scores{Risk: 2}, High},
		{"vague wins", Scores{Ambiguity: 2}, High},
		{"hard to verify and broad", Scores{Verify: 2, Blast: 1, Novelty: 1, Risk: 1}, High},
		{"hard to verify but small", Scores{Verify: 2, Blast: 1}, Medium},
		{"high total", Scores{Ambiguity: 1, Blast: 2, Risk: 1, Novelty: 2, Verify: 1}, High},
		{"small open location stays low", Scores{Ambiguity: 1, Blast: 1}, Low},
		{"small tested fix stays low", Scores{Risk: 1, Verify: 1}, Low},
		{"three points is medium", Scores{Ambiguity: 1, Risk: 1, Verify: 1}, Medium},
		{"pattern follow", Scores{Blast: 1, Risk: 1, Verify: 1}, Medium},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Decide(tt.s))
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		answer  string
		err     error
		want    Level
		wantErr bool
	}{
		{"plain json", `{"ambiguity":0,"blast":0,"risk":0,"novelty":0,"verify":0,"reason":"typo"}`, nil, Low, false},
		{"json wrapped in prose", "Sure:\n```json\n{\"risk\":2,\"reason\":\"prod\"}\n```", nil, High, false},
		{"no json falls back to medium", "I cannot", nil, Medium, true},
		{"out of range falls back to medium", `{"risk":3}`, nil, Medium, true},
		{"judge error falls back to medium", "", errors.New("timeout"), Medium, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			judge := &fakeJudge{answer: tt.answer, err: tt.err}
			c, err := New(Options{Judges: []Judge{judge}})
			require.NoError(t, err)

			got, err := c.Classify(t.Context(), "ABC-1 fix it", Signals{ChangedFiles: 3})
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got.Level)
			require.Contains(t, judge.gotMsg, "<task>\nABC-1 fix it\n</task>")
			require.Contains(t, judge.gotMsg, "changed_files=3")
		})
	}
}

func TestNew_RequiresJudge(t *testing.T) {
	_, err := New(Options{})
	require.ErrorIs(t, err, errNoJudge)
}

func TestMerge(t *testing.T) {
	got := Merge([]Scores{
		{Ambiguity: 0, Blast: 1, Risk: 0, Novelty: 0, Verify: 2, Reason: "a"},
		{Ambiguity: 1, Blast: 1, Risk: 2, Novelty: 0, Verify: 1, Reason: "b"},
	})
	require.Equal(t, Scores{Ambiguity: 1, Blast: 1, Risk: 2, Novelty: 0, Verify: 2, Reason: "a | b"}, got)
}

func TestClassify_Panel(t *testing.T) {
	low := &fakeJudge{answer: `{"reason":"tiny"}`}
	risky := &fakeJudge{answer: `{"risk":2,"reason":"prod"}`}
	broken := &fakeJudge{err: errors.New("down")}

	c, err := New(Options{Judges: []Judge{low, risky, broken}})
	require.NoError(t, err)

	got, err := c.Classify(t.Context(), "bump replicas", Signals{})
	require.NoError(t, err)
	require.Equal(t, High, got.Level)
	require.Len(t, got.Votes, 2)
}

func TestClassify_AllJudgesFail(t *testing.T) {
	c, err := New(Options{Judges: []Judge{&fakeJudge{err: errors.New("a")}, &fakeJudge{answer: "nope"}}})
	require.NoError(t, err)

	got, err := c.Classify(t.Context(), "x", Signals{})
	require.Error(t, err)
	require.Equal(t, Medium, got.Level)
}
