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
// The guard is registered globally and fail-closed, but runHook enforces the full rules only in review mode, where the
// read-only promise must hold even when the model ignores its instructions. Other sessions are gated only against
// the agent writing learning evidence (see DecideOutsideReview).
// The resume hint is fail-open: a shutdown must never wait on it.
func installHooks(empryoDir, self string, legacyHooks ...string) error {
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

	for _, legacy := range legacyHooks {
		if !legacyHookPresent(hooks, legacy) {
			fmt.Fprintf(os.Stderr, "codeforge: no matching legacy hook for %q in %s; check project or other agent hook settings if the error continues.\n", legacy, path)
		}
	}

	const hookTimeoutSeconds = 10
	setOurs(hooks, "PreToolUse", hookCommand, "", map[string]any{
		"type":       "command",
		"command":    shellQuote(self) + " " + hookCommand,
		"timeout":    hookTimeoutSeconds,
		"failClosed": true,
	}, legacyHooks...)
	// Empryo gives each SessionEnd hook at most 3 seconds while it shuts down.
	const sessionEndTimeoutSeconds = 3
	setOurs(hooks, "SessionEnd", sessionEndCommand, "", map[string]any{
		"type":    "command",
		"command": shellQuote(self) + " " + sessionEndCommand,
		"timeout": sessionEndTimeoutSeconds,
	}, legacyHooks...)
	// Learning: Empryo reports whether each shell command succeeded or failed. Async and fail-open, so it never
	// slows or blocks a tool call.
	for _, event := range []string{"PostToolUse", "PostToolUseFailure"} {
		setOurs(hooks, event, observeCommand, "Bash", map[string]any{
			"type":    "command",
			"command": shellQuote(self) + " " + observeCommand,
			"timeout": observeTimeoutSec,
			"async":   true,
		}, legacyHooks...)
	}
	// Corrections: Empryo sends each prompt the user submits. Same rules as above: async and fail-open.
	setOurs(hooks, promptEvent, observeCommand, "", map[string]any{
		"type":    "command",
		"command": shellQuote(self) + " " + observeCommand,
		"timeout": observeTimeoutSec,
		"async":   true,
	}, legacyHooks...)
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
func setOurs(hooks map[string]any, event, sub, matcher string, hook map[string]any, legacyHooks ...string) {
	rule := map[string]any{"hooks": []any{hook}}
	if matcher != "" {
		rule["matcher"] = matcher
	}
	hooks[event] = append(keepOthers(hooks[event], sub, legacyHooks), rule)
}

// keepOthers returns the rules of an event without codeforge's hooks.
func keepOthers(event any, sub string, legacyHooks []string) []any {
	var kept []any
	if rules, ok := event.([]any); ok {
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
				if !isOurHook(h, sub, legacyHooks...) {
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
	return kept
}

// isOurHook recognises the permanent helper and codeforge's legacy commands.
// Merely mentioning codeforge in a user hook is not enough to replace it.
func isOurHook(h any, sub string, legacyHooks ...string) bool {
	m, ok := h.(map[string]any)
	if !ok {
		return false
	}
	cmd, ok := m["command"].(string)
	if !ok {
		return false
	}
	if matchesLegacyHook(cmd, sub, legacyHooks) {
		return true
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
