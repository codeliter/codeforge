package src

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
)

func TestSectionNamed(t *testing.T) {
	title, err := sectionNamed(" code STYLE ")
	require.NoError(t, err)
	require.Equal(t, "Code style", title)
	_, err = sectionNamed("Do and don't")
	require.ErrorIs(t, err, errUnknownSection, "the rules are drawn from the sections, not learned alone")
	require.ErrorContains(t, err, "Replies to reviewers")
}

func TestRevertPersona_SwapsWithTheEarlierOne(t *testing.T) {
	home := t.TempDir()
	var out strings.Builder
	require.ErrorIs(t, revertPersona(t.Context(), home, &out), errNoPrevious)

	p, err := savePersona(home, []byte("# First\n"))
	require.NoError(t, err)
	_, err = savePersona(home, []byte("# Second\n"))
	require.NoError(t, err)
	require.NoError(t, runPersona(t.Context(), home, []string{"revert"}, &out))
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "# First\n", string(data))
	require.NoError(t, revertPersona(t.Context(), home, &out), "a second revert undoes the first")
	data, err = os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "# Second\n", string(data))
	require.ErrorIs(t, runPersona(t.Context(), home, []string{"revert", "x"}, &out), errPersonaUsage)
}

func TestRevertPersona_PutsBackWhereThePersonaCameFromToo(t *testing.T) {
	home := t.TempDir()
	oss, err := starterPersona("oss")
	require.NoError(t, err)
	_, err = savePersona(home, oss)
	require.NoError(t, err)
	require.NoError(t, savePersonaSource(home, personaSource{Kind: sourceKindStarter, Starter: "oss"}))
	_, err = savePersona(home, []byte("# Persona\n\nLearned.\n"))
	require.NoError(t, err)
	require.NoError(t, savePersonaSource(home, personaSource{Kind: sourceKindLearned, Repos: []string{"/work/api"}}))

	var out strings.Builder
	require.NoError(t, revertPersona(t.Context(), home, &out))
	src, known := globalPersonaSource(home, nil)
	require.True(t, known)
	require.Equal(t, sourceKindStarter, src.Kind, "the starter is back, and so is its record")
	require.NoError(t, revertPersona(t.Context(), home, &out))
	src, known = globalPersonaSource(home, nil)
	require.True(t, known)
	require.Equal(t, sourceKindLearned, src.Kind)
}

func TestRecordLearned_KeepsEarlierRepositoriesAndTheOldDateUnlessAllOfItIsNew(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(config.Dir(home), privateDir))
	then := time.Now().Add(-90 * 24 * time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, savePersonaSource(home, personaSource{Kind: sourceKindLearned, Repos: []string{"/work/a"}, At: then}))

	require.NoError(t, recordLearned(home, []string{"/work/b"}, "m", false))
	src, _ := globalPersonaSource(home, nil)
	require.Equal(t, []string{"/work/b", "/work/a"}, src.Repos, "a repository used for one section is revisited by the next update")
	require.True(t, src.At.Equal(then), "sections that were not replaced are as old as they were")

	require.NoError(t, recordLearned(home, []string{"/work/a", "/work/b"}, "m", true))
	src, _ = globalPersonaSource(home, nil)
	require.WithinDuration(t, time.Now(), src.At, time.Minute, "all of it was just learned")

	require.NoError(t, savePersonaSource(home, personaSource{Kind: sourceKindStarter, Starter: "oss"}))
	require.NoError(t, recordLearned(home, []string{"/work/b"}, "m", false))
	src, _ = globalPersonaSource(home, nil)
	require.Equal(t, []string{"/work/b"}, src.Repos)
	require.WithinDuration(t, time.Now(), src.At, time.Minute, "a starter has no learning date to keep")
}

func TestMergePersona_ANewSectionThatWasNotTickedIsNotInstalled(t *testing.T) {
	old := "# Persona\n\n## Voice\nPlain.\n"
	merged := mergePersona(old, "# Persona\n\n## Voice\nBlunt.\n\n## Replies to reviewers\nOne line.\n", func(title string) bool { return title == "Voice" })
	require.Contains(t, merged, "Blunt.")
	require.NotContains(t, merged, "Replies to reviewers")
}

func TestRunPersonaUpdate_NeedsAPersonaAndKnownArguments(t *testing.T) {
	home := t.TempDir()
	require.ErrorIs(t, runPersonaUpdate(t.Context(), home, "/missing", config.Config{}, nil), errNoGlobalPersona)
	require.ErrorIs(t, runPersonaUpdate(t.Context(), home, "/missing", config.Config{}, []string{"--bogus"}), errPersonaUsage)
	require.ErrorIs(t, runPersonaUpdate(t.Context(), home, "/missing", config.Config{}, []string{"--section", "nope"}), errUnknownSection)
}

func TestMergePersona_OneRelearnedSectionKeepsItsPlace(t *testing.T) {
	old := "# Persona\n\nCareful.\n\n## Voice\nPlain.\n\n## Code style\nEarly returns.\n\n## Do and don't\n- Do test.\n\n## Personal notes\nMine.\n"
	merged := mergePersona(old, "## Code style\nSmall functions.\n\n## Replies to reviewers\nOne line.\n", func(string) bool { return true })
	var titles []string
	for _, s := range splitPersona(merged) {
		titles = append(titles, s.title)
	}
	require.Equal(t, []string{"", "Voice", "Code style", "Replies to reviewers", "Do and don't", "Personal notes"}, titles,
		"the saved order stands and a new section follows the one it comes after")
	require.Contains(t, merged, "Careful.")
	require.Contains(t, merged, "Small functions.")
	require.NotContains(t, merged, "Early returns.")
}

func TestPersonaStale(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(config.Dir(home), privateDir))
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q")
	git("config", "user.email", "me@example.test")
	git("config", "user.name", "Me")
	git("commit", "-q", "--allow-empty", "-m", "One")

	write := func(s personaSource, at time.Time) {
		t.Helper()
		require.NoError(t, savePersonaSource(home, s))
		// savePersonaSource stamps now; the test needs an older record.
		data, err := os.ReadFile(filepath.Join(config.Dir(home), personaSourceFile))
		require.NoError(t, err)
		stamped := strings.Replace(string(data), "\"at\": \"", "\"at\": \""+at.UTC().Format(time.RFC3339)+"\", \"was\": \"", 1)
		require.NoError(t, os.WriteFile(filepath.Join(config.Dir(home), personaSourceFile), []byte(stamped), privateFile))
	}
	now := time.Now()
	require.Empty(t, personaStale(t.Context(), home, now), "nothing recorded")

	write(personaSource{Kind: sourceKindLearned, Repos: []string{repo}}, now.Add(-90*24*time.Hour))
	require.Equal(t, "learned 90 days and 1 commit of yours ago", personaStale(t.Context(), home, now))

	write(personaSource{Kind: sourceKindLearned, Repos: []string{repo}}, now.Add(-10*24*time.Hour))
	require.Empty(t, personaStale(t.Context(), home, now), "recent, and far from the commit count")

	write(personaSource{Kind: sourceKindLearned, Repos: []string{repo}}, now.Add(time.Hour))
	require.Empty(t, personaStale(t.Context(), home, now.Add(100*24*time.Hour)), "old, but nothing done since")

	write(personaSource{Kind: sourceKindStarter, Starter: "oss"}, now.Add(-90*24*time.Hour))
	require.Empty(t, personaStale(t.Context(), home, now), "a starter was never learned from history")
}

func TestSplitPersona_AHeadingInsideAFenceIsNotASection(t *testing.T) {
	doc := "# Persona\n\n## Voice\nPlain.\n\n## Personal notes\nAn example:\n\n```markdown\n## Voice\nNot a section.\n```\n\nAfter it.\n"
	sections := splitPersona(doc)
	require.Len(t, sections, 3)
	require.Equal(t, "Personal notes", sections[2].title)
	require.Contains(t, sections[2].body, "Not a section.")
	require.Contains(t, sections[2].body, "After it.")
	merged := mergePersona(doc, "## Voice\nBlunt.\n", func(string) bool { return true })
	require.Contains(t, merged, "Blunt.")
	require.Contains(t, merged, "```markdown\n## Voice\nNot a section.\n```", "the user's example is left whole")
	require.Contains(t, reviewPersona(doc), "Not a section.")
}

func TestSplitPersona_FencesCloseOnlyOnAFenceAsLongAndOfTheSameKind(t *testing.T) {
	for _, fence := range []string{"````", "~~~~"} {
		inner := fence[:3]
		doc := "## Personal notes\n" + fence + "\n" + inner + "\n## Voice\nNot a section.\n" + inner + "\n" + fence + "\n\n## Voice\nReal.\n"
		sections := splitPersona(doc)
		require.Len(t, sections, 2, fence)
		require.Contains(t, sections[0].body, "Not a section.")
		require.Equal(t, "Real.", sections[1].body)
	}
	indented := splitPersona("## Notes\n    ```\n\n## Voice\nReal.\n")
	require.Len(t, indented, 2, "four spaces make a literal line, not a fence")
	other := splitPersona("## Notes\n```\n~~~\n## Voice\nInside.\n```\n\n## Voice\nReal.\n")
	require.Len(t, other, 2, "a fence of the other kind does not close one")
	require.Equal(t, "Real.", other[1].body)
	info := splitPersona("## Notes\n```go\n``` not a close\n## Voice\nInside.\n```\n")
	require.Len(t, info, 1, "a fence with text after it does not close one")
}

func TestCommitPersona_AFailedRecordPutsEverythingBack(t *testing.T) {
	home := t.TempDir()
	p, err := savePersona(home, []byte("# First\n"))
	require.NoError(t, err)
	require.NoError(t, savePersonaSource(home, personaSource{Kind: sourceKindStarter, Starter: "oss"}))
	base, err := os.ReadFile(p)
	require.NoError(t, err)

	_, err = commitPersona(t.Context(), home, reconciled{persona: []byte("# Second\n"), base: base}, func() error { return os.ErrPermission })
	require.ErrorIs(t, err, os.ErrPermission)
	now, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "# First\n", string(now), "the persona is not left beside another's source")
	src, known := globalPersonaSource(home, nil)
	require.True(t, known)
	require.Equal(t, sourceKindStarter, src.Kind)
	_, err = os.Stat(filepath.Join(config.Dir(home), previousPersonaFile))
	require.ErrorIs(t, err, os.ErrNotExist, "nor is a copy for revert left behind")
	skill, err := os.ReadFile(filepath.Join(home, ".empryo", "skills", "codeforge-persona", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(skill), "# First")
}
