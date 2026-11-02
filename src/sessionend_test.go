package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func sessionDirs(t *testing.T, ids ...string) string {
	t.Helper()
	cwd := t.TempDir()
	for _, id := range ids {
		require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".empryo", "sessions", id), 0o700))
	}
	return cwd
}

func TestRunSessionEnd(t *testing.T) {
	const id = "1f15c107-959c-44ea-b03b-b27add189453"
	cwd := sessionDirs(t, id, "2a000000-0000")
	payload := `{"session_id":"` + id + `","cwd":"` + cwd + `","reason":"other","transcript_path":"/secret-transcript"}`

	var out strings.Builder
	runSessionEnd(strings.NewReader(payload), &out, true)
	require.Contains(t, out.String(), "codeforge --resume 1f15c107\n")
	require.Contains(t, out.String(), "codeforge --cwd '"+cwd+"' --resume '"+id+"'\n")
	require.NotContains(t, out.String(), "secret-transcript", "the transcript path is never printed")
}

func TestRunSessionEnd_SkipsWithoutHint(t *testing.T) {
	cwd := sessionDirs(t, "abcdef12")
	tests := []struct {
		name     string
		payload  string
		launched bool
	}{
		{"not launched by codeforge", `{"session_id":"abcdef12","cwd":"` + cwd + `"}`, false},
		{"garbage", `not json`, true},
		{"unsafe id", `{"session_id":"../../x","cwd":"` + cwd + `"}`, true},
		{"id with shell metacharacters", `{"session_id":"a;rm -rf ~","cwd":"` + cwd + `"}`, true},
		{"relative cwd", `{"session_id":"abcdef12","cwd":"repo"}`, true},
		{"escape sequence in cwd", `{"session_id":"abcdef12","cwd":"/tmp/\u001b]0;x\u0007"}`, true},
		{"oversized payload", `{"session_id":"abcdef12","cwd":"/` + strings.Repeat("a", maxSessionEndBytes) + `"}`, true},
		{"overlong cwd", `{"session_id":"abcdef12","cwd":"/` + strings.Repeat("a", maxCwdBytes) + `"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			runSessionEnd(strings.NewReader(tt.payload), &out, tt.launched)
			require.Empty(t, out.String())
		})
	}
}

func TestResumeHint_QuotesCwd(t *testing.T) {
	hint := resumeHint("/tmp/it's here", "abcdef1234")
	require.Contains(t, hint, `codeforge --cwd '/tmp/it'\''s here' --resume 'abcdef1234'`)
}

func TestShortestPrefix(t *testing.T) {
	cwd := sessionDirs(t, "1f15c107-aaaa", "1f15c107-abbb", "99999999-x")
	require.Equal(t, "1f15c107-aa", shortestPrefix(cwd, "1f15c107-aaaa"))
	require.Equal(t, "99999999", shortestPrefix(cwd, "99999999-x"))
	require.Equal(t, "short", shortestPrefix(cwd, "short"))
	require.Equal(t, "1f15c107-zzzz", shortestPrefix(t.TempDir(), "1f15c107-zzzz"), "full ID when sessions cannot be listed")

	both := sessionDirs(t, "abcdefgh", "abcdefgh-2")
	require.Equal(t, "abcdefgh", shortestPrefix(both, "abcdefgh"), "an exact ID wins over a longer one")
}

func TestInstallHooks_SessionEnd(t *testing.T) {
	dir := t.TempDir()
	user := `{"hooks":{"SessionEnd":[{"hooks":[{"type":"command","command":"notify"}]},{"hooks":[{"type":"command","command":"echo codeforge session-end"}]}]}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(user), 0o600))

	require.NoError(t, installHooks(dir, "/usr/local/bin/codeforge"))
	require.NoError(t, installHooks(dir, "/opt/homebrew/bin/codeforge"))

	data, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(data), "codeforge' session-end"), "a moved binary replaces the old entry")
	require.Equal(t, 1, strings.Count(string(data), "codeforge' hook\""))
	require.Contains(t, string(data), "notify")
	require.Contains(t, string(data), "echo codeforge session-end", "a user hook that mentions codeforge is kept")
}

func TestLaunchCommand_SetsLaunchMarker(t *testing.T) {
	_, env, err := launchCommand(t.Context(), t.TempDir(), "/not-an-executable", modeChat, []string{"--session-id", "abcdef12"})
	require.NoError(t, err)
	require.Contains(t, env, launchMarker+"=1")
}
