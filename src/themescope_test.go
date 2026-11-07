//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"
)

// themeHome is a home with the Steel themes installed and a global Empryo config.
type themeHome struct {
	home, config, ledger string
}

const userConfig = "{\n  \"effort\": \"low\",\n  \"big\": 9007199254740993,\n  \"theme\": {\n    \"name\": \"catppuccin\",\n    \"transparent\": true\n  }\n}\n"

func newThemeHome(t *testing.T) themeHome {
	t.Helper()
	home := t.TempDir()
	require.NoError(t, syncAssets(filepath.Join(home, ".empryo"), config.Config{}))
	t.Setenv(themeEnv, "")
	t.Setenv("EMPRYO_HOME", "")
	old := appearanceProbe
	appearanceProbe = func(context.Context) string { return "dark" }
	t.Cleanup(func() { appearanceProbe = old })
	h := themeHome{home: home, config: filepath.Join(home, ".empryo", "config.json"), ledger: themeLedgerPath(home)}
	h.write(t, userConfig)
	return h
}

func (h themeHome) write(t *testing.T, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(h.config, []byte(body), 0o600))
}

func (h themeHome) read(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(h.config)
	require.NoError(t, err)
	return string(raw)
}

func TestScopeTheme_SteelIsOnWhileTheTUIIsOpenAndTheUsersThemeIsBackAfter(t *testing.T) {
	h := newThemeHome(t)
	undo := scopeTheme(t.Context(), h.home)
	applied := h.read(t)
	require.JSONEq(t, `{"effort":"low","big":9007199254740993,"theme":{"name":"codeforge-steel","transparent":true}}`, applied)
	require.Contains(t, applied, "9007199254740993", "a number too large for a float keeps its digits")
	require.FileExists(t, h.ledger)

	undo()
	require.Equal(t, userConfig, h.read(t), "the user's file comes back byte for byte")
	require.NoFileExists(t, h.ledger)
}

func TestScopeTheme_PicksTheLightThemeForALightTerminal(t *testing.T) {
	h := newThemeHome(t)
	appearanceProbe = func(context.Context) string { return "light" }
	undo := scopeTheme(t.Context(), h.home)
	defer undo()
	require.Contains(t, h.read(t), `"codeforge-steel-light"`)
}

func TestScopeTheme_WithNoThemeOfTheUsersOwnTheKeyIsRemovedAgain(t *testing.T) {
	h := newThemeHome(t)
	h.write(t, `{"effort":"low"}`)
	undo := scopeTheme(t.Context(), h.home)
	require.JSONEq(t, `{"effort":"low","theme":{"name":"codeforge-steel"}}`, h.read(t))
	undo()
	require.Equal(t, `{"effort":"low"}`, h.read(t))
}

func TestScopeTheme_SettingsEmpryoSavedMeanwhileSurviveTheRevert(t *testing.T) {
	h := newThemeHome(t)
	undo := scopeTheme(t.Context(), h.home)
	// Empryo rewrote the whole file because the user changed a setting; the Steel theme is still in it.
	h.write(t, `{"effort":"high","model":"x","theme":{"name":"codeforge-steel","transparent":true}}`)
	undo()
	require.JSONEq(t, `{"effort":"high","model":"x","theme":{"name":"catppuccin","transparent":true}}`, h.read(t),
		"only the theme goes back to what it was; the new settings stay")
	require.NoFileExists(t, h.ledger)
}

func TestScopeTheme_ATheoryChosenInTheTUIIsTheUsersAndStays(t *testing.T) {
	h := newThemeHome(t)
	undo := scopeTheme(t.Context(), h.home)
	h.write(t, `{"theme":{"name":"nord"}}`) // /theme in the TUI
	undo()
	require.JSONEq(t, `{"theme":{"name":"nord"}}`, h.read(t))
	require.NoFileExists(t, h.ledger)
}

func TestScopeTheme_DoesNothingWhenThereIsNothingToDo(t *testing.T) {
	t.Run("the user already uses a Steel theme", func(t *testing.T) {
		for _, name := range []string{"codeforge-steel", "codeforge-steel-light"} {
			h := newThemeHome(t)
			body := `{"theme":{"name":"` + name + `"}}`
			h.write(t, body)
			scopeTheme(t.Context(), h.home)()
			require.Equal(t, body, h.read(t))
			require.NoFileExists(t, h.ledger)
		}
	})
	t.Run("the theme file is not installed", func(t *testing.T) {
		h := newThemeHome(t)
		require.NoError(t, os.Remove(filepath.Join(h.home, ".empryo", "themes", "codeforge-steel.json")))
		scopeTheme(t.Context(), h.home)()
		require.Equal(t, userConfig, h.read(t))
	})
	t.Run("switched off", func(t *testing.T) {
		for _, off := range []string{"off", "OFF", "0", "none", "system"} {
			h := newThemeHome(t)
			t.Setenv(themeEnv, off)
			scopeTheme(t.Context(), h.home)()
			require.Equal(t, userConfig, h.read(t), off)
			require.NoFileExists(t, h.ledger, off)
		}
	})
	t.Run("a config with comments is left alone", func(t *testing.T) {
		h := newThemeHome(t)
		body := "// mine\n{\"effort\":\"low\"}\n"
		h.write(t, body)
		scopeTheme(t.Context(), h.home)()
		require.Equal(t, body, h.read(t))
		require.NoFileExists(t, h.ledger)
	})
	t.Run("no config yet", func(t *testing.T) {
		h := newThemeHome(t)
		require.NoError(t, os.Remove(h.config))
		scopeTheme(t.Context(), h.home)()
		require.NoFileExists(t, h.config, "Empryo makes its own config; codeforge does not")
	})
}

func TestScopeTheme_LeavesEverythingAloneWhenEmpryoReadsAnotherHome(t *testing.T) {
	h := newThemeHome(t)
	t.Setenv("EMPRYO_HOME", t.TempDir())
	scopeTheme(t.Context(), h.home)()
	require.Equal(t, userConfig, h.read(t), "the themes are installed in the home directory, not where EMPRYO_HOME points")
	require.NoFileExists(t, h.ledger)
}

func TestScopeTheme_ARestoreNeverLoosensPermissionsTheUserTightened(t *testing.T) {
	h := newThemeHome(t)
	require.NoError(t, os.Chmod(h.config, 0o644))
	undo := scopeTheme(t.Context(), h.home)
	require.NoError(t, os.Chmod(h.config, 0o600)) // the user made it private meanwhile
	undo()
	info, err := os.Stat(h.config)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.Equal(t, userConfig, h.read(t))
}

func TestSignalsToCatch_LeaveSIGHUPAloneWhenItWasIgnored(t *testing.T) {
	for _, hupIgnored := range []bool{false, true} {
		sigs := signalsToCatch(hupIgnored)
		require.Contains(t, sigs, os.Signal(syscall.SIGTERM))
		require.Contains(t, sigs, os.Signal(syscall.SIGQUIT))
		require.Contains(t, sigs, os.Interrupt)
	}
	require.Contains(t, signalsToCatch(false), os.Signal(syscall.SIGHUP))
	require.NotContains(t, signalsToCatch(true), os.Signal(syscall.SIGHUP), "a run started under nohup keeps ignoring a closed terminal")
}

func TestScopeTheme_AConfigThatIsALinkStaysALink(t *testing.T) {
	h := newThemeHome(t)
	target := filepath.Join(t.TempDir(), "dotfiles-config.json")
	require.NoError(t, os.WriteFile(target, []byte(userConfig), 0o600))
	require.NoError(t, os.Remove(h.config))
	require.NoError(t, os.Symlink(target, h.config))
	undo := scopeTheme(t.Context(), h.home)
	info, err := os.Lstat(h.config)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "the link is still a link while the theme is on")
	raw, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Contains(t, string(raw), "codeforge-steel")
	undo()
	back, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, userConfig, string(back))
	info, err = os.Lstat(h.config)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
}

func TestScopeTheme_ATUIWithTheThemeSwitchedOffStillHoldsAnAppliedTheme(t *testing.T) {
	h := newThemeHome(t)
	first := scopeTheme(t.Context(), h.home)
	t.Setenv(themeEnv, "off")
	second := scopeTheme(t.Context(), h.home) // its engine reads the Steel theme the first one applied
	first()
	require.Contains(t, h.read(t), "codeforge-steel", "the opted-out TUI's engine could still save the Steel theme back")
	second()
	require.Equal(t, userConfig, h.read(t))
	require.NoFileExists(t, h.ledger)

	// With nothing applied, switching it off applies nothing and records nothing.
	scopeTheme(t.Context(), h.home)()
	require.Equal(t, userConfig, h.read(t))
	require.NoFileExists(t, h.ledger)
}

func TestScopeTheme_TheLastTUIToCloseTakesItOut(t *testing.T) {
	h := newThemeHome(t)
	first := scopeTheme(t.Context(), h.home)
	second := scopeTheme(t.Context(), h.home)
	first()
	require.Contains(t, h.read(t), "codeforge-steel", "another codeforge TUI is still open")
	second()
	require.Equal(t, userConfig, h.read(t))
	require.NoFileExists(t, h.ledger)
}

func TestScopeTheme_TheUndoDoesNotNeedAContextThatIsStillAlive(t *testing.T) {
	h := newThemeHome(t)
	ctx, cancel := context.WithCancel(t.Context())
	undo := scopeTheme(ctx, h.home)
	cancel() // what main's signal context does on SIGTERM
	undo()
	require.Equal(t, userConfig, h.read(t))
}

// killed leaves the theme applied and the ledger naming a codeforge that no longer exists.
func killed(t *testing.T, h themeHome) {
	t.Helper()
	scopeTheme(t.Context(), h.home) // never undone: the process was killed
	ledger, ok := loadThemeLedger(h.ledger)
	require.True(t, ok)
	ledger.Holders = []themeHolder{{Pid: 2_147_483_000, ID: "dead"}}
	require.NoError(t, saveThemeLedger(h.ledger, ledger))
}

func TestScopeTheme_PutsBackWhatAKilledCodeforgeLeft(t *testing.T) {
	h := newThemeHome(t)
	killed(t, h)
	require.Contains(t, h.read(t), "codeforge-steel")
	scopeTheme(t.Context(), h.home)()
	require.Equal(t, userConfig, h.read(t), "the stale theme was reverted first, then applied and taken out again")
	require.NoFileExists(t, h.ledger)
}

func TestScopeTheme_CleansUpEvenWhenTheThemeIsSwitchedOff(t *testing.T) {
	h := newThemeHome(t)
	killed(t, h)
	t.Setenv(themeEnv, "off")
	scopeTheme(t.Context(), h.home)()
	require.Equal(t, userConfig, h.read(t))
	require.NoFileExists(t, h.ledger)
}

func TestScopeTheme_KeepsTheLedgerWhenTheFileCannotBePutBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only folder does not stop root")
	}
	h := newThemeHome(t)
	undo := scopeTheme(t.Context(), h.home)
	dir := filepath.Dir(h.config)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { bestEffort(os.Chmod(dir, 0o700)) })
	undo()
	require.FileExists(t, h.ledger, "the original is still known, so a later launch can restore it")
	require.Contains(t, h.read(t), "codeforge-steel")

	require.NoError(t, os.Chmod(dir, 0o700))
	scopeTheme(t.Context(), h.home)()
	require.Equal(t, userConfig, h.read(t))
	require.NoFileExists(t, h.ledger)
}

func TestScopeTheme_AReusedPidIsNotTheCodeforgeThatLeftTheTheme(t *testing.T) {
	old := processStart
	processStart = func(int) string { return "Fri Nov  7 10:00:00 2026" }
	t.Cleanup(func() { processStart = old })

	require.True(t, holderAlive(themeHolder{Pid: os.Getpid(), Start: "Fri Nov  7 10:00:00 2026"}), "the same process")
	require.False(t, holderAlive(themeHolder{Pid: os.Getpid(), Start: "Thu Jan  1 00:00:00 1970"}), "a different process now has that pid")
	require.True(t, holderAlive(themeHolder{Pid: os.Getpid()}), "no start time was recorded: the pid decides")
	require.False(t, holderAlive(themeHolder{Pid: 2_147_483_000}))
	processStart = func(int) string { return "" } // ps is missing or too slow
	require.True(t, holderAlive(themeHolder{Pid: os.Getpid(), Start: "Fri Nov  7 10:00:00 2026"}), "an unreadable start time is not evidence of another process")
	processStart = func(int) string { return "Fri Nov  7 10:00:00 2026" }

	h := newThemeHome(t)
	scopeTheme(t.Context(), h.home) // left applied
	ledger, ok := loadThemeLedger(h.ledger)
	require.True(t, ok)
	ledger.Holders = []themeHolder{{Pid: os.Getpid(), ID: "old", Start: "Thu Jan  1 00:00:00 1970"}}
	require.NoError(t, saveThemeLedger(h.ledger, ledger))
	scopeTheme(t.Context(), h.home)()
	require.Equal(t, userConfig, h.read(t), "the stale holder with a recycled pid no longer keeps the theme in place")
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

// engineFixture is a fake engine that records what it was told and stays up until it is asked to stop.
type engineFixture struct {
	script, ready, mark, stop string
	env                       []string
}

func newEngine(t *testing.T, trapTerm string) engineFixture {
	t.Helper()
	dir := t.TempDir()
	f := engineFixture{
		script: filepath.Join(dir, "engine"), ready: filepath.Join(dir, "ready"),
		mark: filepath.Join(dir, "mark"), stop: filepath.Join(dir, "stop"),
	}
	body := "#!/bin/sh\ntrap '' INT\n" + trapTerm + "\necho up > \"$READY\"\nwhile [ ! -e \"$STOP\" ]; do sleep 0.05; done\n"
	require.NoError(t, os.WriteFile(f.script, []byte(body), execPerm))
	f.env = append(os.Environ(), "READY="+f.ready, "MARK="+f.mark, "STOP="+f.stop)
	return f
}

const exitsOnTerm = "trap 'echo term > \"$MARK\"; exit 0' TERM"

// signalWhenUp sends sig to this process once the engine is running.
func (f engineFixture) signalWhenUp(t *testing.T, sig syscall.Signal) {
	t.Helper()
	go func() {
		for range 200 {
			if _, err := os.Stat(f.ready); err == nil {
				bestEffort(syscall.Kill(os.Getpid(), sig))
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		// The engine never came up: a stray SIGHUP or SIGQUIT would kill the test binary, so nothing is sent.
	}()
}

func TestRunEngineWith_ATerminatingSignalStopsTheEngineThenUndoes(t *testing.T) {
	for name, sig := range map[string]syscall.Signal{"SIGTERM": syscall.SIGTERM, "SIGHUP": syscall.SIGHUP, "SIGQUIT": syscall.SIGQUIT} {
		t.Run(name, func(t *testing.T) {
			if sig == syscall.SIGHUP && signal.Ignored(syscall.SIGHUP) {
				t.Skip("this test run started with SIGHUP ignored (nohup, a background job): codeforge then leaves it ignored too")
			}
			f := newEngine(t, exitsOnTerm)
			var calls int
			var engineGone bool
			afterChild := func() {
				calls++
				_, err := os.Stat(f.mark)
				engineGone = err == nil // the engine wrote its mark on SIGTERM and has exited
			}
			f.signalWhenUp(t, sig)
			start := time.Now()
			require.NoError(t, runEngineWith(t.Context(), []string{f.script}, f.env, time.Time{}, os.Stdout, afterChild))
			require.Equal(t, 1, calls, "the undo ran once")
			require.True(t, engineGone, "and only after the engine had been asked to stop and was gone")
			require.Less(t, time.Since(start), 2*time.Second)
			mark, err := os.ReadFile(f.mark)
			require.NoError(t, err)
			require.Equal(t, "term\n", string(mark), "SIGHUP and SIGQUIT reach the engine as SIGTERM")
		})
	}
}

func TestRunEngineWith_CtrlCIsTheEnginesBusinessAndNeverUndoesAnything(t *testing.T) {
	f := newEngine(t, exitsOnTerm)
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- runEngineWith(t.Context(), []string{f.script}, f.env, time.Time{}, os.Stdout, func() { calls.Add(1) })
	}()
	f.signalWhenUp(t, syscall.SIGINT)
	select {
	case err := <-done:
		t.Fatalf("SIGINT ended the run (%v): in the TUI Ctrl+C only cancels a turn", err)
	case <-time.After(600 * time.Millisecond):
	}
	require.Zero(t, calls.Load(), "the theme stays while the engine runs")
	require.NoError(t, os.WriteFile(f.stop, nil, 0o600))
	require.NoError(t, <-done)
	require.Equal(t, int32(1), calls.Load(), "it is undone when the engine exits by itself")
}

func TestRunEngineWith_AnEngineThatIgnoresTermIsKilledAfterTheGraceAndThenUndone(t *testing.T) {
	old := stopGrace
	stopGrace = 300 * time.Millisecond
	t.Cleanup(func() { stopGrace = old })
	f := newEngine(t, "trap '' TERM")
	var calls int
	f.signalWhenUp(t, syscall.SIGTERM)
	start := time.Now()
	err := runEngineWith(t.Context(), []string{f.script}, f.env, time.Time{}, os.Stdout, func() { calls++ })
	var exit *EngineExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, signalExitBase+int(syscall.SIGKILL), exit.Code)
	require.Equal(t, 1, calls)
	require.Less(t, time.Since(start), 3*time.Second, "a hung engine cannot hold the undo forever")
}

func TestRunEngineWith_ASignalCaughtBeforeTheEngineStartedIsNotLost(t *testing.T) {
	f := newEngine(t, exitsOnTerm)
	early := make(chan os.Signal, 1)
	early <- syscall.SIGTERM // arrived between the caller's registration and the engine's
	var calls int
	start := time.Now()
	err := runEngineWith(t.Context(), []string{f.script}, f.env, time.Time{}, os.Stdout, func() { calls++ }, early)
	var exit *EngineExitError
	require.ErrorAs(t, err, &exit, "the engine was stopped, by the SIGTERM it was sent")
	require.Equal(t, signalExitBase+int(syscall.SIGTERM), exit.Code)
	require.Equal(t, 1, calls)
	require.Less(t, time.Since(start), 3*time.Second, "the engine was asked to stop even though nothing signalled it afterwards")
}

func TestRunEngineWith_UndoesWhenTheEngineExitsOnItsOwn(t *testing.T) {
	f := newEngine(t, exitsOnTerm)
	require.NoError(t, os.WriteFile(f.stop, nil, 0o600))
	var calls int
	require.NoError(t, runEngineWith(t.Context(), []string{f.script}, f.env, time.Time{}, os.Stdout, func() { calls++ }))
	require.Equal(t, 1, calls)
	require.NoError(t, runEngineWith(t.Context(), []string{f.script}, f.env, time.Time{}, os.Stdout, nil), "no undo is fine")
}

func TestRunThemedStyle_TheUsersThemeIsBackAfterASignalEndedTheSession(t *testing.T) {
	h := newThemeHome(t)
	f := newEngine(t, exitsOnTerm)
	undo := scopeTheme(t.Context(), h.home)
	require.Contains(t, h.read(t), "codeforge-steel")
	f.signalWhenUp(t, syscall.SIGTERM) // codeforge was asked to stop
	require.NoError(t, runEngineWith(t.Context(), []string{f.script}, f.env, time.Time{}, os.Stdout, undo))
	require.Equal(t, userConfig, h.read(t), "restored before runEngineWith returned")
	require.NoFileExists(t, h.ledger)
	undo() // the deferred call in the caller finds nothing left to do
	require.Equal(t, userConfig, h.read(t))
}
