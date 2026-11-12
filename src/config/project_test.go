package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeProject(t *testing.T, project, body string) string {
	t.Helper()
	path := ProjectPath(project)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestTrust_RoundTripsTheDigest(t *testing.T) {
	home := t.TempDir()
	ledger, err := LoadTrust(home)
	require.NoError(t, err)
	require.Empty(t, ledger)

	require.NoError(t, Trust(home, "/repo/a", "one"))
	require.NoError(t, Trust(home, "/repo/b", "two"))
	ledger, err = LoadTrust(home)
	require.NoError(t, err)
	require.Equal(t, Store{"/repo/a": Digest("one"), "/repo/b": Digest("two")}, ledger)
	require.NotEqual(t, Digest("one"), Digest("two"))
}

func TestLoadTrust_ABrokenLedgerIsAnError(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(Dir(home), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(Dir(home), trustFile), []byte("{"), 0o600))
	_, err := LoadTrust(home)
	require.Error(t, err)
}

func TestReadTrustedProject_AMissingFileIsEmpty(t *testing.T) {
	cfg, err := ReadTrustedProject(t.TempDir(), t.TempDir(), nil)
	require.NoError(t, err)
	require.Equal(t, Config{}, cfg)
}

func TestReadTrustedProject_UnapprovedIsSkippedWithNobodyToAsk(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	writeProject(t, project, "model: a/code\n")
	cfg, err := ReadTrustedProject(home, project, nil)
	require.NoError(t, err)
	require.Empty(t, cfg.Model)
}

func TestReadTrustedProject_ARefusalLeavesItUntrusted(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	path := writeProject(t, project, "model: a/code\n")
	asked := 0
	cfg, err := ReadTrustedProject(home, project, func(got, preview string) (bool, error) {
		asked++
		require.Equal(t, path, got)
		require.Contains(t, preview, "a/code")
		return false, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, asked)
	require.Empty(t, cfg.Model)
	ledger, err := LoadTrust(home)
	require.NoError(t, err)
	require.Empty(t, ledger)
}

func TestReadTrustedProject_ApprovalIsRememberedUntilTheFileChanges(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	writeProject(t, project, "model: a/code\n")
	asked := 0
	yes := func(string, string) (bool, error) { asked++; return true, nil }

	cfg, err := ReadTrustedProject(home, project, yes)
	require.NoError(t, err)
	require.Equal(t, "a/code", cfg.Model)

	cfg, err = ReadTrustedProject(home, project, yes)
	require.NoError(t, err)
	require.Equal(t, "a/code", cfg.Model)
	require.Equal(t, 1, asked, "an approved file is not asked about again")

	writeProject(t, project, "model: b/other\n")
	cfg, err = ReadTrustedProject(home, project, nil)
	require.NoError(t, err)
	require.Empty(t, cfg.Model, "changed bytes need a new approval")
}

func TestApproveProject_TrustsAValidFileOnly(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	require.Error(t, ApproveProject(home, project), "nothing to approve")

	writeProject(t, project, "model: not!a/model/ref\n")
	require.Error(t, ApproveProject(home, project))
	ledger, err := LoadTrust(home)
	require.NoError(t, err)
	require.Empty(t, ledger)

	path := writeProject(t, project, "model: a/code\n")
	require.NoError(t, ApproveProject(home, project))
	ledger, err = LoadTrust(home)
	require.NoError(t, err)
	require.Equal(t, Digest("model: a/code\n"), ledger[path])
}

func TestEnsureProjectEditing(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	never := func() bool { t.Fatal("asked to replace"); return false }
	require.NoError(t, EnsureProjectEditing(home, project, never), "no file yet")

	writeProject(t, project, "model: a/code\n")
	require.ErrorIs(t, EnsureProjectEditing(home, project, func() bool { return false }), ErrUnapprovedEdit)
	require.NoError(t, EnsureProjectEditing(home, project, func() bool { return true }))

	require.NoError(t, ApproveProject(home, project))
	require.NoError(t, EnsureProjectEditing(home, project, never), "approved bytes need no question")
}

func TestLoad_AProjectOverridesTheGlobalFileOnceApproved(t *testing.T) {
	t.Setenv("CODEFORGE_MODEL", "") // an override in the developer's shell must not decide this
	home, project := t.TempDir(), t.TempDir()
	writeConfig(t, home, `{"model":"a/global"}`)
	writeProject(t, project, "model: a/local\n")

	cfg, err := Load(home, project, nil)
	require.NoError(t, err)
	require.Equal(t, "a/global", cfg.Model)

	require.NoError(t, ApproveProject(home, project))
	cfg, err = Load(home, project, nil)
	require.NoError(t, err)
	require.Equal(t, "a/local", cfg.Model)
}

func TestSameScope(t *testing.T) {
	home := t.TempDir()
	require.True(t, SameScope(home, home))
	require.True(t, SameScope(home, filepath.Join(home, ".")))
	require.False(t, SameScope(home, t.TempDir()))
}

func TestLoadBase_AppliesDefaultsButNotTheEnvironment(t *testing.T) {
	t.Setenv("CODEFORGE_MODEL", "x/env")
	cfg, err := LoadBase(t.TempDir())
	require.NoError(t, err)
	require.Equal(t, DefaultModel, cfg.Model)
}
