//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

func TestStarters(t *testing.T) {
	got := starters()
	require.Len(t, got, 4)
	for _, s := range got {
		require.NotEmpty(t, s.Summary, s.Name)
		data, err := starterPersona(s.Name)
		require.NoError(t, err)
		require.Contains(t, string(data), "## Code style")
	}
}

func TestStarterPersona_Unknown(t *testing.T) {
	_, err := starterPersona("../../etc/passwd")
	require.ErrorIs(t, err, errUnknownPersona)
}

func TestInstallPersonaSkill(t *testing.T) {
	home := t.TempDir()
	skill := filepath.Join(home, ".empryo", "skills", "codeforge-persona", "SKILL.md")

	require.NoError(t, installPersonaSkill(home))
	got, err := os.ReadFile(skill)
	require.NoError(t, err)
	require.Contains(t, string(got), "codeforge init")

	_, err = savePersona(home, []byte("# Persona\nShort sentences.\n"))
	require.NoError(t, err)
	got, err = os.ReadFile(skill)
	require.NoError(t, err)
	require.Contains(t, string(got), "name: codeforge-persona")
	require.Contains(t, string(got), "Short sentences.")
	require.Less(t, strings.Index(string(got), clarityBaseline), strings.Index(string(got), "Short sentences."),
		"the clarity baseline comes before the persona, which builds on it")
}

func TestInstallPersonaSkill_EachPartIsASkillLoadedForItsWork(t *testing.T) {
	home := t.TempDir()
	skill := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(home, ".empryo", "skills", name, "SKILL.md"))
		require.NoError(t, err)
		return string(data)
	}
	require.NoError(t, installPersonaSkill(home))
	for _, k := range onDemandSkills {
		require.Contains(t, skill(k.name), "Follow `"+k.otherwise+"`", "every skill exists before there is a persona, so the profiles resolve")
	}
	_, err := savePersona(home, []byte("# Persona\n\nCareful.\n\n## Voice\nPlain.\n\n## Commit messages\nImperative.\n\n## Pull requests\nShort.\n\n## Review comments\nOne finding each.\n\n## Replies to reviewers\n"+noEvidence+"\n\n## Code style\nEarly returns.\n\n## Do and don't\n- Do test.\n\n## Personal notes\nTickets look like ABC-1.\n"))
	require.NoError(t, err)
	base := skill("codeforge-persona")
	for _, kept := range []string{clarityBaseline, "Careful.", "## Voice\nPlain.", "## Do and don't", "## Personal notes", "- `codeforge-persona-commits`: before writing a commit message."} {
		require.Contains(t, base, kept)
	}
	for _, elsewhere := range []string{"Imperative.", "Short.", "One finding each.", "Early returns."} {
		require.NotContains(t, base, elsewhere, "what is loaded always holds only what applies to everything")
	}
	old := skill(reviewAlias)
	for _, kept := range []string{"Careful.", "## Voice", "One finding each.", "Early returns."} {
		require.Contains(t, old, kept, "a session started before the upgrade still finds its reviewing skill")
	}
	require.NotContains(t, old, "Imperative.")

	tests := []struct{ name, want string }{
		{"codeforge-persona-commits", "## Commit messages\nImperative."},
		{"codeforge-persona-prs", "## Pull requests\nShort."},
		{"codeforge-persona-reviewing", "## Review comments\nOne finding each."},
		{"codeforge-persona-code", "## Code style\nEarly returns."},
		{"codeforge-persona-replies", "Follow `codeforge-respond`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Contains(t, skill(tt.name), "name: "+tt.name+"\n")
			require.Contains(t, skill(tt.name), tt.want)
		})
	}
}

func TestPersonaStore_OneFileASectionAndTheOldSingleFileStillReads(t *testing.T) {
	home := t.TempDir()
	saved, err := readPersona(home)
	require.NoError(t, err)
	require.Nil(t, saved)

	legacy := "# Persona\n\nCareful.\n\n## Voice\nPlain.\n"
	require.NoError(t, os.MkdirAll(config.Dir(home), privateDir))
	require.NoError(t, os.WriteFile(filepath.Join(config.Dir(home), legacyPersonaFile), []byte(legacy), privateFile))
	saved, err = readPersona(home)
	require.NoError(t, err)
	require.Equal(t, legacy, string(saved), "a persona from an older version is read as it is")
	require.Equal(t, filepath.Join(config.Dir(home), legacyPersonaFile), personaPath(home))

	doc := "# Persona\n\nCareful.\n\n## Voice\nPlain.\n\n## Code style\nEarly returns.\n\n## Do and don't\n- Do test.\n\n## Personal notes\nMine.\n"
	dir, err := savePersona(home, []byte(doc))
	require.NoError(t, err)
	require.Equal(t, personaDir(home), dir)
	_, err = os.Stat(filepath.Join(config.Dir(home), legacyPersonaFile))
	require.ErrorIs(t, err, os.ErrNotExist, "the single file is replaced by the directory")
	tests := []struct{ file, want string }{
		{"about.md", "# Persona\n\nCareful.\n"},
		{"voice.md", "## Voice\nPlain.\n"},
		{"code-style.md", "## Code style\nEarly returns.\n"},
		{"do-and-don-t.md", "## Do and don't\n- Do test.\n"},
		{"personal-notes.md", "## Personal notes\nMine.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, tt.file))
			require.NoError(t, err)
			require.Equal(t, tt.want, string(data))
		})
	}
	saved, err = readPersona(home)
	require.NoError(t, err)
	require.Equal(t, doc, string(saved), "the files read back as the document that was saved")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "team-rules.md"), []byte("Always squash.\n"), privateFile))
	require.NoError(t, os.Remove(filepath.Join(dir, "voice.md")))
	saved, err = readPersona(home)
	require.NoError(t, err)
	require.Contains(t, string(saved), "## team-rules\nAlways squash.", "a file added by hand is a section named after it")
	require.NotContains(t, string(saved), "## Voice", "and a file removed by hand is a section gone")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("mine"), privateFile))
	_, err = savePersona(home, []byte("# Persona\n\n## Voice\nBlunt.\n"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "code-style.md"))
	require.ErrorIs(t, err, os.ErrNotExist, "a section the saved document lacks leaves no file behind")
	_, err = os.Stat(filepath.Join(dir, "scratch.txt"))
	require.NoError(t, err, "what is not a section file stays in the directory")
}

func TestPersonaStore_SavingNeverTouchesWhatIsNotASectionFile(t *testing.T) {
	home := t.TempDir()
	first := "# Persona\n\n## Code style\nEarly returns.\n\n## Notes\nOne.\n\n## Code style\nNo globals.\n\n## VOICE\nLoud.\n\n## Voice\nPlain.\n"
	dir, err := savePersona(home, []byte(first))
	require.NoError(t, err)
	files := map[string]string{
		"code-style.md": "## Code style\nEarly returns.\n\nNo globals.\n",
		"voice.md":      "## VOICE\nLoud.\n",
		"voice~2.md":    "## Voice\nPlain.\n",
	}
	for name, want := range files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, name)
		require.Equal(t, want, string(data), "one title is one file; two titles are two, whatever their file names")
	}

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pull-requests.md", "keep"), privateDir))
	require.NoError(t, os.Symlink(filepath.Join(dir, "notes.md"), filepath.Join(dir, "linked.md")))
	before, err := readPersona(home)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(before), "One."), "a link is not a section")
	_, err = savePersona(home, []byte("# Changed\n\n## Pull requests\nShort.\n"))
	require.ErrorIs(t, err, errPersonaStore, "a directory where a section file would go is the user's, so the save is refused")
	after, err := readPersona(home)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "and refused whole: nothing was written")

	require.NoError(t, removePersona(home))
	_, err = os.Stat(filepath.Join(dir, "pull-requests.md", "keep"))
	require.NoError(t, err, "removing the persona leaves what is not a section file")
	_, err = os.Lstat(filepath.Join(dir, "linked.md"))
	require.NoError(t, err)
	gone, err := readPersona(home)
	require.NoError(t, err)
	require.Nil(t, gone)
}

func TestPersonaStore_ASaveCutShortIsReadWholeAndFinishedByTheNext(t *testing.T) {
	home := t.TempDir()
	_, err := savePersona(home, []byte("# Persona\n\n## Voice\nPlain.\n\n## Code style\nEarly returns.\n"))
	require.NoError(t, err)
	// A save that set the document down and wrote one file before it was killed.
	next := "# Persona\n\n## Voice\nBlunt.\n\n## Notes\nNew.\n"
	require.NoError(t, os.WriteFile(filepath.Join(config.Dir(home), savingFile), []byte(next), privateFile))
	require.NoError(t, os.WriteFile(filepath.Join(personaDir(home), "voice.md"), []byte("## Voice\nBlunt.\n"), privateFile))

	got, err := readPersona(home)
	require.NoError(t, err)
	require.Equal(t, next, string(got), "readers get the whole document that was being saved, not a mix")
	require.Equal(t, personaDir(home), personaPath(home))

	_, err = savePersona(home, []byte(next+"\n## Code style\nTabs.\n"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(config.Dir(home), savingFile))
	require.ErrorIs(t, err, os.ErrNotExist)
	previous, err := os.ReadFile(filepath.Join(config.Dir(home), previousPersonaFile))
	require.NoError(t, err)
	require.Equal(t, next, string(previous), "the cut-short save counts as the persona the next one replaced")
	got, err = readPersona(home)
	require.NoError(t, err)
	require.Contains(t, string(got), "Tabs.")
	require.NotContains(t, string(got), "Early returns.")
}

func TestPersonaPath_FollowsWhatTheReaderReads(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(config.Dir(home), legacyPersonaFile)
	require.NoError(t, os.MkdirAll(personaDir(home), privateDir))
	require.NoError(t, os.WriteFile(legacy, []byte("# Old\n"), privateFile))
	require.NoError(t, os.WriteFile(filepath.Join(personaDir(home), "scratch.txt"), []byte("x"), privateFile))
	require.Equal(t, legacy, personaPath(home), "a directory with no section file is not the persona")
	got, err := readPersona(home)
	require.NoError(t, err)
	require.Equal(t, "# Old\n", string(got))

	require.NoError(t, allOrNothing(home, func() error { return nil }))
	require.Error(t, allOrNothing(t.TempDir(), func() error { return os.ErrPermission }), "with no ~/.codeforge a failed change has nothing to put back")
	require.NoError(t, os.Remove(legacy))
	require.ErrorIs(t, allOrNothing(home, func() error {
		_, err := savePersona(home, []byte("# New\n"))
		return errors.Join(err, os.ErrPermission)
	}), os.ErrPermission)
	_, err = os.Stat(filepath.Join(personaDir(home), "scratch.txt"))
	require.NoError(t, err, "undoing a first save removes its section files, not what the user kept there")
	got, err = readPersona(home)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestInstallPersonaSkill_ASectionWrittenTwiceKeepsAllItsRules(t *testing.T) {
	home := t.TempDir()
	_, err := savePersona(home, []byte("# Persona\n\n## Code style\nEarly returns.\n"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(personaDir(home), "more.md"), []byte("## Code style\nNo globals.\n"), privateFile))
	require.NoError(t, installPersonaSkill(home))
	data, err := os.ReadFile(filepath.Join(home, ".empryo", "skills", "codeforge-persona-code", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "Early returns.")
	require.Contains(t, string(data), "No globals.")
}

func TestInstallPersonaSkillHandlesEmptyPersona(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(config.Dir(home), privateDir))
	require.NoError(t, os.WriteFile(filepath.Join(config.Dir(home), legacyPersonaFile), []byte(" \n\t"), privateFile))
	require.NoError(t, installPersonaSkill(home))
	data, err := os.ReadFile(filepath.Join(home, ".empryo", "skills", "codeforge-persona", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "No persona yet.")
}

func TestRepoSlug(t *testing.T) {
	tests := []struct{ remote, want string }{
		{"git@github.com:acme/api.git", "acme/api"},
		{"https://github.com/acme/api.git", "acme/api"},
		{"https://github.com/acme/api", "acme/api"},
		{"ssh://git@github.com/acme/api.git", "acme/api"},
		{"https://gitlab.com/acme/api.git", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			require.Equal(t, tt.want, repoSlug(tt.remote))
		})
	}
}

func TestInitFromArgs(t *testing.T) {
	ans, err := initFromArgs(nil)
	require.NoError(t, err)
	require.Equal(t, initAnswers{scope: scopeAuto, source: sourceLearn, repos: "."}, ans)

	ans, err = initFromArgs([]string{"../other", "../third"})
	require.NoError(t, err)
	require.Equal(t, "../other,../third", ans.repos)

	ans, err = initFromArgs([]string{"--persona", "oss"})
	require.NoError(t, err)
	require.Equal(t, sourceStarter, ans.source)
	require.Equal(t, "oss", ans.starter)

	_, err = initFromArgs([]string{"--bogus"})
	require.ErrorIs(t, err, errInitUsage)
}
