//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"os"
	"path/filepath"
	"testing"

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
	require.Equal(t, initAnswers{scope: scopeGlobal, source: sourceLearn, repos: "."}, ans)

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
