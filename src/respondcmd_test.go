package src

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHelpListsRespond(t *testing.T) {
	require.Contains(t, helpText, "respond [PR] [--watch]")
}

func TestRunAllow_NeedsATerminal(t *testing.T) {
	err := runAllow(t.Context(), t.TempDir(), []string{"merge"})
	require.True(t, errors.Is(err, errAllowNeedsTerminal) || errors.Is(err, errAllowNotConfirmed),
		"without a person typing the code, nothing is approved: %v", err)
}

func TestOneTimeCode(t *testing.T) {
	a, err := oneTimeCode()
	require.NoError(t, err)
	require.Len(t, a, codeDigits)
}
