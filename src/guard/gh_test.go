package guard

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckGH(t *testing.T) {
	for _, argv := range [][]string{
		{"pr", "view", "1"}, {"pr", "merge", "1"}, {"-R", "owner/b", "pr", "merge", "1"},
		{"pr", "create", "--fill"}, {"pr", "create", "--head", "other"}, {"-R", "owner/b", "pr", "create", "--fill"},
		{"api", "repos/{owner}/{repo}/issues/1/comments", "-fbody=x"}, {"api", "repos/{owner}/{repo}/pulls", "-ftitle=x"},
		{"api", "graphql", "-f", "query=mutation{x}"}, {"alias", "list"},
		{"stack", "submit"}, {"-R", "owner/b", "stack", "view"},
	} {
		require.NoError(t, CheckGH(argv), argv)
	}
	require.Empty(t, rules(Check("gh stack submit", false)), "the stack extension is allowed by name")
	require.True(t, DecideOutsideReview(input(t, "Bash", map[string]any{"command": "gh stack sync"})).Allow)
	for _, cmd := range []string{"gh stack submit", "gh stack view"} {
		require.Equal(t, []string{RuleReadOnly}, rules(Check(cmd, true)), "an extension runs its own code, so review mode refuses all of it: "+cmd)
	}

	t.Setenv("GH_REPO", "owner/b")
	require.NoError(t, CheckGH([]string{"pr", "create", "--fill"}), "GH_REPO needs no approval either")

	require.ErrorIs(t, CheckGH([]string{"land", "123"}), ErrGHRefused, "alias")
	require.ErrorIs(t, CheckGH([]string{"alias", "set", "land", "pr merge"}), ErrGHRefused)
}
