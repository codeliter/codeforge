//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	require.Equal(t, privateRepoPersonaDir(t.Context(), home, root), p, "a new persona is kept outside the repository, one file a section")
	about, err := os.ReadFile(filepath.Join(p, openingFile))
	require.NoError(t, err)
	require.Equal(t, "# Repo\nRules.\n", string(about))
	require.True(t, strings.HasPrefix(p, config.Dir(home)))
	_, err = os.Stat(filepath.Join(root, ".codeforge"))
	require.ErrorIs(t, err, os.ErrNotExist, "nothing is added to the repository")

	_, err = saveRepoPersona(t.Context(), home, root, []byte("other"))
	require.ErrorIs(t, err, errRepoPersonaExists)

	scaffold := t.TempDir()
	shared := writeRepoPersona(t, scaffold, "")
	_, err = saveRepoPersona(t.Context(), home, scaffold, []byte("# Repo\nRules.\n"))
	require.ErrorIs(t, err, errRepoPersonaExists, "a scaffold in the repository is the team's to fill in: nothing there is replaced")
	data, err := os.ReadFile(shared)
	require.NoError(t, err)
	require.Empty(t, data)
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
	require.Contains(t, got[1], "## Voice\nPlain.", "what applies to everything is in the instructions")
	require.NotContains(t, got[1], "Tabs.", "the rest is loaded for the work it covers")
	skills := repoSkillDirs(t, home)
	require.Len(t, skills, 1, "a part the persona lacks is no skill")
	require.Contains(t, got[1], "- `"+filepath.Base(skills[0])+"`: before writing, changing or reviewing code.")
	skill := filepath.Join(skills[0], "SKILL.md")
	code, err := os.ReadFile(skill)
	require.NoError(t, err)
	require.Contains(t, string(code), "## Code style (the user's own for this repository)\nTabs.")
	require.Contains(t, string(code), "Load only when your instructions name this skill.")
	review, err := withRepoPersonaFor(t.Context(), home, modeReview, []string{"task"})
	require.NoError(t, err)
	require.Contains(t, review[1], "## Code style\nTabs.", "review mode has the code part in its instructions")
	require.NotContains(t, review[1], repoSkillPrefix)
	_, err = os.Stat(skill)
	require.NoError(t, err, "and a session already running here keeps the skill it was told of")
	_, err = os.Stat(filepath.Join(resolved, ".empryo"))
	require.ErrorIs(t, err, os.ErrNotExist, "nothing is written into the checkout, so nothing private can be staged from it")

	require.NoError(t, runPersona(t.Context(), home, []string{"share"}, &out))
	shared, err := loadRepoPersona(resolved)
	require.NoError(t, err)
	require.Equal(t, "# Mine\n\n## Voice\nPlain.\n\n## Code style\nTabs.", shared, "every section is shared")
	voice, err := os.ReadFile(filepath.Join(resolved, repoPersonaDirPath, "voice.md"))
	require.NoError(t, err)
	require.Equal(t, "## Voice\nPlain.\n", string(voice), "one file a section")
	require.Equal(t, filepath.Join(resolved, repoPersonaDirPath), sharedPersonaPath(resolved))
	_, err = os.Stat(private)
	require.NoError(t, err, "sharing never removes what you wrote")
	got, err = withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.Contains(t, got[1], "<repo_persona>\n# Mine", "and it is trusted, since its author shared it")
	require.NoError(t, runPersona(t.Context(), home, []string{"forget"}, &out))
	got, err = withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.NotContains(t, got[1], "<my_repo_persona>")
	skills = repoSkillDirs(t, home)
	require.Len(t, skills, 3, "each launch names the skill that holds its own conventions; earlier ones stay for sessions still running")
	require.Contains(t, got[1], "codeforge-repo-persona-code-")
	code, err = os.ReadFile(skill)
	require.NoError(t, err)
	require.Contains(t, string(code), "the user's own", "a skill is never changed once written")
	old := time.Now().Add(-2 * repoSkillAge)
	require.NoError(t, os.Chtimes(skill, old, old))
	require.NoError(t, os.RemoveAll(filepath.Join(resolved, ".codeforge")))
	got, err = withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.Equal(t, []string{"task"}, got)
	_, err = os.Stat(skill)
	require.ErrorIs(t, err, os.ErrNotExist, "a skill no launch has used for a long time is removed")
	edited := filepath.Join(repoSkillDirs(t, home)[0], "SKILL.md")
	text, err := os.ReadFile(edited)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(edited, []byte(strings.Replace(string(text), "description: ", "description: Mine. ", 1)), privateFile))
	require.NoError(t, os.Chtimes(edited, old, old))
	sweepRepoSkills(filepath.Dir(filepath.Dir(edited)), nil)
	_, err = os.Stat(edited)
	require.NoError(t, err, "one edited since it was written is kept")
	require.Len(t, repoSkillDirs(t, home), 2)
	require.ErrorIs(t, runPersona(t.Context(), home, []string{"share", "x"}, &out), errPersonaUsage)
}

func repoSkillDirs(t *testing.T, root string) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(root, ".empryo", "skills", repoSkillPrefix+"*"))
	require.NoError(t, err)
	return dirs
}

func TestRepoSkills_AreNeverRewrittenOrWrittenThroughALink(t *testing.T) {
	home, root, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	t.Chdir(root)
	resolved, err := repoRoot(t.Context())
	require.NoError(t, err)
	_, err = saveRepoPersona(t.Context(), home, resolved, []byte("# Mine\n\n## Code style\nTabs.\n"))
	require.NoError(t, err)
	_, err = withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	skills := repoSkillDirs(t, home)
	require.Len(t, skills, 1)

	got, err := withRepoPersona(t.Context(), home, []string{"--system", "mine", "task"})
	require.NoError(t, err)
	require.Equal(t, []string{"--system", "mine", "task"}, got, "the user's own instructions name no repository skill")
	_, err = os.Stat(skills[0])
	require.NoError(t, err, "and a session already running here keeps the one it was told of")

	require.NoError(t, os.WriteFile(filepath.Join(skills[0], "SKILL.md"), []byte("tracked by the repository"), privateFile))
	got, err = withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.Contains(t, got[1], "## Code style\nTabs.", "a skill that could not be installed is given in the instructions instead")
	require.NotContains(t, got[1], repoSkillPrefix)
	kept, err := os.ReadFile(filepath.Join(skills[0], "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, "tracked by the repository", string(kept), "what is there under that name is never rewritten")

	require.NoError(t, os.RemoveAll(filepath.Join(home, ".empryo")))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".empryo"), privateDir))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(home, ".empryo", "skills")))
	got, err = withRepoPersona(t.Context(), home, []string{"task"})
	require.NoError(t, err)
	require.Contains(t, got[1], "## Code style\nTabs.")
	entries, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing is written through a link")

	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.MkdirAll(target, privateDir))
	require.NoError(t, os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("text"), privateFile))
	stamp := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(target, "SKILL.md"), stamp, stamp))
	skillsDir := t.TempDir()
	require.NoError(t, os.Symlink(target, filepath.Join(skillsDir, "linked-skill")))
	require.ErrorIs(t, installRepoSkill(skillsDir, "linked-skill", "text"), errRepoSkill, "a skill directory that is a link is not taken up")
	info, err := os.Stat(filepath.Join(target, "SKILL.md"))
	require.NoError(t, err)
	require.True(t, info.ModTime().Before(time.Now().Add(-time.Minute)), "nor touched through it")

	inside := filepath.Join(resolved, "home")
	require.NoError(t, os.MkdirAll(inside, privateDir))
	_, err = installRepoSkills(t.Context(), inside, resolved, nil, map[string]string{"Code style": "Tabs."})
	require.ErrorIs(t, err, errRepoSkill, "when the home directory is in the checkout, skills would be too")
	dotted := filepath.Join(resolved, "..home")
	require.NoError(t, os.MkdirAll(dotted, privateDir))
	_, err = installRepoSkills(t.Context(), dotted, resolved, nil, map[string]string{"Code style": "Tabs."})
	require.ErrorIs(t, err, errRepoSkill, "a name that only starts like the way out is still inside")
	linked := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.Symlink(inside, linked))
	_, err = installRepoSkills(t.Context(), linked, resolved, nil, map[string]string{"Code style": "Tabs."})
	require.ErrorIs(t, err, errRepoSkill, "as is a home that is a link into the checkout")
}

func TestSharedPersona_KeepsItsOrderAndNothingInTheRepositoryIsReplaced(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	doc := "# Team\n\n## Zebra\nZ.\n\n## Alpha\nA.\n"
	require.NoError(t, createShared(home, root, []byte(doc)))
	got, err := loadRepoPersona(root)
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(doc), got, "sections come back in the order they were written, not by file name")

	scaffold := t.TempDir()
	seed, err := repoPersonaSeed("")
	require.NoError(t, err)
	_, err = writeSectionDir(filepath.Join(scaffold, repoPersonaDirPath), seed, dirPerm, publicFile)
	require.NoError(t, err)
	_, err = saveRepoPersona(t.Context(), home, scaffold, []byte(doc))
	require.ErrorIs(t, err, errRepoPersonaExists, "a scaffold kept as a directory is the team's to fill in")
	require.ErrorContains(t, err, "unfilled template")
	got, err = loadRepoPersona(scaffold)
	require.NoError(t, err)
	require.True(t, unfinishedRepoPersona([]byte(got)), "and it is left as it was")

	empty := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(empty, repoPersonaDirPath), dirPerm))
	require.ErrorIs(t, createShared(home, empty, []byte(doc)), errRepoPersonaExists, "even an empty directory already there is not taken over")
	long := t.TempDir()
	_, err = writeSectionDir(filepath.Join(long, "persona"), []byte("# P\n\n## "+strings.Repeat("a", 256)+"\nx\n"), dirPerm, publicFile)
	require.ErrorIs(t, err, errRepoPersonaSize)
	left, err := os.ReadDir(long)
	require.NoError(t, err)
	require.Empty(t, left, "a persona that cannot be written whole leaves nothing behind")
	atLimit := t.TempDir()
	writeRepoPersona(t, atLimit, strings.Repeat("x", maxRepoPersonaBytes))
	_, err = writeSectionDir(filepath.Join(atLimit, "ok"), []byte(strings.Repeat("x", maxRepoPersonaBytes)), dirPerm, publicFile)
	require.NoError(t, err, "a single file at the limit can be shared as it is")
	_, err = writeSectionDir(filepath.Join(atLimit, "out"), []byte(strings.Repeat("x", maxRepoPersonaBytes+1)), dirPerm, publicFile)
	require.ErrorIs(t, err, errRepoPersonaSize, "one too large as section files is refused before anything is written")
	_, err = os.Stat(filepath.Join(atLimit, "out"))
	require.ErrorIs(t, err, os.ErrNotExist)
	legacy := t.TempDir()
	writeRepoPersona(t, legacy, strings.Repeat("## A\n", 10)+strings.Repeat("x", maxRepoPersonaBytes-50))
	_, err = loadRepoPersona(legacy)
	require.NoError(t, err, "a single file is held to its own size, as before")

	require.NoError(t, os.WriteFile(filepath.Join(root, repoPersonaDirPath, orderFile), []byte(orderHeader+"zebra.md\nzebra.md\nzebra.md\nalpha.md\n"), publicFile))
	require.NoError(t, os.WriteFile(filepath.Join(root, repoPersonaDirPath, "zz.md"), []byte("## Zz\nLast.\n"), publicFile))
	got, err = loadRepoPersona(root)
	require.NoError(t, err)
	require.Less(t, strings.Index(got, "## Alpha"), strings.Index(got, "## Zz"), "a file the order does not list comes after all it does")
	require.NoError(t, os.Remove(filepath.Join(root, repoPersonaDirPath, "zz.md")))
	require.NoError(t, os.Remove(filepath.Join(root, repoPersonaDirPath, orderFile)))
	require.NoError(t, os.Symlink("/dev/null", filepath.Join(root, repoPersonaDirPath, orderFile)))
	got, err = loadRepoPersona(root)
	require.NoError(t, err)
	require.Contains(t, got, "## Alpha\nA.\n\n## Zebra", "an order that is a link is not opened")
	require.NoError(t, os.Remove(filepath.Join(root, repoPersonaDirPath, orderFile)))
	require.NoError(t, os.WriteFile(filepath.Join(root, repoPersonaDirPath, orderFile), []byte("zebra.md\nalpha.md\n"), publicFile))
	got, err = loadRepoPersona(root)
	require.NoError(t, err)
	require.Contains(t, got, "## Alpha\nA.\n\n## Zebra", "a file of that name that codeforge did not write is not taken for an order")
	require.NoError(t, os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, repoPersonaPath)))
	got, err = loadRepoPersona(root)
	require.NoError(t, err)
	require.Contains(t, got, "## Zebra", "a link to nothing is no single file, so the directory is read")
	require.NoError(t, os.Remove(filepath.Join(root, repoPersonaPath)))
	hollow := t.TempDir()
	writeRepoPersona(t, hollow, "Rules in the single file.")
	require.NoError(t, os.Rename(filepath.Join(hollow, repoPersonaPath), filepath.Join(hollow, ".codeforge", "kept.md")))
	require.NoError(t, os.MkdirAll(filepath.Join(hollow, repoPersonaDirPath), dirPerm))
	require.NoError(t, os.WriteFile(filepath.Join(hollow, repoPersonaDirPath, openingFile), nil, publicFile))
	require.NoError(t, os.WriteFile(filepath.Join(hollow, repoPersonaDirPath, "code-style.md"), nil, publicFile))
	got, err = readSectionDoc(filepath.Join(hollow, repoPersonaDirPath), filepath.Join(hollow, ".codeforge", "kept.md"), "x")
	require.NoError(t, err)
	require.Equal(t, "Rules in the single file.", got, "a directory that says nothing does not hide a single file that does")
	writeRepoPersona(t, root, "The team's single file.")
	got, err = loadRepoPersona(root)
	require.NoError(t, err)
	require.Equal(t, "The team's single file.", got, "a directory never hides a single file beside it")
	require.Equal(t, filepath.Join(root, repoPersonaPath), sharedPersonaPath(root))

	_, err = writeSectionDir(filepath.Join(t.TempDir(), "persona"), []byte("## Code style\n"), dirPerm, publicFile)
	require.ErrorIs(t, err, errEmptyPersona, "headings with nothing under them are not a persona")
	apart := "## A\nfirst\n\n## B\nmiddle\n\n## A\nlast\n"
	spread := filepath.Join(t.TempDir(), "persona")
	back, err := writeSectionDir(spread, []byte(apart), dirPerm, publicFile)
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(apart), back, "a title used twice keeps both places")
	reread, err := readSectionDoc(spread, "", "x")
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(apart), reread)
	var many strings.Builder
	many.WriteString("# P\n")
	for i := range maxRepoSections + 1 {
		fmt.Fprintf(&many, "\n## S%d\nx\n", i)
	}
	_, err = writeSectionDir(filepath.Join(t.TempDir(), "persona"), []byte(many.String()), dirPerm, publicFile)
	require.ErrorIs(t, err, errRepoPersonaSize, "more sections than can be loaded back are not saved")

	foreign := filepath.Join(home, ".empryo", "skills", repoSkillPrefix+"mine")
	require.NoError(t, os.MkdirAll(foreign, privateDir))
	require.NoError(t, os.WriteFile(filepath.Join(foreign, "SKILL.md"), []byte("x"), privateFile))
	old := time.Now().Add(-2 * repoSkillAge)
	require.NoError(t, os.Chtimes(filepath.Join(foreign, "SKILL.md"), old, old))
	sweepRepoSkills(filepath.Dir(foreign), nil)
	_, err = os.Stat(filepath.Join(foreign, "SKILL.md"))
	require.NoError(t, err, "only skills codeforge made are ever swept")
}

func TestPersonaSize_IsTheSameHoweverThePersonaIsKept(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	doc := "# P\n## A\n" + strings.Repeat("x", maxRepoPersonaBytes-14) + "\n## B\ny\n"
	require.Equal(t, maxRepoPersonaBytes, personaSize(doc))
	dir, err := saveRepoPersona(t.Context(), home, root, []byte(doc))
	require.NoError(t, err)
	got, err := loadPrivateRepoPersona(t.Context(), home, root)
	require.NoError(t, err, "what was saved at the limit loads")
	require.Equal(t, maxRepoPersonaBytes, personaSize(got))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("## B\nyy\n"), privateFile))
	_, err = loadPrivateRepoPersona(t.Context(), home, root)
	require.ErrorIs(t, err, errRepoPersonaSize)
	_, err = saveRepoPersona(t.Context(), home, t.TempDir(), []byte(doc+"z"))
	require.ErrorIs(t, err, errRepoPersonaSize)

	twice := "## A\n" + strings.Repeat("x", 100) + "\n\n## A\ny\n"
	_, texts := sectionFiles([]byte(twice))
	require.Equal(t, len(texts["a.md"])-1, personaSize(twice), "sections that share a title measure as the one file they are saved as")
	_, err = saveRepoPersona(t.Context(), home, t.TempDir(), []byte("## A\nx\n"+strings.Repeat("\n", maxRepoPersonaRaw)))
	require.ErrorIs(t, err, errRepoPersonaSize, "blank lines do not get a persona past the limit on what is read")
	big := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(big, ".codeforge"), dirPerm))
	require.NoError(t, os.WriteFile(filepath.Join(big, repoPersonaPath), []byte("x"+strings.Repeat("\n", maxRepoPersonaRaw)), publicFile))
	_, err = loadRepoPersona(big)
	require.ErrorIs(t, err, errRepoPersonaSize)
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
