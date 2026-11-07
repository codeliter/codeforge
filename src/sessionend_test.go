package src

import (
	"context"
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
	require.NotContains(t, out.String(), "\x1b", "redirected hook output stays plain")
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
	hint := resumeHint(t.Context(), "/tmp/it's here", "abcdef1234", false)
	require.Contains(t, hint, `codeforge --cwd '/tmp/it'\''s here' --resume 'abcdef1234'`)
}

func TestResumeHint_Layout(t *testing.T) {
	const id = "1f15c107-959c-44ea-b03b-b27add189453"
	cwd := sessionDirs(t, id)
	want := "\n  Resume this session\n    codeforge --resume 1f15c107\n\n  From another directory\n    codeforge --cwd " + shellQuote(cwd) + " --resume " + shellQuote(id) + "\n\n"
	require.Equal(t, want, resumeHint(t.Context(), cwd, id, false))
}

func TestResumeHint_Colors(t *testing.T) {
	const cwd = "/tmp/it's here"
	const id = "abcdef1234"
	old := appearanceProbe
	appearanceProbe = func(context.Context) string { return "dark" }
	t.Cleanup(func() { appearanceProbe = old })
	pal := paletteFor("dark")
	plain := resumeHint(t.Context(), cwd, id, false)
	colored := resumeHint(t.Context(), cwd, id, true)
	require.NotContains(t, plain, "\x1b")
	require.Contains(t, colored, pal.dim+"Resume this session\x1b[0m")
	require.Contains(t, colored, pal.dim+"From another directory\x1b[0m")
	require.Contains(t, colored, pal.accent+"codeforge --resume "+id+"\x1b[0m")
	require.Contains(t, colored, pal.accent+"codeforge --cwd '/tmp/it'\\''s here' --resume 'abcdef1234'\x1b[0m")
	stripped := strings.NewReplacer(pal.dim, "", pal.accent, "", "\x1b[0m", "").Replace(colored)
	require.Equal(t, plain, stripped, "color must not alter either copyable command")
	var out strings.Builder
	require.False(t, resumeHintColors(&out), "non-terminal writers must stay plain")
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

func TestSessionHasActivity(t *testing.T) {
	cwd := t.TempDir()
	write := func(id, meta string) {
		dir := filepath.Join(cwd, ".empryo", "sessions", id)
		require.NoError(t, os.MkdirAll(dir, privateDir))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), privateFile))
	}
	write("empty", `{"tabs":[{"tokenUsage":{"total":0},"messageRange":{"startLine":0,"endLine":0}}]}`)
	write("chat", `{"tabs":[{"tokenUsage":{"total":0},"messageRange":{"endLine":0}},{"tokenUsage":{"total":12},"messageRange":{"endLine":4}}]}`)
	write("messages", `{"tabs":[{"tokenUsage":{"total":0},"messageRange":{"endLine":2}}]}`)
	write("odd", `not json`)
	require.False(t, sessionHasActivity(cwd, "empty"))
	require.True(t, sessionHasActivity(cwd, "chat"))
	require.True(t, sessionHasActivity(cwd, "messages"))
	require.True(t, sessionHasActivity(cwd, "odd"), "unreadable metadata keeps the hint")
	require.True(t, sessionHasActivity(cwd, "missing"), "missing metadata keeps the hint")
	var out strings.Builder
	runSessionEnd(strings.NewReader(`{"session_id":"empty","cwd":"`+cwd+`"}`), &out, true)
	require.Empty(t, out.String(), "an empty session prints no resume hint")
}
