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
	require.Contains(t, out.String(), "Use tabs.")

	require.NoError(t, config.Trust(home, filepath.Join(repo, repoPersonaPath), "# Team\nUse tabs."))
	out.Reset()
	require.NoError(t, runPersona(t.Context(), home, nil, &out))
	require.Contains(t, out.String(), "in use: it overrides the global persona where they differ")

	require.ErrorIs(t, runPersona(t.Context(), home, []string{"show"}, &out), errPersonaUsage)
}
