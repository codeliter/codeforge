package src

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHelpListsRespond(t *testing.T) {
	require.Contains(t, helpText, "respond [PR] [--watch]")
}

func TestAllow_IsRemoved(t *testing.T) {
	for _, rule := range []string{"open-pr", "merge"} {
		require.ErrorIs(t, Run(t.Context(), []string{"allow", rule}), errAllowRemoved, rule)
	}
	require.NotContains(t, helpText, "allow RULE")
	require.Contains(t, helpText, "Pushing, opening PRs")
}
