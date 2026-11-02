package src

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// launchMarker is set only on engines codeforge starts; EMPRYO_HOOK_ENV=CODEFORGE_* passes it to hooks.
	launchMarker       = "CODEFORGE_LAUNCH"
	maxSessionEndBytes = 64 * 1024
	maxCwdBytes        = 4096
	minResumePrefix    = 8
)

// SessionEnd prints how to resume the session that just ended. It always exits 0, so a shutdown never waits on it.
func SessionEnd() int {
	if os.Getenv("CODEFORGE_RESUME_HINT") == "launcher" {
		return 0
	}
	runSessionEnd(os.Stdin, os.Stderr, os.Getenv(launchMarker) == "1")
	return 0
}

// runSessionEnd writes the hint only for codeforge-launched engines and a well-formed payload; anything else is
// silently skipped, because this runs during shutdown where nobody can act on an error.
func runSessionEnd(stdin io.Reader, w io.Writer, launched bool) {
	if !launched {
		return
	}
	var in struct {
		SessionID string `json:"session_id"` //nolint:tagliatelle // Empryo's hook payload field.
		Cwd       string `json:"cwd"`
	}
	if json.NewDecoder(io.LimitReader(stdin, maxSessionEndBytes)).Decode(&in) != nil {
		return
	}
	if !safeSessionID(in.SessionID) || !printablePath(in.Cwd) || !filepath.IsAbs(in.Cwd) {
		return
	}
	fmt.Fprint(w, resumeHint(in.Cwd, in.SessionID))
}

func resumeHint(cwd, id string) string {
	return fmt.Sprintf("codeforge: resume this session from %s with:\n  codeforge --resume %s\nor from anywhere with:\n  codeforge --cwd %s --resume %s\n",
		cwd, shortestPrefix(cwd, id), shellQuote(cwd), shellQuote(id))
}

// shortestPrefix is the shortest prefix of id, at least minResumePrefix long, that no other session in the
// workspace shares; it falls back to the full ID when the sessions cannot be listed.
func shortestPrefix(cwd, id string) string {
	entries, err := os.ReadDir(filepath.Join(cwd, ".empryo", "sessions"))
	if err != nil {
		return id
	}
	n := min(minResumePrefix, len(id))
	for _, e := range entries {
		other := e.Name()
		if !e.IsDir() || other == id {
			continue
		}
		shared := 0
		for shared < len(id) && shared < len(other) && id[shared] == other[shared] {
			shared++
		}
		n = max(n, min(shared+1, len(id)))
	}
	return id[:n]
}

// printablePath refuses control characters, so a crafted path cannot send escape sequences to the terminal.
func printablePath(p string) bool {
	if p == "" || len(p) > maxCwdBytes || !utf8.ValidString(p) {
		return false
	}
	return !strings.ContainsFunc(p, func(r rune) bool { return unicode.IsControl(r) || r == utf8.RuneError })
}
