package src

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

const personaEngine = `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = --max-steps ]; then
    shift
    if [ "$1" -lt 2 ]; then
      printf '%s\n' '{"output":"","error":"Max steps reached (1)"}'
      exit 1
    fi
  fi
  shift
done
printf '%s\n' '{"output":"# Persona\nUse evidence from the current repository.\n"}'
`

func TestInitUsesLocalPersonaInCurrentRepository(t *testing.T) {
	home, repo, bins := t.TempDir(), t.TempDir(), t.TempDir()
	t.Chdir(repo)
	t.Setenv("PATH", bins+string(os.PathListSeparator)+os.Getenv("PATH"))
	git := `#!/bin/sh
if [ "$1" = rev-parse ] || [ "$3" = rev-parse ]; then
  printf '%s\n' "$PWD"
  exit 0
fi
if [ "$1" != -C ] || [ "$2" != . ]; then exit 91; fi
case "$3" in
  config) printf 'author@example.test\n' ;;
  rev-parse) printf '%s\n' "$PWD" ;;
  log) printf 'fix: preserve user data\n' ;;
  remote) exit 0 ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(bins, "git"), []byte(git), execPerm))
	engine := filepath.Join(bins, "empryo")
	require.NoError(t, os.WriteFile(engine, []byte(personaEngine), execPerm))
	p, err := savePersona(home, []byte("# Existing starter\nGeneric conventions.\n"))
	require.NoError(t, err)
	require.NoError(t, runInit(t.Context(), home, engine, config.Config{}, nil))
	local := privateRepoPersonaPath(t.Context(), home, repo)
	data, err := os.ReadFile(local)
	require.NoError(t, err)
	require.Contains(t, string(data), "Use evidence from the current repository.")
	global, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "# Existing starter\nGeneric conventions.\n", string(global))

	require.NoError(t, runInit(t.Context(), home, engine, config.Config{}, []string{"--global"}))
	global, err = os.ReadFile(p)
	require.NoError(t, err)
	require.Contains(t, string(global), "Use evidence from the current repository.")
	require.NoError(t, os.WriteFile(local, []byte("# Team conventions\nKeep our changes.\n"), privateFile))
	require.NoError(t, os.Remove(engine))
	require.NoError(t, runInit(t.Context(), home, engine, config.Config{}, nil))
	data, err = os.ReadFile(local)
	require.NoError(t, err)
	require.Contains(t, string(data), "Keep our changes.")
}

func TestInitFindsRepositoryRootAndKeepsGlobalPersona(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	out, err := exec.CommandContext(t.Context(), "git", "init", "-q", root).CombinedOutput()
	require.NoError(t, err, string(out))
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(nested, privateDir))
	t.Chdir(nested)
	global, err := savePersona(home, []byte("# Global\nKeep this.\n"))
	require.NoError(t, err)
	require.NoError(t, runInit(t.Context(), home, "/missing-model-binary", config.Config{}, []string{"--persona", "oss"}))
	resolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	local, err := os.ReadFile(privateRepoPersonaPath(t.Context(), home, resolved))
	require.NoError(t, err)
	require.Contains(t, string(local), "Open-source maintainer")
	_, err = os.Stat(filepath.Join(root, repoPersonaPath))
	require.True(t, os.IsNotExist(err), "nothing is added to the repository")
	data, err := os.ReadFile(global)
	require.NoError(t, err)
	require.Equal(t, "# Global\nKeep this.\n", string(data))
	_, err = os.Stat(filepath.Join(nested, repoPersonaPath))
	require.True(t, os.IsNotExist(err))
}

func TestInitCanFillUntouchedRepositoryTemplates(t *testing.T) {
	for _, body := range []string{"", " \n", "template"} {
		t.Run(body, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			t.Chdir(root)
			if body == "template" {
				seed, err := repoPersonaSeed("")
				require.NoError(t, err)
				body = string(seed)
			}
			p := writeRepoPersona(t, root, body)
			require.NoError(t, runInit(t.Context(), home, "/missing-model-binary", config.Config{}, []string{"--repo", "--persona", "oss"}))
			data, err := os.ReadFile(p)
			require.NoError(t, err)
			require.Contains(t, string(data), "Open-source maintainer")
		})
	}
}

func TestInitExplicitGlobalScope(t *testing.T) {
	ans, err := initFromArgs([]string{"--global"})
	require.NoError(t, err)
	require.Equal(t, scopeGlobal, ans.scope)
	_, err = initFromArgs([]string{"--global", "--repo"})
	require.ErrorIs(t, err, errInitUsage)
}

func TestDistilAllowsTheFinalResponseToFinish(t *testing.T) {
	engine := filepath.Join(t.TempDir(), "empryo")
	require.NoError(t, os.WriteFile(engine, []byte(personaEngine), execPerm))
	data, err := distil(t.Context(), engine, "fixture/model", "instructions", "commit history")
	require.NoError(t, err)
	require.Contains(t, string(data), "# Persona")
}

func TestSavePersonaRejectsEmptyWithoutReplacingExisting(t *testing.T) {
	home := t.TempDir()
	p, err := savePersona(home, []byte("# Existing persona\nKeep this.\n"))
	require.NoError(t, err)
	_, err = savePersona(home, []byte(" \n\t"))
	require.ErrorIs(t, err, errEmptyPersona)
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Contains(t, string(data), "Keep this.")
}
