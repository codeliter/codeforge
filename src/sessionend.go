package src

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
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
	if !sessionHasActivity(in.Cwd, in.SessionID) {
		return
	}
	colors := resumeHintColors(w)
	fmt.Fprint(w, sessionSummary(context.Background(), in.Cwd, colors), resumeHint(in.Cwd, in.SessionID, colors))
}

// sessionHasActivity reports whether a session holds any chat or work worth resuming. An empty session
// (opened and closed without a message) has no messages and no token use in any tab. Unreadable metadata
// counts as active, so a real session never loses its resume hint.
func sessionHasActivity(cwd, id string) bool {
	data, err := os.ReadFile(filepath.Join(cwd, ".empryo", "sessions", id, "meta.json"))
	if err != nil {
		return true
	}
	var meta struct {
		Tabs []struct {
			TokenUsage struct {
				Total int64 `json:"total"`
			} `json:"tokenUsage"` //nolint:tagliatelle // Empryo session metadata.
			MessageRange struct {
				EndLine int64 `json:"endLine"` //nolint:tagliatelle // Empryo session metadata.
			} `json:"messageRange"` //nolint:tagliatelle // Empryo session metadata.
		} `json:"tabs"`
	}
	if json.Unmarshal(data, &meta) != nil || meta.Tabs == nil {
		return true
	}
	for _, tab := range meta.Tabs {
		if tab.TokenUsage.Total > 0 || tab.MessageRange.EndLine > 0 {
			return true
		}
	}
	return false
}

func resumeHint(cwd, id string, color bool) string {
	title := "Resume this session"
	other := "From another directory"
	command := "codeforge --resume " + shortestPrefix(cwd, id)
	anywhere := fmt.Sprintf("codeforge --cwd %s --resume %s", shellQuote(cwd), shellQuote(id))
	if color {
		title = "\x1b[2m" + title + "\x1b[0m"
		other = "\x1b[2m" + other + "\x1b[0m"
		// Match the mauve accent of Empryo's default Catppuccin terminal theme.
		command = "\x1b[1;38;2;203;166;247m" + command + "\x1b[0m"
		anywhere = "\x1b[1;38;2;203;166;247m" + anywhere + "\x1b[0m"
	}
	return fmt.Sprintf("\n  %s\n    %s\n\n  %s\n    %s\n\n", title, command, other, anywhere)
}

func resumeHintColors(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd())) && os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == ""
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
