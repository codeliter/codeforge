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

// setOurs replaces only codeforge's hooks, including stale temporary launchers.
// User hooks in the same rule and the rule's matcher are preserved.
func setOurs(hooks map[string]any, event, sub string, hook map[string]any) {
	var kept []any
	if rules, ok := hooks[event].([]any); ok {
		for _, r := range rules {
			m, ok := r.(map[string]any)
			if !ok {
				kept = append(kept, r)
				continue
			}
			list, ok := m["hooks"].([]any)
			if !ok {
				kept = append(kept, r)
				continue
			}
			var remaining []any
			for _, h := range list {
				if !isOurHook(h, sub) {
					remaining = append(remaining, h)
				}
			}
			if len(remaining) == len(list) {
				kept = append(kept, r)
			} else if len(remaining) > 0 {
				m["hooks"] = remaining
				kept = append(kept, m)
			}
		}
	}
	hooks[event] = append(kept, map[string]any{"hooks": []any{hook}})
}

// isOurHook recognises the permanent helper and codeforge's legacy commands.
// Merely mentioning codeforge in a user hook is not enough to replace it.
func isOurHook(h any, sub string) bool {
	m, ok := h.(map[string]any)
	if !ok {
		return false
	}
	cmd, ok := m["command"].(string)
	if !ok {
		return false
	}
	quoted, ok := strings.CutSuffix(cmd, " "+sub)
	if !ok {
		return false
	}
	if quoted == "/tmp/cf" || quoted == "/private/tmp/cf" {
		return true
	}
	if len(quoted) < 2 || quoted[0] != '\'' {
		return false
	}
	return strings.HasSuffix(quoted, "/codeforge'") ||
		strings.HasSuffix(quoted, "/hooks/cf'") ||
		quoted == "'/tmp/cf'" || quoted == "'/private/tmp/cf'"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
