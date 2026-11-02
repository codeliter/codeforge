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

func TestState_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	st, err := Load(dir)
	require.NoError(t, err)

	st.PassGate("/r", "abc", "", "ok", time.Now())
	require.NoError(t, st.Save(dir))

	got, err := Load(dir)
	require.NoError(t, err)
	require.Equal(t, "abc", got.Gates["/r"].Tree)
}

func TestState_LoadIgnoresRetiredApprovals(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, stateFile), []byte(`{"approvals":{"x":{"rule":"open-pr"}},"gates":{}}`), 0o600))
	_, err := Load(dir)
	require.NoError(t, err)
}

func TestDecide_ReadOnlyOffersNoAllow(t *testing.T) {
	d := Decide(shell(t, t.TempDir(), "rm a.txt"), true)
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

func TestDecide_PRsMergesCommentsAndInfraNeedNoApproval(t *testing.T) {
	repo := gitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "dirty.go"), []byte("package x"), 0o600))

	for _, cmd := range []string{
		"gh pr create --fill", "echo x > a && gh pr create --fill", "/opt/homebrew/bin/gh pr create --fill",
		"gh pr merge 1 && gh pr merge 2", "gh pr comment 1 -b x", "terraform apply", "kubectl delete pod x",
	} {
		require.True(t, Decide(shell(t, repo, cmd), false).Allow, cmd)
	}
	for _, cmd := range []string{"gh pr create --fill", "gh pr merge 1", "terraform apply"} {
		d := Decide(shell(t, repo, cmd), true)
		require.False(t, d.Allow, cmd)
		require.NotContains(t, d.Reason, "codeforge allow", cmd)
	}
}

func TestDecide_ScriptPRCreationNeedsNoApproval(t *testing.T) {
	in := input(t, "code_script", map[string]any{"code": `await api.run('gh pr create --fill')`})
	in.Cwd = gitRepo(t)
	require.True(t, Decide(in, false).Allow)
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
