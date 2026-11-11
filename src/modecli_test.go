package src

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The engine fixture models Empryo's contract: only headless runs submit a
// positional prompt; the TUI must reopen the conversation saved by that run.
// Jurors run beside the reviewer, so each launch writes its record under a lock: two at once never interleave. A lock
// still held after about ten seconds was left by a killed launch: the launch fails then, rather than hang or take the
// lock over (two waiters taking it over at once would interleave again).
const modeCLIEngine = `#!/bin/sh
n=0
while ! mkdir "$CODEFORGE_CLI_CAPTURE.lock" 2>/dev/null; do
  n=$((n+1))
  if [ "$n" -gt 1000 ]; then echo "capture lock stuck: $CODEFORGE_CLI_CAPTURE.lock" >&2; exit 1; fi
  sleep 0.01
done
printf 'BEGIN\n' >> "$CODEFORGE_CLI_CAPTURE"
printf '%s\n' "$@" >> "$CODEFORGE_CLI_CAPTURE"
rmdir "$CODEFORGE_CLI_CAPTURE.lock"
headless=0
id=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --headless) headless=1 ;;
    --session-id) shift; id="$1" ;;
  esac
  shift
done
if [ "$headless" = 1 ]; then
  if [ "$CODEFORGE_CLI_FAIL" != 0 ]; then exit "$CODEFORGE_CLI_FAIL"; fi
  mkdir -p ".empryo/sessions/$id"
  printf '{"id":"%s","cwd":"%s"}\n' "$id" "$PWD" > ".empryo/sessions/$id/meta.json"
  printf 'submitted\n' > ".empryo/sessions/$id/session.jsonl"
fi
`

const modeCLIGH = `#!/bin/sh
case "$1 $2" in
  'pr view') printf '%s\n' '{"number":42,"url":"https://github.com/o/r/pull/42","title":"CLI review fixture","headRefName":"main","state":"OPEN","files":[]}' ;;
  'pr diff') printf 'diff --git a/example.go b/example.go\n' ;;
  'api user') printf 'author\n' ;;
  'api --paginate') printf '%s\n' '[[{"user":{"login":"reviewer"},"state":"CHANGES_REQUESTED","body":"CLI feedback fixture"}]]' ;;
  'api graphql') printf '%s\n' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}' ;;
  *) printf 'unexpected gh call: %s\n' "$*" >&2; exit 99 ;;
esac
`

//nolint:revive // Run may replace this subprocess with the headless engine.
func TestModesCLI(t *testing.T) {
	if raw := os.Getenv("CODEFORGE_CLI_ARGS"); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		err := Run(t.Context(), args)
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
	for _, tc := range []struct {
		name, mode, detail string
		args               []string
	}{
		{"work", modeWork, "finish this change", []string{"work", "finish this change"}},
		{"unquoted work", modeWork, "finish\nthis\nchange", []string{"work", "finish", "this", "change"}},
		{"chat", modeChat, "explain this code", []string{"chat", "explain this code"}},
		{"ticket", modeTicket, "fix this bug", []string{"ticket", "--ticket", "ABC-1", "fix this bug"}},
		{"start", modeTicket, "fix this bug", []string{"start", "fix this bug"}},
		{"implicit chat", modeChat, "explain this code", []string{"explain this code"}},
		{"review", modeReview, "CLI review fixture", []string{"review", "42", "--repo", "o/r"}},
		{"respond", modeTicket, "CLI feedback fixture", []string{"respond", "42"}},
	} {
		for _, variant := range []string{"tui", "headless", "failure"} {
			t.Run(tc.name+"/"+variant, func(t *testing.T) {
				t.Parallel() // every case has its own home, workspace and fixtures
				if tc.mode == modeReview && runtime.GOOS != "darwin" {
					t.Skip("review requires macOS sandbox-exec")
				}
				home, wd, bins := t.TempDir(), t.TempDir(), t.TempDir()
				capture := filepath.Join(home, "launches")
				require.NoError(t, os.WriteFile(filepath.Join(bins, "empryo"), []byte(modeCLIEngine), execPerm))
				require.NoError(t, os.WriteFile(filepath.Join(bins, "gh"), []byte(modeCLIGH), execPerm))
				git := exec.CommandContext(t.Context(), "git", "init", "-b", "main", wd)
				out, err := git.CombinedOutput()
				require.NoError(t, err, string(out))
				args := append(append([]string(nil), tc.args...), "--effort", "low", "--model", "fixture/model", "--session-id", "cli-session")
				code := "0"
				switch variant {
				case "headless":
					args = append(args, "--headless")
				case "failure":
					code = "7"
				}
				raw, err := json.Marshal(args)
				require.NoError(t, err)
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestModesCLI$")
				cmd.Dir = wd
				cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bins+string(os.PathListSeparator)+os.Getenv("PATH"), "CODEFORGE_MODE=chat", "CODEFORGE_CLI_ARGS="+string(raw), "CODEFORGE_CLI_CAPTURE="+capture, "CODEFORGE_CLI_FAIL="+code)
				out, err = cmd.CombinedOutput()
				receipt := "codeforge: Task received."
				if tc.mode == modeReview {
					receipt = "codeforge: Review requested."
				} else if tc.name == "respond" {
					receipt = "codeforge: Review responses requested."
				}
				if variant == "headless" {
					require.NotContains(t, string(out), receipt)
				} else {
					require.Contains(t, string(out), receipt)
				}
				if variant == "failure" {
					var exit *exec.ExitError
					require.ErrorAs(t, err, &exit, string(out))
					require.Equal(t, 7, exit.ExitCode(), string(out))
				} else {
					require.NoError(t, err, string(out))
				}
				data, err := os.ReadFile(capture)
				require.NoError(t, err)
				// Triage judges (--no-profile) and the parallel jurors (the gate profile) run before the launch; only launches count as submissions.
				calls := slices.DeleteFunc(strings.Split(string(data), "BEGIN\n")[1:], func(c string) bool {
					return strings.Contains(c, "--no-profile\n") || strings.Contains(c, "--profile\ncodeforge-gate\n")
				})
				wantCalls := 1
				if variant == "tui" {
					wantCalls = 2
				}
				require.Len(t, calls, wantCalls, "submit exactly once, then reopen only after success")
				require.Contains(t, calls[0], "--headless\n")
				require.Contains(t, calls[0], "--save-session\n")
				require.Contains(t, calls[0], tc.detail)
				require.Contains(t, calls[0], "--session-id\ncli-session\n")
				require.Contains(t, calls[0], "--model\nfixture/model\n")
				require.Contains(t, calls[0], "--effort\nlow\n")
				for _, call := range calls {
					require.Contains(t, call, "--profile\n"+profileFor(tc.mode)+"\n")
					if tc.mode == modeReview {
						require.Contains(t, call, "--mode\nplan\n")
					}
				}
				if variant == "tui" {
					if tc.name == "ticket" || tc.name == "start" {
						require.Contains(t, string(out), "codeforge: reviewing the change (round 1 of 2)", "ticket work is reviewed before the TUI opens")
					} else {
						require.NotContains(t, string(out), "reviewing the change")
					}
					require.Contains(t, calls[1], "--session\ncli-session")
					require.NotContains(t, calls[1], "--headless")
					require.NotContains(t, calls[1], tc.detail)
					wd, err = filepath.EvalSymlinks(wd)
					require.NoError(t, err)
					require.NoError(t, verifyEmpryoSession(wd, "cli-session"))
				}
			})
		}
	}
}
