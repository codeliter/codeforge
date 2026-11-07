//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// themeProject is a home with the Steel themes installed and a project directory.
type themeProject struct {
	home, project, config, ledger string
}

func newThemeProject(t *testing.T) themeProject {
	t.Helper()
	home := t.TempDir()
	project, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, syncAssets(filepath.Join(home, ".empryo"), Config{}))
	t.Setenv(themeEnv, "")
	old := appearanceProbe
	appearanceProbe = func(context.Context) string { return "dark" }
	t.Cleanup(func() { appearanceProbe = old })
	return themeProject{
		home: home, project: project,
		config: filepath.Join(project, ".empryo", "config.json"),
		ledger: themeLedgerPath(home, project),
	}
}

func (p themeProject) write(t *testing.T, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p.config), 0o750))
	require.NoError(t, os.WriteFile(p.config, []byte(body), 0o600))
}

func (p themeProject) read(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(p.config)
	require.NoError(t, err)
	return string(raw)
}

func TestScopeTheme_IsOnWhileTheTUIIsOpenAndGoneAfter(t *testing.T) {
	p := newThemeProject(t)
	undo := scopeTheme(t.Context(), p.home, p.project)
	require.JSONEq(t, `{"theme":{"name":"codeforge-steel"}}`, p.read(t))
	require.FileExists(t, p.ledger)
	require.NoFileExists(t, filepath.Join(p.project, ".empryo", ".codeforge-theme.json"), "nothing but the config goes into the project")

	undo()
	require.NoDirExists(t, filepath.Join(p.project, ".empryo"), "the folder codeforge made is gone with the file")
	require.NoFileExists(t, p.ledger)
}

func TestScopeTheme_PicksTheLightThemeForALightTerminal(t *testing.T) {
	p := newThemeProject(t)
	appearanceProbe = func(context.Context) string { return "light" }
	undo := scopeTheme(t.Context(), p.home, p.project)
	defer undo()
	require.JSONEq(t, `{"theme":{"name":"codeforge-steel-light"}}`, p.read(t))
}

func TestScopeTheme_KeepsOtherSettingsAndRestoresTheFileExactly(t *testing.T) {
	p := newThemeProject(t)
	original := "{\n  \"effort\": \"low\",\n    \"big\": 9007199254740993,\n    \"theme\": {\"transparent\": true}\n}\n"
	p.write(t, original)
	undo := scopeTheme(t.Context(), p.home, p.project)
	applied := p.read(t)
	require.JSONEq(t, `{"effort":"low","big":9007199254740993,"theme":{"transparent":true,"name":"codeforge-steel"}}`, applied)
	require.Contains(t, applied, "9007199254740993", "a number too large for a float keeps its digits")

	undo()
	require.Equal(t, original, p.read(t), "the project's own file comes back byte for byte")
	require.NoFileExists(t, p.ledger)
}

func TestScopeTheme_NeverReplacesAThemeTheUserChose(t *testing.T) {
	p := newThemeProject(t)
	p.write(t, `{"theme":{"name":"dracula"}}`)
	undo := scopeTheme(t.Context(), p.home, p.project)
	defer undo()
	require.JSONEq(t, `{"theme":{"name":"dracula"}}`, p.read(t))
	require.NoFileExists(t, p.ledger)
}

func TestScopeTheme_DoesNothingWhenThereIsNothingToDo(t *testing.T) {
	t.Run("the user already uses a Steel theme everywhere, either one", func(t *testing.T) {
		for _, global := range []string{"codeforge-steel", "codeforge-steel-light"} {
			p := newThemeProject(t)
			require.NoError(t, os.WriteFile(filepath.Join(p.home, ".empryo", "config.json"), []byte(`{"theme":{"name":"`+global+`"}}`), 0o600))
			scopeTheme(t.Context(), p.home, p.project)()
			require.NoDirExists(t, filepath.Dir(p.config), global)
		}
	})
	t.Run("the theme file is not installed", func(t *testing.T) {
		p := newThemeProject(t)
		require.NoError(t, os.Remove(filepath.Join(p.home, ".empryo", "themes", "codeforge-steel.json")))
		scopeTheme(t.Context(), p.home, p.project)()
		require.NoDirExists(t, filepath.Dir(p.config))
	})
	t.Run("switched off", func(t *testing.T) {
		for _, off := range []string{"off", "OFF", "0", "none", "system"} {
			p := newThemeProject(t)
			t.Setenv(themeEnv, off)
			scopeTheme(t.Context(), p.home, p.project)()
			require.NoDirExists(t, filepath.Dir(p.config), off)
		}
	})
	t.Run("a config with comments is left alone", func(t *testing.T) {
		p := newThemeProject(t)
		body := "// mine\n{\"effort\":\"low\"}\n"
		p.write(t, body)
		scopeTheme(t.Context(), p.home, p.project)()
		require.Equal(t, body, p.read(t))
		require.NoFileExists(t, p.ledger)
	})
	t.Run("no project", func(t *testing.T) {
		p := newThemeProject(t)
		scopeTheme(t.Context(), p.home, "")()
		require.NoDirExists(t, filepath.Dir(p.config))
	})
	t.Run("a file git tracks", func(t *testing.T) {
		p := newThemeProject(t)
		p.write(t, `{"effort":"low"}`)
		for _, args := range [][]string{{"init", "-q"}, {"add", "-f", ".empryo/config.json"}} {
			out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", p.project}, args...)...).CombinedOutput()
			require.NoError(t, err, string(out))
		}
		scopeTheme(t.Context(), p.home, p.project)()
		require.JSONEq(t, `{"effort":"low"}`, p.read(t), "a committed setting is never changed under the user's feet")
		require.NoFileExists(t, p.ledger)
	})
}

func TestScopeTheme_NeverWritesTheGlobalConfig(t *testing.T) {
	p := newThemeProject(t)
	home, err := filepath.EvalSymlinks(p.home)
	require.NoError(t, err)
	globalConfig := filepath.Join(home, ".empryo", "config.json")
	require.NoError(t, os.WriteFile(globalConfig, []byte(`{"theme":{"name":"nord"}}`), 0o600))
	scopeTheme(t.Context(), p.home, home)()
	raw, err := os.ReadFile(globalConfig)
	require.NoError(t, err)
	require.JSONEq(t, `{"theme":{"name":"nord"}}`, string(raw), "in the home directory the project's .empryo is the global one")
}

func TestScopeTheme_WillNotWriteThroughALink(t *testing.T) {
	t.Run(".empryo is a link to someone else's folder", func(t *testing.T) {
		p := newThemeProject(t)
		other := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(other, "config.json"), []byte(`{"effort":"low"}`), 0o600))
		require.NoError(t, os.Symlink(other, filepath.Join(p.project, ".empryo")))
		scopeTheme(t.Context(), p.home, p.project)()
		raw, err := os.ReadFile(filepath.Join(other, "config.json"))
		require.NoError(t, err)
		require.JSONEq(t, `{"effort":"low"}`, string(raw))
	})
	t.Run("config.json is a link", func(t *testing.T) {
		p := newThemeProject(t)
		target := filepath.Join(t.TempDir(), "shared.json")
		require.NoError(t, os.WriteFile(target, []byte(`{"effort":"low"}`), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Dir(p.config), 0o750))
		require.NoError(t, os.Symlink(target, p.config))
		scopeTheme(t.Context(), p.home, p.project)()
		info, err := os.Lstat(p.config)
		require.NoError(t, err)
		require.NotZero(t, info.Mode()&os.ModeSymlink, "the link is still a link")
		raw, err := os.ReadFile(target)
		require.NoError(t, err)
		require.JSONEq(t, `{"effort":"low"}`, string(raw))
	})
}

func TestScopeTheme_ALedgerInTheCheckoutIsNotTrusted(t *testing.T) {
	p := newThemeProject(t)
	body := `{"effort":"low"}`
	p.write(t, body)
	hostile := `{"workspace":"` + p.project + `","holders":[],"existed":false,"written":"` + hashOf([]byte(body)) + `","theme":"anything"}`
	require.NoError(t, os.WriteFile(filepath.Join(p.project, ".empryo", ".codeforge-theme.json"), []byte(hostile), 0o600))
	scopeTheme(t.Context(), p.home, p.project)()
	require.JSONEq(t, body, p.read(t), "a repository cannot make codeforge delete or replace its files")
}

func TestScopeTheme_TheLastTUIToCloseTakesItOut(t *testing.T) {
	p := newThemeProject(t)
	first := scopeTheme(t.Context(), p.home, p.project)
	second := scopeTheme(t.Context(), p.home, p.project)
	first()
	require.FileExists(t, p.config, "another codeforge TUI is still open on this project")
	second()
	require.NoFileExists(t, p.config)
	require.NoFileExists(t, p.ledger)
}

// killed leaves the theme applied and the ledger naming a codeforge that no longer exists.
func killed(t *testing.T, p themeProject) {
	t.Helper()
	scopeTheme(t.Context(), p.home, p.project) // never undone: the process was killed
	ledger, ok := loadThemeLedger(p.ledger, p.project)
	require.True(t, ok)
	ledger.Holders = []themeHolder{{Pid: 2_147_483_000, ID: "dead"}} // a process that no longer exists
	require.NoError(t, saveThemeLedger(p.ledger, ledger))
}

func TestScopeTheme_PutsBackWhatAKilledCodeforgeLeft(t *testing.T) {
	p := newThemeProject(t)
	original := `{"effort":"low"}`
	p.write(t, original)
	killed(t, p)
	scopeTheme(t.Context(), p.home, p.project)()
	require.Equal(t, original, p.read(t), "the stale theme was reverted first, then applied and taken out again")
	require.NoFileExists(t, p.ledger)
}

func TestScopeTheme_CleansUpEvenWhenTheThemeIsSwitchedOff(t *testing.T) {
	p := newThemeProject(t)
	original := `{"effort":"low"}`
	p.write(t, original)
	killed(t, p)
	t.Setenv(themeEnv, "off")
	scopeTheme(t.Context(), p.home, p.project)()
	require.Equal(t, original, p.read(t), "opting out also removes what an earlier launch left")
	require.NoFileExists(t, p.ledger)
}

func TestScopeTheme_KeepsTheLedgerWhenTheFileCannotBePutBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only folder does not stop root")
	}
	p := newThemeProject(t)
	p.write(t, `{"effort":"low"}`)
	undo := scopeTheme(t.Context(), p.home, p.project)
	dir := filepath.Dir(p.config)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { bestEffort(os.Chmod(dir, 0o750)) })
	undo()
	require.FileExists(t, p.ledger, "the original bytes are still known, so a later launch can restore them")
	require.Contains(t, p.read(t), "codeforge-steel")

	require.NoError(t, os.Chmod(dir, 0o750))
	scopeTheme(t.Context(), p.home, p.project)()
	require.JSONEq(t, `{"effort":"low"}`, p.read(t))
	require.NoFileExists(t, p.ledger)
}

func TestScopeTheme_OnlyTakesOutItsOwnKeyWhenTheFileChangedMeanwhile(t *testing.T) {
	p := newThemeProject(t)
	undo := scopeTheme(t.Context(), p.home, p.project)
	p.write(t, `{"effort":"high","theme":{"name":"codeforge-steel","transparent":true}}`) // the user changed a setting in the TUI
	undo()
	require.JSONEq(t, `{"effort":"high","theme":{"transparent":true}}`, p.read(t))

	// And a theme the user picked inside the TUI is never touched.
	p2 := newThemeProject(t)
	undo = scopeTheme(t.Context(), p2.home, p2.project)
	p2.write(t, `{"theme":{"name":"nord"}}`)
	undo()
	require.JSONEq(t, `{"theme":{"name":"nord"}}`, p2.read(t))
}

func TestScopeTheme_OneProjectHasOneIdentityHoweverItIsSpelled(t *testing.T) {
	p := newThemeProject(t)
	t.Chdir(p.project)
	first := scopeTheme(t.Context(), p.home, ".")
	second := scopeTheme(t.Context(), p.home, p.project)
	first()
	require.FileExists(t, p.config, "the second TUI named the same project by its full path")
	second()
	require.NoFileExists(t, p.config)

	// The home directory is the home directory, whatever it is called.
	home, err := filepath.EvalSymlinks(p.home)
	require.NoError(t, err)
	t.Chdir(home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".empryo"), 0o750))
	scopeTheme(t.Context(), p.home, ".")()
	require.NoFileExists(t, filepath.Join(home, ".empryo", "config.json"), "never the global config, even as '.'")
}

// gitIn runs git in dir and fails the test if it does.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

func TestScopeTheme_InARepositoryOnlyWhereGitAlreadyIgnoresTheConfig(t *testing.T) {
	t.Run("ignored: applied, never staged, and gone after", func(t *testing.T) {
		p := newThemeProject(t)
		gitIn(t, p.project, "init", "-q")
		require.NoError(t, os.WriteFile(filepath.Join(p.project, ".gitignore"), []byte(".empryo/\n"), 0o600))
		undo := scopeTheme(t.Context(), p.home, p.project)
		require.FileExists(t, p.config)
		gitIn(t, p.project, "add", "-A")
		require.NotContains(t, gitIn(t, p.project, "status", "--porcelain"), "config.json", "git add -A leaves the temporary config alone")
		undo()
		require.NoFileExists(t, p.config)
	})
	t.Run("not ignored: nothing is written and no ignore rule is edited", func(t *testing.T) {
		p := newThemeProject(t)
		gitIn(t, p.project, "init", "-q")
		exclude := filepath.Join(p.project, ".git", "info", "exclude")
		before, err := os.ReadFile(exclude)
		require.NoError(t, err)
		scopeTheme(t.Context(), p.home, p.project)()
		require.NoDirExists(t, filepath.Dir(p.config))
		after, err := os.ReadFile(exclude)
		require.NoError(t, err)
		require.Equal(t, string(before), string(after))
	})
	t.Run("a later rule that un-ignores the file counts as not ignored", func(t *testing.T) {
		p := newThemeProject(t)
		gitIn(t, p.project, "init", "-q")
		require.NoError(t, os.WriteFile(filepath.Join(p.project, ".gitignore"), []byte(".empryo/*\n!.empryo/config.json\n"), 0o600))
		scopeTheme(t.Context(), p.home, p.project)()
		require.NoFileExists(t, p.config)
	})
	t.Run("a project in a subfolder of a repository whose rule only covers the root", func(t *testing.T) {
		p := newThemeProject(t)
		gitIn(t, p.project, "init", "-q")
		require.NoError(t, os.WriteFile(filepath.Join(p.project, ".gitignore"), []byte("/.empryo/\n"), 0o600))
		sub := filepath.Join(p.project, "svc")
		require.NoError(t, os.MkdirAll(sub, 0o750))
		scopeTheme(t.Context(), p.home, sub)()
		require.NoDirExists(t, filepath.Join(sub, ".empryo"), "svc/.empryo is not covered by /.empryo/")

		require.NoError(t, os.WriteFile(filepath.Join(p.project, ".gitignore"), []byte(".empryo/\n"), 0o600))
		undo := scopeTheme(t.Context(), p.home, sub)
		require.FileExists(t, filepath.Join(sub, ".empryo", "config.json"))
		undo()
	})
	t.Run("not a repository: applied", func(t *testing.T) {
		p := newThemeProject(t)
		undo := scopeTheme(t.Context(), p.home, p.project)
		require.FileExists(t, p.config)
		undo()
	})
}

func TestScopeTheme_OnlyAnIgnoreRuleThatCoversTheTemporaryFilesToo(t *testing.T) {
	// atomicWriteFile writes a full copy of the config to .codeforge-* beside it before the rename.
	for _, name := range themeProbeNames() {
		require.Regexp(t, "^"+regexp.QuoteMeta(tempPrefix)+"[0-9]+$", name, "the probe is shaped like a real temporary file name")
	}
	for _, rules := range []string{
		".empryo/config.json\n",
		".empryo/config.json\n.empryo/.config.json.*\n",
		".empryo/config.json\n.empryo/.codeforge-theme-probe\n",
		".empryo/.codeforge-*\n!.empryo/.codeforge-[0-9]*\n.empryo/config.json\n",
	} {
		p := newThemeProject(t)
		gitIn(t, p.project, "init", "-q")
		require.NoError(t, os.WriteFile(filepath.Join(p.project, ".gitignore"), []byte(rules), 0o600))
		scopeTheme(t.Context(), p.home, p.project)()
		require.NoDirExists(t, filepath.Dir(p.config), "the file is ignored but the temporary copy written beside it is not: %q", rules)
	}

	p := newThemeProject(t)
	gitIn(t, p.project, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(p.project, ".gitignore"), []byte(".empryo/config.json\n.codeforge-*\n.empryo/.codeforge-*\n"), 0o600))
	undo := scopeTheme(t.Context(), p.home, p.project)
	require.FileExists(t, p.config, "both the file and its temporary copies are ignored")
	undo()
}

// fakeGit puts a git on the path that prints stderr and exits 128, like git does for a refusal.
func fakeGit(t *testing.T, stderr string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '" + stderr + "' >&2\nexit 128\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), execPerm))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestScopeTheme_OnlyGitsOwnNotARepositoryAnswerMeansThereIsNothingToStage(t *testing.T) {
	t.Run("an ownership refusal is not 'no repository'", func(t *testing.T) {
		p := newThemeProject(t)
		fakeGit(t, "fatal: detected dubious ownership in repository")
		scopeTheme(t.Context(), p.home, p.project)()
		require.NoDirExists(t, filepath.Dir(p.config))
	})
	t.Run("a broken repository is not 'no repository'", func(t *testing.T) {
		p := newThemeProject(t)
		fakeGit(t, "fatal: bad config line 1 in file .git/config")
		scopeTheme(t.Context(), p.home, p.project)()
		require.NoDirExists(t, filepath.Dir(p.config))
	})
	t.Run("not a git repository is", func(t *testing.T) {
		p := newThemeProject(t)
		fakeGit(t, "fatal: not a git repository (or any of the parent directories): .git")
		undo := scopeTheme(t.Context(), p.home, p.project)
		require.FileExists(t, p.config)
		undo()
	})
}

func TestScopeTheme_AReusedPidIsNotTheCodeforgeThatLeftTheTheme(t *testing.T) {
	old := processStart
	processStart = func(int) string { return "Fri Nov  7 10:00:00 2026" } // what ps says about every process here
	t.Cleanup(func() { processStart = old })

	require.True(t, holderAlive(themeHolder{Pid: os.Getpid(), Start: "Fri Nov  7 10:00:00 2026"}), "the same process")
	require.False(t, holderAlive(themeHolder{Pid: os.Getpid(), Start: "Thu Jan  1 00:00:00 1970"}), "a different process now has that pid")
	require.True(t, holderAlive(themeHolder{Pid: os.Getpid()}), "no start time was recorded: the pid decides")
	require.False(t, holderAlive(themeHolder{Pid: 2_147_483_000}))
	processStart = func(int) string { return "" } // ps is missing or too slow
	require.True(t, holderAlive(themeHolder{Pid: os.Getpid(), Start: "Fri Nov  7 10:00:00 2026"}), "an unreadable start time is not evidence of another process")
	processStart = func(int) string { return "Fri Nov  7 10:00:00 2026" }

	p := newThemeProject(t)
	original := `{"effort":"low"}`
	p.write(t, original)
	scopeTheme(t.Context(), p.home, p.project) // left applied
	ledger, ok := loadThemeLedger(p.ledger, p.project)
	require.True(t, ok)
	ledger.Holders = []themeHolder{{Pid: os.Getpid(), ID: "old", Start: "Thu Jan  1 00:00:00 1970"}}
	require.NoError(t, saveThemeLedger(p.ledger, ledger))
	scopeTheme(t.Context(), p.home, p.project)()
	require.Equal(t, original, p.read(t), "the stale holder with a recycled pid no longer keeps the theme in place")
}

func TestScopeTheme_KeepsTheFilesPermissionsAndDropsAnEmptiedFile(t *testing.T) {
	p := newThemeProject(t)
	p.write(t, `{"effort":"low"}`)
	require.NoError(t, os.Chmod(p.config, 0o644))
	scopeTheme(t.Context(), p.home, p.project)()
	info, err := os.Stat(p.config)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "a world-readable config does not come back private")

	// A file that did not exist, changed to hold only our key, is not left behind as {}.
	p2 := newThemeProject(t)
	undo := scopeTheme(t.Context(), p2.home, p2.project)
	p2.write(t, `{ "theme": { "name": "codeforge-steel" } }`)
	undo()
	require.NoFileExists(t, p2.config)
	require.NoDirExists(t, filepath.Dir(p2.config))
}

func TestWorkspaceOf(t *testing.T) {
	require.Equal(t, "/x/y", workspaceOf([]string{"empryo", "--cwd", "/x/y", "prompt"}))
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, wd, workspaceOf([]string{"empryo", "prompt"}))
}

func TestSystemAppearance(t *testing.T) {
	t.Setenv("EMPRYO_APPEARANCE", "light")
	require.Equal(t, "light", systemAppearance(t.Context()))
	t.Setenv("EMPRYO_APPEARANCE", "")
	t.Setenv("COLORFGBG", "0;15")
	require.Equal(t, "light", systemAppearance(t.Context()))
	t.Setenv("COLORFGBG", "15;0")
	require.Equal(t, "dark", systemAppearance(t.Context()))
	require.Equal(t, steelDark, steelFor("dark"))
	require.Equal(t, steelLight, steelFor("light"))
}
