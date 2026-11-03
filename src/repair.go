package src

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var errRepairUsage = errors.New("usage: codeforge repair [--legacy-hook /absolute/path]")

type repairOptions struct {
	enabled     bool
	legacyHooks []string
}

func parseRepairArgs(args []string) (repairOptions, error) {
	var opts repairOptions
	if len(args) == 0 || args[0] != "repair" && args[0] != "reinit" {
		return opts, nil
	}
	opts.enabled = true
	for i := 1; i < len(args); i++ {
		if args[i] != "--legacy-hook" || i+1 >= len(args) {
			return opts, errRepairUsage
		}
		i++
		if !filepath.IsAbs(args[i]) || strings.ContainsRune(args[i], 0) {
			return opts, fmt.Errorf("%w: legacy hook paths must be absolute", errRepairUsage)
		}
		opts.legacyHooks = append(opts.legacyHooks, args[i])
	}
	return opts, nil
}

// A renamed legacy executable needs an explicit ownership claim from the user.
// Match only the executable itself and its exact Codeforge subcommand, never a
// shell command that merely mentions the path.
func matchesLegacyHook(command, sub string, paths []string) bool {
	command = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(command), "exec "))
	for _, path := range paths {
		for _, executable := range []string{path, shellQuote(path)} {
			if command == executable || command == executable+" "+sub {
				return true
			}
		}
	}
	return false
}

func legacyHookPresent(hooks map[string]any, path string) bool {
	for _, event := range []struct{ name, sub string }{{"PreToolUse", hookCommand}, {"SessionEnd", sessionEndCommand}} {
		rules, ok := hooks[event.name].([]any)
		if !ok {
			continue
		}
		for _, rule := range rules {
			m, ok := rule.(map[string]any)
			if !ok {
				continue
			}
			list, ok := m["hooks"].([]any)
			if !ok {
				continue
			}
			for _, hook := range list {
				h, ok := hook.(map[string]any)
				if !ok {
					continue
				}
				command, ok := h["command"].(string)
				if ok && matchesLegacyHook(command, event.sub, []string{path}) {
					return true
				}
			}
		}
	}
	return false
}
