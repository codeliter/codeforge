//nolint:err113 // dynamic error messages are okay in tests
package src

import (
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

func TestInstallPersonaSkill_ReviewersGetOnlyWhatReviewingNeeds(t *testing.T) {
	home := t.TempDir()
	review := filepath.Join(home, ".empryo", "skills", reviewPersonaSkill, "SKILL.md")
	require.NoError(t, installPersonaSkill(home))
	got, err := os.ReadFile(review)
	require.NoError(t, err)
	require.Contains(t, string(got), "No persona yet.", "the skill exists before there is a persona, so the profiles resolve")

	_, err = savePersona(home, []byte("# Persona\n\nCareful.\n\n## Voice\nPlain.\n\n## Commit messages\nImperative.\n\n## Pull requests\nShort.\n\n## Review comments\nOne finding each.\n\n## Replies to reviewers\nOne line.\n\n## Code style\nEarly returns.\n\n## Do and don't\n- Do test.\n\n## Personal notes\nTickets look like ABC-1.\n"))
	require.NoError(t, err)
	got, err = os.ReadFile(review)
	require.NoError(t, err)
	text := string(got)
	require.Contains(t, text, "name: "+reviewPersonaSkill)
	require.Contains(t, text, clarityBaseline)
	for _, kept := range []string{"Careful.", "## Voice", "## Review comments", "## Code style", "## Do and don't", "## Personal notes"} {
		require.Contains(t, text, kept)
	}
	for _, dropped := range []string{"## Commit messages", "## Pull requests", "## Replies to reviewers", "Imperative."} {
		require.NotContains(t, text, dropped)
	}
	full, err := os.ReadFile(filepath.Join(home, ".empryo", "skills", "codeforge-persona", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(full), "## Commit messages", "the other modes still get all of it")
}

func TestInstallPersonaSkillHandlesEmptyPersona(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(config.Dir(home), privateDir))
	require.NoError(t, os.WriteFile(filepath.Join(config.Dir(home), "persona.md"), []byte(" \n\t"), privateFile))
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
