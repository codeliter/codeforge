package src

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// installHooks merges codeforge's guard and resume hint into ~/.empryo/hooks.json, keeping any hooks the user added.
// The guard is global and fail-closed because hard stops must hold even when the model ignores its instructions.
// The resume hint is fail-open: a shutdown must never wait on it.
func installHooks(empryoDir, self string) error {
	path := filepath.Join(empryoDir, "hooks.json")

	doc := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("reading hooks: %w", err)
	default:
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("decoding %s: %w", path, err)
		}
	}

	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
	}

	const hookTimeoutSeconds = 10
	setOurs(hooks, "PreToolUse", hookCommand, map[string]any{
		"type":       "command",
		"command":    shellQuote(self) + " " + hookCommand,
		"timeout":    hookTimeoutSeconds,
		"failClosed": true,
	})
	// Empryo gives each SessionEnd hook at most 3 seconds while it shuts down.
	const sessionEndTimeoutSeconds = 3
	setOurs(hooks, "SessionEnd", sessionEndCommand, map[string]any{
		"type":    "command",
		"command": shellQuote(self) + " " + sessionEndCommand,
		"timeout": sessionEndTimeoutSeconds,
	})
	doc["hooks"] = hooks

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding hooks: %w", err)
	}
	if have, err := os.ReadFile(path); err == nil && string(have) == string(out)+"\n" {
		return nil
	}
	return writeFile(empryoDir, "hooks.json", append(out, '\n'), privateFile)
}

const (
	hookCommand       = "hook"
	sessionEndCommand = "session-end"
)

// setOurs replaces codeforge's rule for one event and keeps every other rule as it was.
func setOurs(hooks map[string]any, event, sub string, hook map[string]any) {
	var kept []any
	if rules, ok := hooks[event].([]any); ok {
		for _, r := range rules {
			if !isOurs(r, sub) {
				kept = append(kept, r)
			}
		}
	}
	hooks[event] = append(kept, map[string]any{"hooks": []any{hook}})
}

// isOurs matches only entries codeforge wrote: a single-quoted path to a binary named codeforge, then sub.
// A user hook that merely mentions codeforge is kept.
func isOurs(rule any, sub string) bool {
	m, ok := rule.(map[string]any)
	if !ok {
		return false
	}
	list, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range list {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		cmd, ok := hm["command"].(string)
		if !ok {
			continue
		}
		quoted, ok := strings.CutSuffix(cmd, " "+sub)
		if ok && len(quoted) > 1 && quoted[0] == '\'' && strings.HasSuffix(quoted, "/codeforge'") {
			return true
		}
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
