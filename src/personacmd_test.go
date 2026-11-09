package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
)

func TestRunPersona_ShowsEachPersonaAndWhereItCameFrom(t *testing.T) {
	home := t.TempDir()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(repo)
	var out strings.Builder
	require.NoError(t, runPersona(t.Context(), home, nil, &out))
	require.Contains(t, out.String(), "Global persona: none")
	require.Contains(t, out.String(), "Repository persona: none")

	oss, err := starterPersona("oss")
	require.NoError(t, err)
	_, err = savePersona(home, oss)
	require.NoError(t, err)
	out.Reset()
	require.NoError(t, runPersona(t.Context(), home, []string{"describe"}, &out))
	require.Contains(t, out.String(), `Source: the built-in "oss" starter`, "a persona saved before sources were recorded is known by its text")

	require.NoError(t, savePersonaSource(home, personaSource{Kind: sourceKindLearned, Repos: []string{"~/work/api"}}))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".codeforge"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, repoPersonaPath), []byte("# Team\nUse tabs.\n"), 0o600))
	out.Reset()
	require.NoError(t, runPersona(t.Context(), home, nil, &out))
	require.Contains(t, out.String(), "Source: learned from your history in ~/work/api")
	require.Contains(t, out.String(), "Repository persona: "+filepath.Join(repo, repoPersonaPath))
	require.Contains(t, out.String(), "not in use: new or changed since you approved it")
	require.Contains(t, out.String(), "Use tabs.", "a persona with no sections is summarised by its opening")
	require.NotContains(t, out.String(), "## Code style", "the summary names the sections, it does not print them")
	require.Contains(t, out.String(), "Covers: ")
	out.Reset()
	require.NoError(t, runPersona(t.Context(), home, []string{"describe", "--full"}, &out))
	require.Contains(t, out.String(), "## Code style", "--full prints the whole persona")

	require.NoError(t, config.Trust(home, filepath.Join(repo, repoPersonaPath), "# Team\nUse tabs."))
	out.Reset()
	require.NoError(t, runPersona(t.Context(), home, nil, &out))
	require.Contains(t, out.String(), "in use: it overrides the global persona where they differ")

	require.ErrorIs(t, runPersona(t.Context(), home, []string{"show"}, &out), errPersonaUsage)
}

func TestPersonaSummary(t *testing.T) {
	text := "# Persona\n\nA homelab engineer who ships small changes.\n\n## Voice\n- Plain.\n\n## Commits\n- Imperative."
	require.Equal(t, "A homelab engineer who ships small changes.\nCovers: Voice, Commits.", personaSummary(text))
}
