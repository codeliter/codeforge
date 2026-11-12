//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codeliter/codeforge/src/config"

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

	require.NoError(t, config.Trust(home, p, "v1"))
	ok, err = trusted(home, p, "v1")
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = trusted(home, p, "v2")
	require.NoError(t, err)
	require.False(t, ok, "a changed persona needs approval again")
}

func TestSaveRepoPersona(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()

	p, err := saveRepoPersona(t.Context(), home, root, []byte("# Repo\nRules.\n"))
	require.NoError(t, err)
	require.Equal(t, privateRepoPersonaPath(t.Context(), home, root), p, "a new persona is kept outside the repository")
	require.True(t, strings.HasPrefix(p, config.Dir(home)))
	_, err = os.Stat(filepath.Join(root, ".codeforge"))
	require.ErrorIs(t, err, os.ErrNotExist, "nothing is added to the repository")

	_, err = saveRepoPersona(t.Context(), home, root, []byte("other"))
	require.ErrorIs(t, err, errRepoPersonaExists)

	scaffold := t.TempDir()
	shared := writeRepoPersona(t, scaffold, "")
	p, err = saveRepoPersona(t.Context(), home, scaffold, []byte("# Repo\nRules.\n"))
	require.NoError(t, err)
	require.Equal(t, shared, p, "a scaffold already in the repository is filled where it is")
	ok, err := trusted(home, p, "# Repo\nRules.")
	require.NoError(t, err)
	require.True(t, ok, "the author's own persona is trusted")
}

func TestPersonaShare_MovesYourPersonaIntoTheRepository(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Chdir(root)
	resolved, err := repoRoot(t.Context())
	require.NoError(t, err)
	var out strings.Builder
	require.ErrorIs(t, runPersona(t.Context(), home, []string{"share"}, &out), errNothingToShare)

	private, err := saveRepoPersona(t.Context(), home, resolved, []byte("# Mine\n\n## Voice\nPlain.\n\n## Code style\nTabs.\n"))
	require.NoError(t, err)
	got, err := withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.Contains(t, got[1], "<my_repo_persona>\n# Mine", "your own persona needs no approval")
	require.NotContains(t, got[1], "<repo_persona>")

	require.NoError(t, runPersona(t.Context(), home, []string{"share"}, &out))
	shared, err := os.ReadFile(filepath.Join(resolved, repoPersonaPath))
	require.NoError(t, err)
	require.Equal(t, "# Mine\n\n## Voice\nPlain.\n\n## Code style\nTabs.\n", string(shared), "every section is shared")
	_, err = os.Stat(private)
	require.NoError(t, err, "sharing never removes what you wrote")
	got, err = withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.Contains(t, got[1], "<repo_persona>\n# Mine", "and it is trusted, since its author shared it")
	require.NoError(t, runPersona(t.Context(), home, []string{"forget"}, &out))
	got, err = withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.NotContains(t, got[1], "<my_repo_persona>")
	require.ErrorIs(t, runPersona(t.Context(), home, []string{"share", "x"}, &out), errPersonaUsage)
}

func TestWithRepoPersona_TheTeamsWinsOverYourOwn(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Chdir(root)
	resolved, err := repoRoot(t.Context())
	require.NoError(t, err)
	p := privateRepoPersonaPath(t.Context(), home, resolved)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), privateDir))
	require.NoError(t, os.WriteFile(p, []byte("Use spaces."), privateFile))
	writeRepoPersona(t, resolved, "Use tabs.")
	require.NoError(t, config.Trust(home, filepath.Join(resolved, repoPersonaPath), "Use tabs."))

	got, err := withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.Contains(t, got[1], "Where they differ, the team's wins.")
	require.Less(t, strings.Index(got[1], "<repo_persona>\nUse tabs."), strings.Index(got[1], "<my_repo_persona>\nUse spaces."))

	var out strings.Builder
	require.ErrorIs(t, runPersonaShare(t.Context(), home, &out), errRepoPersonaExists, "sharing never replaces what the team has")
	require.ErrorIs(t, createShared(home, resolved, []byte("x")), errRepoPersonaExists, "nor a file that appeared after the check")
	kept, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "Use spaces.", string(kept), "a refused share leaves your copy alone")

	out.Reset()
	require.NoError(t, runPersona(t.Context(), home, nil, &out))
	require.Contains(t, out.String(), "the shared one below wins")
	writeRepoPersona(t, resolved, "Use tabs, changed.")
	out.Reset()
	require.NoError(t, runPersona(t.Context(), home, nil, &out))
	require.NotContains(t, out.String(), "the shared one below wins", "an unapproved shared persona is not in effect, so it wins nothing")
}

func TestPersonaShare_RefusesWhatCouldNotBeLoaded(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Chdir(root)
	resolved, err := repoRoot(t.Context())
	require.NoError(t, err)
	p := privateRepoPersonaPath(t.Context(), home, resolved)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), privateDir))
	require.NoError(t, os.WriteFile(p, []byte(strings.Repeat("x", maxRepoPersonaBytes+1)), privateFile))
	var out strings.Builder
	require.ErrorIs(t, runPersonaShare(t.Context(), home, &out), errRepoPersonaSize)
	_, err = os.Stat(p)
	require.NoError(t, err, "your copy is kept")
	_, err = os.Stat(filepath.Join(resolved, repoPersonaPath))
	require.ErrorIs(t, err, os.ErrNotExist, "and nothing is written to the repository")

	require.NoError(t, os.WriteFile(p, []byte("Use spaces."), privateFile))
	scaffold := writeRepoPersona(t, resolved, "")
	err = runPersonaShare(t.Context(), home, &out)
	require.ErrorIs(t, err, errRepoPersonaExists, "a scaffold in the repository is the team's to remove")
	require.ErrorContains(t, err, "unfilled scaffold")
	data, err := os.ReadFile(scaffold)
	require.NoError(t, err)
	require.Empty(t, data)
	_, err = os.Stat(p)
	require.NoError(t, err)
}

func TestOriginKey(t *testing.T) {
	tests := []struct{ origin, want string }{
		{"git@github.com:acme/api.git", "git@github.com:acme/api.git"},
		{"https://github.com/acme/api", "https://github.com/acme/api"},
		{"ssh://alice@host:2222/~/acme/api.git", "ssh://alice@host:2222/~/acme/api.git"},
		{"/srv/git/api.git", "/srv/git/api.git"},
		{"file:///srv/git/api.git", "file:///srv/git/api.git"},
		{"../api", "/work/api"},
		{"../sub/../api", "/work/api"},
		{"vendor/up.git", "/work/repo/vendor/up.git"},
	}
	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			require.Equal(t, tt.want, originKey("/work/repo", tt.origin))
		})
	}
}

func TestPrivateRepoPersonaPath_ALongNameStillFitsOneComponent(t *testing.T) {
	root := filepath.Join(t.TempDir(), strings.Repeat("n", 250))
	require.Less(t, len(filepath.Base(filepath.Dir(privateRepoPersonaPath(t.Context(), t.TempDir(), root)))), 64)
}

func TestPrivateRepoPersonaPath_FollowsTheOriginNotThePath(t *testing.T) {
	home := t.TempDir()
	clone := func(url string) string {
		dir := filepath.Join(t.TempDir(), "work") // its own parent, so a relative origin points somewhere of its own
		for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "remote", "add", "origin", url}} {
			out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput()
			require.NoError(t, err, string(out))
		}
		return dir
	}
	for _, relative := range []string{"upstream.git", "vendor/upstream.git", "./up"} {
		require.NotEqual(t, privateRepoPersonaPath(t.Context(), home, clone(relative)), privateRepoPersonaPath(t.Context(), home, clone(relative)), relative)
	}
	require.NotEqual(t, privateRepoPersonaPath(t.Context(), home, clone("/srv/git/api")), privateRepoPersonaPath(t.Context(), home, clone("/srv/git/api.git")), "two local directories are two repositories")
	require.NotEqual(t, privateRepoPersonaPath(t.Context(), home, clone("https://github.com/acme/api.git")), privateRepoPersonaPath(t.Context(), home, clone("https://gitlab.com/acme/api.git")), "the same name on another host is another repository")
	for _, same := range []string{"https://example.test/acme/api.git", "ssh://git@example.test/acme/api", "/srv/git/api.git"} {
		require.Equal(t, privateRepoPersonaPath(t.Context(), home, clone(same)), privateRepoPersonaPath(t.Context(), home, clone(same)), same)
	}
	a, b := clone("git@github.com:acme/api.git"), clone("git@github.com:acme/api.git")
	require.Equal(t, privateRepoPersonaPath(t.Context(), home, a), privateRepoPersonaPath(t.Context(), home, b), "two clones of one repository share the persona")
	require.Contains(t, privateRepoPersonaPath(t.Context(), home, a), filepath.Join(".codeforge", "repos")+string(filepath.Separator))
	require.NotEqual(t, privateRepoPersonaPath(t.Context(), home, a), privateRepoPersonaPath(t.Context(), home, clone("git@github.com:other/api.git")))
	require.NotEqual(t, privateRepoPersonaPath(t.Context(), home, t.TempDir()), privateRepoPersonaPath(t.Context(), home, t.TempDir()), "without a remote the path tells repositories apart")
	require.NotEqual(t, privateRepoPersonaPath(t.Context(), home, clone("../upstream.git")), privateRepoPersonaPath(t.Context(), home, clone("../upstream.git")),
		"a relative origin means a different repository from a different place")
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
	require.NoError(t, config.Trust(home, filepath.Join(resolved, repoPersonaPath), "Use tabs."))
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
	require.Equal(t, sourceLearn, ans.source)

	ans, err = initFromArgs([]string{"--repo", "--persona", "startup"})
	require.NoError(t, err)
	require.Equal(t, sourceStarter, ans.source)
	require.Equal(t, "startup", ans.starter)

	_, err = initFromArgs([]string{"--repo", "../other"})
	require.ErrorIs(t, err, errInitUsage)
}

func TestPersonaListAndForget(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Chdir(root)
	resolved, err := repoRoot(t.Context())
	require.NoError(t, err)
	var out strings.Builder
	require.NoError(t, runPersona(t.Context(), home, []string{"list"}, &out))
	require.Contains(t, out.String(), "no private repository personas")
	require.ErrorIs(t, runPersona(t.Context(), home, []string{"forget"}, &out), errNoRepoPersona)

	p, err := saveRepoPersona(t.Context(), home, resolved, []byte("# Mine\n"))
	require.NoError(t, err)
	name := filepath.Base(filepath.Dir(p))
	out.Reset()
	require.NoError(t, runPersona(t.Context(), home, []string{"list"}, &out))
	require.Contains(t, out.String(), name+"  "+resolved, "each is listed with the repository it is for")

	require.ErrorIs(t, runPersona(t.Context(), home, []string{"forget", "../" + name}, &out), errPersonaUsage, "only a name from the list is taken")
	require.NoError(t, runPersona(t.Context(), home, []string{"forget", name}, &out))
	_, err = os.Stat(filepath.Dir(p))
	require.ErrorIs(t, err, os.ErrNotExist)

	_, err = saveRepoPersona(t.Context(), home, resolved, []byte("# Mine\n"))
	require.NoError(t, err)
	require.NoError(t, runPersona(t.Context(), home, []string{"forget"}, &out), "with no name it is this repository's")
	got, err := loadPrivateRepoPersona(t.Context(), home, resolved)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestWithoutCredentials(t *testing.T) {
	tests := []struct{ origin, want string }{
		{"https://user:s3cret@example.com/team/repo.git", "https://example.com/team/repo.git"},
		{"https://token@example.com/team/repo.git", "https://example.com/team/repo.git"},
		{"ssh://git@example.com:22/team/repo", "ssh://example.com:22/team/repo"},
		{"https://example.com/a@b/repo", "https://example.com/a@b/repo"},
		{"https://tok@example.com", "https://example.com"},
		{"git@github.com:acme/api.git", "git@github.com:acme/api.git"},
		{"/srv/git/api.git", "/srv/git/api.git"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, withoutCredentials(tt.origin), tt.origin)
	}
}

func TestPersonaForget_RefusesALinkedEntry(t *testing.T) {
	home, elsewhere := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(elsewhere, "persona.md"), []byte("x"), privateFile))
	repos := filepath.Join(config.Dir(home), privateReposDir)
	require.NoError(t, os.MkdirAll(repos, privateDir))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(repos, "linked")))
	var out strings.Builder
	require.ErrorIs(t, runPersonaForget(t.Context(), home, []string{"linked"}, &out), errNoRepoPersona)
	_, err := os.Stat(filepath.Join(elsewhere, "persona.md"))
	require.NoError(t, err, "nothing outside ~/.codeforge is removed")
	require.NoError(t, runPersonaList(home, &out))
	require.NotContains(t, out.String(), "linked")
}

func TestPersonaList_NeverShowsCredentialsFromTheOrigin(t *testing.T) {
	home, root := t.TempDir(), filepath.Join(t.TempDir(), "work")
	for _, args := range [][]string{{"init", "-q", root}, {"-C", root, "remote", "add", "origin", "https://user:s3cret@example.com/team/repo.git"}} {
		out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	p, err := saveRepoPersona(t.Context(), home, root, []byte("# Mine\n"))
	require.NoError(t, err)
	label, err := os.ReadFile(filepath.Join(filepath.Dir(p), repoKeyFile))
	require.NoError(t, err)
	require.NotContains(t, string(label), "s3cret", "nor are they stored")
	require.NotContains(t, p, "s3cret")
	require.NotContains(t, p, "user", "the path is a hash, so nothing of the origin shows in it")
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(p), repoKeyFile), []byte("https://user:s3cret@example.com/team/repo.git\n"), privateFile))
	var out strings.Builder
	require.NoError(t, runPersonaList(home, &out))
	require.Contains(t, out.String(), "https://example.com/team/repo.git")
	require.NotContains(t, out.String(), "s3cret", "a label an older build stored is redacted when shown")
}
