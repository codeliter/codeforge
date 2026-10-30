package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run())
	}

	root, err := RepoRoot(t.Context(), dir)
	require.NoError(t, err)
	return root
}

func shell(t *testing.T, repo, cmd string) Input {
	t.Helper()
	in := input(t, "Bash", map[string]any{"command": cmd})
	in.Cwd = repo
	return in
}

func TestState_PruneExpired(t *testing.T) {
	st, err := Load(t.TempDir())
	require.NoError(t, err)
	now := time.Now()

	st.Allow("/r", RuleMerge, now)
	require.False(t, st.PruneExpired(now))
	require.True(t, st.PruneExpired(now.Add(ApprovalTTL+time.Second)))
	require.Empty(t, st.Approvals)
}

func TestState_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	st, err := Load(dir)
	require.NoError(t, err)

	now := time.Now()
	st.Allow("/r", RuleMerge, now)
	st.PassGate("/r", "abc", "", "ok", now)
	require.NoError(t, st.Save(dir))

	got, err := Load(dir)
	require.NoError(t, err)
	require.True(t, got.GatePassed("/r", "abc"))
	require.True(t, got.Consume("/r", RuleMerge, now))
}

func TestDecide_ReadOnlyOffersNoAllow(t *testing.T) {
	st, err := Load(t.TempDir())
	require.NoError(t, err)

	d := Decide(t.Context(), st, shell(t, t.TempDir(), "rm a.txt"), true, time.Now())
	require.False(t, d.Allow)
	require.NotContains(t, d.Reason, "codeforge allow")
	require.Contains(t, d.Reason, "read-only")
}

func TestState_ParallelSaves(t *testing.T) {
	dir := t.TempDir()
	st, err := Load(dir)
	require.NoError(t, err)

	errs := make(chan error, 20)
	for range 20 {
		go func() { errs <- st.Save(dir) }()
	}
	for range 20 {
		require.NoError(t, <-errs)
	}
}

func TestDecide_OneApprovalCoversOneAction(t *testing.T) {
	repo := gitRepo(t)
	st, err := Load(t.TempDir())
	require.NoError(t, err)
	now := time.Now()

	st.Allow(repo, RuleMerge, now)
	require.False(t, Decide(t.Context(), st, shell(t, repo, "gh pr merge 1 && gh pr merge 2"), false, now).Allow)
	require.True(t, st.Approved(repo, RuleMerge, now), "the rejected batch spends nothing")
	require.True(t, Decide(t.Context(), st, shell(t, repo, "gh pr merge 1"), false, now).Allow)
}

func TestTreeID_RedistributedBytesChangeFingerprint(t *testing.T) {
	repo := gitRepo(t)
	write := func(a, b string) string {
		require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte(a), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(repo, "b.txt"), []byte(b), 0o600))
		id, err := TreeID(t.Context(), repo)
		require.NoError(t, err)
		return id
	}

	require.NotEqual(t, write("a", "bc"), write("ab", "c"))
}

func TestDecide_OpenPRNeedsCleanTree(t *testing.T) {
	repo := gitRepo(t)
	st, err := Load(t.TempDir())
	require.NoError(t, err)
	now := time.Now()

	require.NoError(t, os.WriteFile(filepath.Join(repo, "fix.go"), []byte("package x"), 0o600))
	tree, err := TreeID(t.Context(), repo)
	require.NoError(t, err)
	st.PassGate(repo, tree, "", "ok", now)

	require.False(t, Decide(t.Context(), st, shell(t, repo, "gh pr create --fill"), false, now).Allow)
}

func TestDecide_ReviewedPRCreationSucceeds(t *testing.T) {
	repo := gitRepo(t)
	st, err := Load(t.TempDir())
	require.NoError(t, err)
	now := time.Now()
	tree, err := TreeID(t.Context(), repo)
	require.NoError(t, err)
	st.PassGate(repo, tree, "", "ok", now)

	require.True(t, Decide(t.Context(), st, shell(t, repo, "gh pr create --fill"), false, now).Allow)
	require.False(t, Decide(t.Context(), st, shell(t, repo, "echo x > a && gh pr create --fill"), false, now).Allow)
}

func TestTreeID_IgnoresEmpryoState(t *testing.T) {
	repo := gitRepo(t)
	before, err := TreeID(t.Context(), repo)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".empryo", "sessions"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".empryo", "sessions", "x.jsonl"), []byte("{}"), 0o600))
	after, err := TreeID(t.Context(), repo)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestDecide_DirectGHPathIsRefused(t *testing.T) {
	repo := gitRepo(t)
	st, err := Load(t.TempDir())
	require.NoError(t, err)
	now := time.Now()

	st.Allow(repo, RuleMerge, now)
	require.False(t, Decide(t.Context(), st, shell(t, repo, "/opt/homebrew/bin/gh pr merge 1"), false, now).Allow)
	require.True(t, Decide(t.Context(), st, shell(t, repo, "/opt/homebrew/bin/gh pr view 1"), false, now).Allow, "reads are fine")
	require.True(t, Decide(t.Context(), st, shell(t, repo, "gh pr merge 1"), false, now).Allow)
}

func TestDecide_ScriptGHApprovalSurvivesForShim(t *testing.T) {
	repo := gitRepo(t)
	st, err := Load(t.TempDir())
	require.NoError(t, err)
	now := time.Now()
	st.Allow(repo, RuleMerge, now)

	in := input(t, "code_script", map[string]any{"code": `await api.run('gh pr merge 1')`})
	in.Cwd = repo
	require.True(t, Decide(t.Context(), st, in, false, now).Allow)
	require.True(t, st.Approved(repo, RuleMerge, now))

	require.NoError(t, CheckGH(t.Context(), st, repo, []string{"pr", "merge", "1"}, now))
	require.False(t, st.Approved(repo, RuleMerge, now), "spent exactly once, by the shim")
}
