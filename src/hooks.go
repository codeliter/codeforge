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

// installHooks merges codeforge's guard into ~/.empryo/hooks.json, keeping any hooks the user added.
// It is global and fail-closed because hard stops must hold even when the model ignores its instructions.
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

	var kept []any
	if rules, ok := hooks["PreToolUse"].([]any); ok {
		for _, r := range rules {
			if !isOurs(r) {
				kept = append(kept, r)
			}
		}
	}

	const hookTimeoutSeconds = 10
	ours := map[string]any{
		"hooks": []any{map[string]any{
			"type":       "command",
			"command":    shellQuote(self) + " hook",
			"timeout":    hookTimeoutSeconds,
			"failClosed": true,
		}},
	}
	hooks["PreToolUse"] = append(kept, ours)
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

func isOurs(rule any) bool {
	m, ok := rule.(map[string]any)
	if !ok {
		return false
	}
	list, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range list {
		if hm, ok := h.(map[string]any); ok {
			if cmd, ok := hm["command"].(string); ok && strings.HasSuffix(cmd, " hook") && strings.Contains(cmd, "codeforge") {
				return true
			}
		}
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
