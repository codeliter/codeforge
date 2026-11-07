package src

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

func TestStarterPickerOptions(t *testing.T) {
	choices := starters()
	options := starterOptions(choices)
	require.Len(t, options, 4)
	for i, choice := range choices {
		require.NotEmpty(t, choice.Description)
		require.Equal(t, choice.Name, options[i].Value)
		require.Contains(t, options[i].Label, choice.Name)
		require.Contains(t, options[i].Label, choice.Description)
	}
}

func TestResolveStarter(t *testing.T) {
	calls := 0
	choose := func() (string, error) {
		calls++
		return "startup", nil
	}
	name, err := resolveStarter("oss", choose)
	require.NoError(t, err)
	require.Equal(t, "oss", name)
	require.Zero(t, calls, "explicit names must not open the selector")
	name, err = resolveStarter("", choose)
	require.NoError(t, err)
	require.Equal(t, "startup", name)
	require.Equal(t, 1, calls)
	_, err = resolveStarter("", func() (string, error) { return "", context.Canceled })
	require.ErrorIs(t, err, context.Canceled)
	_, err = resolveStarter("", func() (string, error) { return "unknown", nil })
	require.ErrorIs(t, err, errUnknownPersona)
}

func TestPersonaPickerWithoutTerminalListsChoicesAndWritesNothing(t *testing.T) {
	if interactive() {
		t.Skip("tests the non-interactive path")
	}
	home, repo := t.TempDir(), t.TempDir()
	t.Chdir(repo)
	err := runInit(t.Context(), home, "/missing-model-binary", config.Config{}, []string{"--persona"})
	require.ErrorIs(t, err, errInitUsage)
	for _, choice := range starters() {
		require.Contains(t, err.Error(), choice.Name+": "+choice.Description)
	}
	_, err = os.Stat(filepath.Join(home, ".codeforge", "persona.md"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(repo, repoPersonaPath))
	require.True(t, os.IsNotExist(err))
}

func TestInitPersonaNameIsOptional(t *testing.T) {
	for _, tc := range []struct {
		name, scope string
		args        []string
	}{
		{"choose locally", scopeAuto, []string{"--persona"}},
		{"global after persona", scopeGlobal, []string{"--persona", "--global"}},
		{"global before persona", scopeGlobal, []string{"--global", "--persona"}},
		{"repo after persona", scopeRepo, []string{"--persona", "--repo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ans, err := initFromArgs(tc.args)
			require.NoError(t, err)
			require.Equal(t, tc.scope, ans.scope)
			require.Equal(t, sourceStarter, ans.source)
			require.Empty(t, ans.starter, "an omitted name must open the selector, not consume the next flag")
			require.Equal(t, ".", ans.repos)
		})
	}
}
