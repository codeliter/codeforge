package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// underScript runs the test binary in a pseudo-terminal from script(1). Nothing must query that terminal: it answers
// nothing, and a query waits for its timeout. Bubble Tea, which asks for the background colour at start-up, is for that
// reason linked only into codeforge-forms, never into codeforge.
func underScript(ctx context.Context, script string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, script, append([]string{"-q", "/dev/null"}, args...)...)
}

//nolint:revive // The subprocess exercises the real launch lifecycle.
func TestLaunchExitHint(t *testing.T) {
	if os.Getenv("CODEFORGE_SHUTDOWN_TEST") == "1" {
		args := []string{"--resume", "saved-session"}
		if os.Getenv("CODEFORGE_HEADLESS_TEST") == "1" {
			args = append(args, "--headless")
		}
		err := launch(t.Context(), os.Getenv("HOME"), os.Getenv("CODEFORGE_TEST_ENGINE"), modeChat, args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(wd)
	home := t.TempDir()
	_, err = persistSession(t.Context(), home, modeChat, []string{"--session-id", "saved-session"})
	require.NoError(t, err)
	historicalSession(t, wd, "saved-session")
	engine := filepath.Join(t.TempDir(), "engine")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\necho engine-exited\n"), execPerm))
	for _, headless := range []string{"0", "1"} {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLaunchExitHint$")
		cmd.Env = append(os.Environ(), "HOME="+home, "CODEFORGE_SHUTDOWN_TEST=1", "CODEFORGE_TEST_ENGINE="+engine, "CODEFORGE_HEADLESS_TEST="+headless)
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		if headless == "1" {
			require.NotContains(t, string(out), "codeforge --resume")
		} else {
			require.Contains(t, string(out), "codeforge --resume saved-se")
			require.Greater(t, strings.Index(string(out), "codeforge --resume"), strings.Index(string(out), "engine-exited"))
		}
	}
}

func TestExitHintColors(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("uses macOS script argument syntax")
	}
	script, err := exec.LookPath("script")
	require.NoError(t, err)
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(wd)
	home := t.TempDir()
	_, err = persistSession(t.Context(), home, modeChat, []string{"--session-id", "saved-session"})
	require.NoError(t, err)
	historicalSession(t, wd, "saved-session")
	engine := filepath.Join(t.TempDir(), "engine")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\necho engine-exited\n"), execPerm))
	for _, tt := range []struct {
		name    string
		tty     bool
		term    string
		noColor string
		colored bool
	}{
		{name: "terminal", tty: true, term: "xterm-256color", colored: true},
		{name: "redirected", term: "xterm-256color"},
		{name: "no color", tty: true, term: "xterm-256color", noColor: "1"},
		{name: "dumb terminal", tty: true, term: "dumb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLaunchExitHint$")
			if tt.tty {
				cmd = underScript(ctx, script, os.Args[0], "-test.run=^TestLaunchExitHint$")
			}
			cmd.Env = append(os.Environ(), "HOME="+home, "CODEFORGE_SHUTDOWN_TEST=1", "CODEFORGE_TEST_ENGINE="+engine, "CODEFORGE_HEADLESS_TEST=0", "TERM="+tt.term, "NO_COLOR="+tt.noColor, "EMPRYO_APPEARANCE=dark")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
			require.Contains(t, string(out), "engine-exited")
			hint := string(out)[strings.Index(string(out), "engine-exited")+len("engine-exited"):]
			require.Contains(t, hint, "codeforge --resume saved-se")
			if tt.colored {
				accent := paletteFor("dark").accent
				require.Contains(t, hint, accent+"codeforge --resume saved-se\x1b[0m")
				require.Contains(t, hint, accent+"codeforge --cwd ")
			} else {
				require.NotContains(t, hint, "\x1b[")
			}
		})
	}
}

//nolint:revive // Exit codes are observed by the parent test process.
func TestInteractiveSignals(t *testing.T) {
	if os.Getenv("CODEFORGE_SIGNAL_TEST") == "1" {
		err := runInteractive(t.Context(), []string{os.Getenv("CODEFORGE_TEST_ENGINE")}, os.Environ())
		var exit *EngineExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	for _, group := range []bool{false, true} {
		t.Run(strconv.FormatBool(group), func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "ready")
			engine := filepath.Join(dir, "engine")
			script := "#!/bin/sh\ntrap 'echo engine-stopped; exit 0' INT TERM\necho $$ > \"$CODEFORGE_PID_FILE\"\nwhile :; do sleep 0.05; done\n"
			require.NoError(t, os.WriteFile(engine, []byte(script), execPerm))
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInteractiveSignals$")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Env = append(os.Environ(), "CODEFORGE_SIGNAL_TEST=1", "CODEFORGE_TEST_ENGINE="+engine, "CODEFORGE_PID_FILE="+pidFile)
			output := filepath.Join(dir, "output")
			f, err := os.Create(output)
			require.NoError(t, err)
			defer func() { require.NoError(t, f.Close()) }()
			cmd.Stdout, cmd.Stderr = f, f
			require.NoError(t, cmd.Start())
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			waited := false
			defer func() {
				if !waited {
					if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
						t.Error(err)
					}
					if err := <-done; err == nil {
						t.Log("launcher exited before cleanup")
					}
				}
			}()
			require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 5*time.Second, 10*time.Millisecond)
			if group {
				// Mimic terminal Ctrl+C, delivered to the whole foreground process group.
				require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGINT))
			} else {
				require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
			}
			select {
			case err := <-done:
				waited = true
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("launcher did not exit after signal")
			}
			out, err := os.ReadFile(output)
			require.NoError(t, err)
			require.Equal(t, 1, strings.Count(string(out), "engine-stopped"))
		})
	}
}

func TestInteractiveTerminal(t *testing.T) {
	if os.Getenv("CODEFORGE_TTY_TEST") == "1" {
		before, err := term.GetState(int(os.Stdin.Fd()))
		require.NoError(t, err)
		require.NoError(t, runInteractive(t.Context(), []string{os.Getenv("CODEFORGE_TEST_ENGINE")}, os.Environ()))
		// Turning canonical mode back on marks the terminal "input pending" (PENDIN) until something reads from it,
		// which the next prompt would; one empty read here stands in for it, so only the settings are compared.
		fd := int(os.Stdin.Fd())
		require.NoError(t, syscall.SetNonblock(fd, true))
		_, _ = syscall.Read(fd, make([]byte, 1)) //nolint:errcheck // nothing to read (EAGAIN) is expected
		require.NoError(t, syscall.SetNonblock(fd, false))
		after, err := term.GetState(int(os.Stdin.Fd()))
		require.NoError(t, err)
		require.Equal(t, before, after)
		fmt.Println("terminal-restored")
		return
	}
	if runtime.GOOS != "darwin" {
		t.Skip("uses macOS script argument syntax")
	}
	script, err := exec.LookPath("script")
	require.NoError(t, err)
	engine := filepath.Join(t.TempDir(), "engine")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\nstty -echo -icanon\n"), execPerm))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := underScript(ctx, script, os.Args[0], "-test.run=^TestInteractiveTerminal$")
	cmd.Env = append(os.Environ(), "CODEFORGE_TTY_TEST=1", "CODEFORGE_TEST_ENGINE="+engine)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), "terminal-restored")
}

func TestShowExitHint(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"exit 0", true}, {"exit 1", false}, {"exit 130", true}, {"exit 143", true},
		{"kill -TERM $$", true}, {"kill -INT $$", true},
	} {
		t.Run(tc.command, func(t *testing.T) {
			err := exec.CommandContext(t.Context(), "sh", "-c", tc.command).Run()
			require.Equal(t, tc.want, showExitHint(err))
		})
	}
}

func TestInteractiveFailure(t *testing.T) {
	engine := filepath.Join(t.TempDir(), "engine")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\nexit 7\n"), execPerm))
	err := runInteractive(t.Context(), []string{engine}, os.Environ())
	var exit *EngineExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 7, exit.Code)
	require.False(t, showExitHint(err))
}

// TestLaunchTitleAndEmptySessionInTerminal runs the real launch path in a pseudo-terminal with a fake engine:
// the engine sees the mode title already set, the previous title comes back after it exits, and a session
// left empty by the engine prints no resume hint while a used one does.
func TestLaunchTitleAndEmptySessionInTerminal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("uses macOS script argument syntax")
	}
	script, err := exec.LookPath("script")
	require.NoError(t, err)
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(wd)
	home := t.TempDir()
	_, err = persistSession(t.Context(), home, modeChat, []string{"--session-id", "saved-session"})
	require.NoError(t, err)
	historicalSession(t, wd, "saved-session")
	meta := filepath.Join(wd, ".empryo", "sessions", "saved-session", "meta.json")
	engine := filepath.Join(t.TempDir(), "engine")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\nprintf '%s' \"$CODEFORGE_TEST_META\" > \"$CODEFORGE_TEST_META_PATH\"\necho engine-exited\n"), execPerm))
	for _, tt := range []struct {
		name string
		meta string
		hint bool
	}{
		{name: "empty session", meta: `{"id":"saved-session","cwd":"` + wd + `","tabs":[{"tokenUsage":{"total":0},"messageRange":{"endLine":0}}]}`},
		{name: "used session", meta: `{"id":"saved-session","cwd":"` + wd + `","tabs":[{"tokenUsage":{"total":42},"messageRange":{"endLine":3}}]}`, hint: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := underScript(ctx, script, os.Args[0], "-test.run=^TestLaunchExitHint$")
			cmd.Env = append(os.Environ(), "HOME="+home, "CODEFORGE_SHUTDOWN_TEST=1", "CODEFORGE_TEST_ENGINE="+engine, "CODEFORGE_HEADLESS_TEST=0", "TERM=xterm-256color", "NO_COLOR=", "CODEFORGE_TEST_META="+tt.meta, "CODEFORGE_TEST_META_PATH="+meta)
			raw, err := cmd.CombinedOutput()
			out := string(raw)
			require.NoError(t, err, out)
			exited := strings.Index(out, "engine-exited")
			require.GreaterOrEqual(t, exited, 0, out)
			title := strings.Index(out, "\x1b[22;0t\x1b]0;Codeforge · chat\x07")
			require.GreaterOrEqual(t, title, 0, "the mode title is set: %q", out)
			require.Less(t, title, exited, "the title is set before the engine draws")
			require.Greater(t, strings.LastIndex(out, "\x1b[23;0t"), exited, "the previous title is restored after the engine exits")
			require.Equal(t, tt.hint, strings.Contains(out[exited:], "Resume this session"), out[exited:])
		})
	}
}

func TestRunEngineStopsTheWholeTreeAtItsDeadline(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	engine := filepath.Join(dir, "engine")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\n(sleep 1; touch \""+child+"\") &\nsleep 30\n"), execPerm))
	start := time.Now()
	err := runEngine(t.Context(), []string{engine}, os.Environ(), time.Now().Add(300*time.Millisecond))
	var exit *EngineExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, deadlineExit, exit.Code)
	require.Less(t, time.Since(start), 900*time.Millisecond)
	time.Sleep(1500 * time.Millisecond)
	require.NoFileExists(t, child, "the engine's background child outlived the deadline")
}

func TestRunEngineWithoutDeadlineIsRunInteractive(t *testing.T) {
	engine := filepath.Join(t.TempDir(), "engine")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\nexit 0\n"), execPerm))
	require.NoError(t, runEngine(t.Context(), []string{engine}, os.Environ(), time.Time{}))
}
