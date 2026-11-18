package main

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src"
	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/jobs"
)

// tree describes every file under roots (path, mode, size, content hash), so two snapshots can be compared.
func tree(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return fmt.Errorf("stat %s: %w", path, err)
			}
			sum := ""
			if info.Mode().IsRegular() {
				raw, err := os.ReadFile(path)
				if err != nil {
					return fmt.Errorf("reading %s: %w", path, err)
				}
				sum = fmt.Sprintf("%x", sha256.Sum256(raw))
			}
			out[path] = fmt.Sprintf("%v %d %s", info.Mode(), info.Size(), sum)
			return nil
		}))
	}
	return out
}

func TestEveryCommandAnswersHelpAndChangesNothing(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codeforge")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".").CombinedOutput()
	require.NoError(t, err, string(out))

	home, project := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codeforge"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codeforge", "config.yaml"), []byte("model: sonnet\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codeforge", "state.json"), []byte("{}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(project, "keep.txt"), []byte("mine\n"), 0o600))
	before := tree(t, home, project)

	// Each registered command with each help flag, and the forms that write: a subcommand of a command and extra flags.
	var calls [][]string
	for _, cmd := range src.RegisteredCommands() {
		if cmd == "gh-shim" {
			continue // it forwards its arguments to gh (a stale shim sends gh --help here), so it is not a help target
		}
		calls = append(calls, []string{cmd, "--help"}, []string{cmd, "-h"})
	}
	calls = append(calls,
		[]string{"--help"}, []string{"-h"}, []string{"help"},
		[]string{"models", "set", "review.model", "x", "--help"},
		[]string{"models", "apply", "--help"},
		[]string{"provider", "set", "litellm", "--help"},
		[]string{"learn", "correct", "--help"},
		[]string{"learn", "accept", "1", "-h"},
		[]string{"init", "--repo", "--help"},
		[]string{"review", "123", "--comment=yes", "--help"},
		[]string{"--cwd", project, "repair", "--help"},
	)

	for _, args := range calls {
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Dir = project
		cmd.Stdin = strings.NewReader(`{"tool_name":"Bash"}`)
		cmd.Env = append(os.Environ(), "HOME="+home, "CODEFORGE_LAUNCH=1", "CODEFORGE_GUARD=")
		stdout, err := cmd.Output()
		require.NoError(t, err, "codeforge %s must exit 0", strings.Join(args, " "))
		require.Contains(t, string(stdout), "Usage:", "codeforge %s prints usage", strings.Join(args, " "))
	}
	require.Equal(t, before, tree(t, home, project), "help left config, state and the project exactly as they were")
}

func TestEngineExitCode(t *testing.T) {
	code, ok := engineExitCode(&src.EngineExitError{Code: 143})
	require.True(t, ok)
	require.Equal(t, 143, code)
	err := exec.CommandContext(t.Context(), "sh", "-c", "exit 7").Run()
	require.Error(t, err)
	_, ok = engineExitCode(fmt.Errorf("setup failed: %w", err))
	require.False(t, ok, "non-engine subprocess errors must retain their diagnostics")
}

// screen collects what a program draws on a terminal.
type screen struct {
	mu  sync.Mutex
	raw strings.Builder
}

// read copies everything the terminal receives until the program is gone.
func (s *screen) read(tty *os.File) {
	buf := make([]byte, 4096)
	for {
		n, err := tty.Read(buf)
		s.mu.Lock()
		s.raw.Write(buf[:n])
		s.mu.Unlock()
		if err != nil {
			return // the program closed its end
		}
	}
}

// frames are the views drawn so far, each as its lines without colours, and what was drawn after the last one began.
// A frame is whole once the next one has begun.
func (s *screen) frames() (whole [][]string, last string) {
	const redraw = "\x1b[H\x1b[J" // what job watch begins every view with
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := strings.Split(strings.ReplaceAll(s.raw.String(), "\r", ""), redraw)
	for _, p := range parts[1:max(len(parts)-1, 1)] {
		whole = append(whole, strings.Split(strings.TrimSuffix(ansi.Strip(p), "\n"), "\n"))
	}
	return whole, ansi.Strip(parts[len(parts)-1])
}

// fits reports whether a frame takes no more than the terminal: its lines and the line the cursor rests on within
// rows, each line within cols.
func fits(frame []string, rows, cols int) bool {
	if len(frame)+1 > rows {
		return false
	}
	for _, l := range frame {
		if ansi.StringWidth(l) > cols {
			return false
		}
	}
	return true
}

func TestJobWatch_OnATerminalRedrawsFollowsItsSizeAndEnds(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codeforge")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".").CombinedOutput()
	require.NoError(t, err, string(out))

	const wait, tick = 20 * time.Second, 50 * time.Millisecond
	tests := []struct {
		name string
		// end is what ends the watch: the job reaching its end, or the user leaving.
		end  func(t *testing.T, st jobs.Store, j *jobs.Job, tty *os.File)
		last []string // what the last view shows
		gone []string // and what it no longer does
	}{
		{
			name: "the job ends",
			end: func(t *testing.T, st jobs.Store, j *jobs.Job, _ *os.File) {
				t.Helper()
				j.State, j.Finished = jobs.Done, time.Now()
				require.NoError(t, st.Save(j))
			},
			last: []string{"j1 · done · exit 0", "line 100"},
			gone: []string{"watching ·", "ctrl+c leaves"},
		},
		{
			name: "the user presses ctrl+c",
			end: func(t *testing.T, _ jobs.Store, _ *jobs.Job, tty *os.File) {
				t.Helper()
				_, err := tty.Write([]byte{3}) // what the terminal sends for ctrl+c: an interrupt to the program in front
				require.NoError(t, err)
			},
			last: []string{"j1 · running"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			st := jobs.Store{Dir: filepath.Join(config.Dir(home), "jobs")}
			now := time.Now()
			j := &jobs.Job{
				ID: "j1", State: jobs.Running, Name: "deploy", Command: "make deploy " + strings.Repeat("--flag ", 30), Dir: home,
				Created: now.Add(-time.Hour), Started: now.Add(-time.Minute), Expire: now.Add(time.Hour),
			}
			require.NoError(t, st.Save(j))
			var log strings.Builder
			for i := 1; i <= 100; i++ {
				fmt.Fprintf(&log, "line %d\n", i)
			}
			require.NoError(t, os.WriteFile(st.LogPath(j.ID), []byte(log.String()), 0o600))

			cmd := exec.CommandContext(t.Context(), bin, "job", "watch", j.ID)
			cmd.Dir = home
			cmd.Env = append(os.Environ(), "HOME="+home, "TERM=xterm-256color", "NO_COLOR=", "EMPRYO_APPEARANCE=dark", "CODEFORGE_GUARD=")
			tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 100})
			require.NoError(t, err)
			defer tty.Close() //nolint:errcheck // the test is over
			var s screen
			go s.read(tty)
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()

			// It redraws: one view over the last, each fitted to the terminal, the end of the log at the bottom.
			require.Eventually(t, func() bool { whole, _ := s.frames(); return len(whole) >= 2 }, wait, tick, "it draws again every second")
			whole, _ := s.frames()
			for _, frame := range whole {
				require.True(t, fits(frame, 24, 100), "a view of %d lines on 24 rows of 100:\n%s", len(frame), strings.Join(frame, "\n"))
				require.True(t, strings.HasPrefix(frame[0], "j1 · running · running for "), frame[0])
				require.Equal(t, "  line 100", frame[len(frame)-2], "the log's end is the last thing above the footer")
				require.Contains(t, frame[len(frame)-1], "watching · ")
				require.Len(t, frame, 24-1, "the view takes the rows the terminal has, less the one the cursor rests on")
			}

			// It follows the terminal when that is made smaller: fewer lines, and none wider than it.
			drawn := len(whole)
			require.NoError(t, pty.Setsize(tty, &pty.Winsize{Rows: 9, Cols: 40}))
			require.Eventually(t, func() bool {
				now, _ := s.frames()
				return len(now) > drawn && fits(now[len(now)-1], 9, 40)
			}, wait, tick, "the view is fitted to the new size at the next redraw")
			whole, _ = s.frames()
			small := whole[len(whole)-1]
			require.True(t, strings.HasPrefix(small[0], "j1 · running"), "the status line stays on a small terminal: %q", small[0])
			require.Equal(t, "  line 100", small[len(small)-2])
			require.Contains(t, small[len(small)-1], "watching")

			tt.end(t, st, j, tty)
			select {
			case err := <-exited:
				require.NoError(t, err, "leaving a watch, or the job ending, is no failure")
			case <-time.After(wait):
				require.FailNow(t, "the watch did not end")
			}
			require.Eventually(t, func() bool {
				_, last := s.frames()
				for _, want := range tt.last {
					if !strings.Contains(last, want) {
						return false
					}
				}
				return true
			}, wait, tick, "the last view stays on the screen")
			_, last := s.frames()
			for _, gone := range tt.gone {
				require.NotContains(t, last, gone)
			}
		})
	}
}
