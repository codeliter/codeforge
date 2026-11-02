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
	} {
		require.NoError(t, CheckGH(argv), argv)
	}

	t.Setenv("GH_REPO", "owner/b")
	require.NoError(t, CheckGH([]string{"pr", "create", "--fill"}), "GH_REPO needs no approval either")

	require.ErrorIs(t, CheckGH([]string{"land", "123"}), ErrGHRefused, "alias")
	require.ErrorIs(t, CheckGH([]string{"alias", "set", "land", "pr merge"}), ErrGHRefused)
}
