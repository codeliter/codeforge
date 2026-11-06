//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/review"
)

func TestRepoFromURL(t *testing.T) {
	require.Equal(t, "acme/api", repoFromURL("https://github.com/acme/api/pull/1"))
	require.Empty(t, repoFromURL("42"))
}

func TestReviewArgs_RejectsBadRef(t *testing.T) {
	_, err := reviewArgs(t.Context(), "", ReviewConfig{}, nil, nil, []string{"42;rm"})
	require.ErrorIs(t, err, review.ErrNoPR)
}

func TestReviewArgs_RejectsEffortAboveCeiling(t *testing.T) {
	_, err := reviewArgs(t.Context(), "", ReviewConfig{}, nil, nil, []string{"42", "--effort", "max"})
	require.ErrorIs(t, err, errEffortAboveCeiling)
}

func TestReviewArgs_FlagValueIsNotThePR(t *testing.T) {
	_, err := reviewArgs(t.Context(), "", ReviewConfig{}, nil, nil, []string{"--max-steps", "1", "x"})
	require.ErrorIs(t, err, review.ErrNoPR, "x is the ref, not the --max-steps value")
}

func TestWithSystemNote(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"adds one", []string{"--model", "m", "brief"}, []string{"--system", "note", "--model", "m", "brief"}},
		{"extends the persona", []string{"--system", "persona", "brief"}, []string{"--system", "persona\n\nnote", "brief"}},
		{"extends the persona given with equals", []string{"--system=persona", "brief"}, []string{"--system=persona\n\nnote", "brief"}},
		{"value of another flag is not a system prompt", []string{"--model", "--system", "brief"}, []string{"--system", "note", "--model", "--system", "brief"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, withSystemNote(tt.args, "note"))
		})
	}
}

func TestReviewTimeNote(t *testing.T) {
	require.Contains(t, review.LimitNote(), "within 180 seconds")
	require.Contains(t, review.WithBudget("brief", review.TierRisky, 60*time.Second), "Time limit")
}

func TestClampFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"absent is set", []string{"--model", "m"}, []string{"--model", "m", "--timeout", "1000"}},
		{"looser value is lowered", []string{"--timeout", "600000", "--model", "m"}, []string{"--model", "m", "--timeout", "1000"}},
		{"equals form is lowered", []string{"--timeout=600000", "--model", "m"}, []string{"--model", "m", "--timeout", "1000"}},
		{"stricter value stays", []string{"--timeout", "500"}, []string{"--timeout", "500"}},
		{"junk value is replaced", []string{"--timeout", "soon"}, []string{"--timeout", "1000"}},
		{"another flag's value is kept", []string{"--model", "--timeout", "x"}, []string{"--model", "--timeout", "x", "--timeout", "1000"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, clampFlag(tt.args, "--timeout", 1000))
		})
	}
}
