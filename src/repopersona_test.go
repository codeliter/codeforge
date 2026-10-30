//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeRepoPersona(t *testing.T, root, body string) string {
	t.Helper()
	p := filepath.Join(root, repoPersonaPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestLoadRepoPersona(t *testing.T) {
	root := t.TempDir()

	got, err := loadRepoPersona(root)
	require.NoError(t, err)
	require.Empty(t, got)

	writeRepoPersona(t, root, "\n# Repo\nUse Conventional Commits.\n\n")
	got, err = loadRepoPersona(root)
	require.NoError(t, err)
	require.Equal(t, "# Repo\nUse Conventional Commits.", got)
}

func TestLoadRepoPersona_TooLarge(t *testing.T) {
	root := t.TempDir()
	writeRepoPersona(t, root, strings.Repeat("x", maxRepoPersonaBytes+1))

	_, err := loadRepoPersona(root)
	require.ErrorIs(t, err, errRepoPersonaSize)
}

func TestTrusted(t *testing.T) {
	home := t.TempDir()
	p := "/repo/.codeforge/persona.md"

	ok, err := trusted(home, p, "v1")
	require.NoError(t, err)
	require.False(t, ok, "unapproved persona must be skipped without a terminal")

	require.NoError(t, trust(home, p, "v1"))
	ok, err = trusted(home, p, "v1")
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = trusted(home, p, "v2")
	require.NoError(t, err)
	require.False(t, ok, "a changed persona needs approval again")
}

func TestSaveRepoPersona(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()

	p, err := saveRepoPersona(home, root, []byte("# Repo\nRules.\n"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, repoPersonaPath), p)

	ok, err := trusted(home, p, "# Repo\nRules.")
	require.NoError(t, err)
	require.True(t, ok, "the author's own persona is trusted")

	_, err = saveRepoPersona(home, root, []byte("other"))
	require.ErrorIs(t, err, errRepoPersonaExists)
}

func TestWithRepoPersona(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Chdir(root)

	got, err := withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.Equal(t, []string{"task"}, got, "no persona leaves args alone")

	writeRepoPersona(t, root, "Use tabs.")
	got, err = withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.Equal(t, []string{"task"}, got, "untrusted persona is skipped")

	resolved, err := repoRoot(t.Context())
	require.NoError(t, err)
	require.NoError(t, trust(home, filepath.Join(resolved, repoPersonaPath), "Use tabs."))
	got, err = withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.Equal(t, "--system", got[0])
	require.Contains(t, got[1], "<repo_persona>\nUse tabs.\n</repo_persona>")
	require.Contains(t, got[1], "never override hard stops")
	require.Equal(t, "task", got[2])

	got, err = withRepoPersona(t.Context(), home, []string{"--system", "mine", "task"})
	require.NoError(t, err)
	require.Equal(t, []string{"--system", "mine", "task"}, got, "an explicit --system wins")
}

func TestRepoPersonaSeed(t *testing.T) {
	blank, err := repoPersonaSeed("")
	require.NoError(t, err)
	require.Contains(t, string(blank), "# Repository persona")

	oss, err := repoPersonaSeed("oss")
	require.NoError(t, err)
	require.Contains(t, string(oss), "Open-source maintainer")

	_, err = repoPersonaSeed("nope")
	require.ErrorIs(t, err, errUnknownPersona)
}

func TestInitFromArgs_Repo(t *testing.T) {
	ans, err := initFromArgs([]string{"--repo"})
	require.NoError(t, err)
	require.Equal(t, scopeRepo, ans.scope)
	require.Equal(t, sourceBlank, ans.source)

	ans, err = initFromArgs([]string{"--repo", "--persona", "startup"})
	require.NoError(t, err)
	require.Equal(t, sourceStarter, ans.source)
	require.Equal(t, "startup", ans.starter)

	_, err = initFromArgs([]string{"--repo", "../other"})
	require.ErrorIs(t, err, errInitUsage)
}
