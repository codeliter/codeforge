//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/review"
)

func TestRepoFromURL(t *testing.T) {
	require.Equal(t, "acme/api", repoFromURL("https://github.com/acme/api/pull/1"))
	require.Empty(t, repoFromURL("42"))
}

func TestReviewArgs_RejectsBadRef(t *testing.T) {
	_, err := reviewArgs(t.Context(), ReviewConfig{}, []string{"42;rm"})
	require.ErrorIs(t, err, review.ErrNoPR)
}

func TestReviewArgs_RejectsEffortAboveCeiling(t *testing.T) {
	_, err := reviewArgs(t.Context(), ReviewConfig{}, []string{"42", "--effort", "max"})
	require.ErrorIs(t, err, errEffortAboveCeiling)
}

func TestReviewArgs_FlagValueIsNotThePR(t *testing.T) {
	_, err := reviewArgs(t.Context(), ReviewConfig{}, []string{"--max-steps", "1", "x"})
	require.ErrorIs(t, err, review.ErrNoPR, "x is the ref, not the --max-steps value")
}
