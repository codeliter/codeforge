package guard

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCheckGH(t *testing.T) {
	repo := gitRepo(t)
	now := time.Now()
	st, err := Load(t.TempDir())
	require.NoError(t, err)

	require.NoError(t, CheckGH(t.Context(), st, repo, []string{"pr", "view", "1"}, now))
	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"pr", "merge", "1"}, now), ErrGHRefused)
	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"api", "repos/{owner}/{repo}/issues/1/comments", "-fbody=x"}, now), ErrGHRefused)
	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"pr", "create", "--fill"}, now), ErrGHRefused, "not reviewed")

	st.PassGate(repo, "tree", gitHead(t.Context(), repo), "ok", now)
	require.NoError(t, CheckGH(t.Context(), st, repo, []string{"pr", "create", "--fill"}, now))
	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"pr", "create", "--head", "other"}, now), ErrGHRefused)

	require.NoError(t, os.WriteFile(filepath.Join(repo, "dirty.go"), []byte("x"), 0o600))
	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"pr", "create", "--fill"}, now), ErrGHRefused, "dirty tree")

	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"land", "123"}, now), ErrGHRefused, "alias")
	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"alias", "set", "land", "pr merge"}, now), ErrGHRefused)

	st.Allow(repo, RuleMerge, now)
	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"-R", "owner/b", "pr", "merge", "1"}, now), ErrGHRefused, "cross-repo merge")
	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"pr", "merge", "1", "--repo", "owner/b"}, now), ErrGHRefused, "trailing --repo")
	t.Setenv("GH_REPO", "owner/b")
	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"pr", "merge", "1"}, now), ErrGHRefused, "GH_REPO")
	t.Setenv("GH_REPO", "")
	require.True(t, st.Approved(repo, RuleMerge, now), "refused calls spend nothing")
	require.NoError(t, CheckGH(t.Context(), st, repo, []string{"pr", "merge", "1"}, now))
	require.ErrorIs(t, CheckGH(t.Context(), st, repo, []string{"pr", "merge", "2"}, now), ErrGHRefused, "approval spent")
}
