package guard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// readTools never write, so they may look at protected files.
var readTools = []string{"read", "Read", "grep", "Grep", "Glob", "find", "navigate", "list_dir", "LS"}

var (
	hookConfigDirs  = []string{".empryo", ".claude", ".codex", ".copilot"}
	hookConfigFiles = []string{"hooks.json", "config.json", "settings.json", "settings.local.json", "config.toml"}
)

// protectedPath reports files that would let the agent approve itself or switch the guard off:
// codeforge's own state and any agent config that can disable hooks.
func protectedPath(p, cwd, home string) bool {
	p = expand(p, home)
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)

	if home != "" {
		cf := filepath.Join(home, ".codeforge")
		if p == cf || strings.HasPrefix(p, cf+string(filepath.Separator)) {
			return true
		}
	}

	if home != "" && p == filepath.Join(home, ".empryo", "bin", "gh") {
		return true
	}
	// Git hooks and repository config run code outside the agent's tool calls, so the agent may not write them.
	if filepath.Base(p) == "config" && filepath.Base(filepath.Dir(p)) == ".git" || strings.Contains(filepath.ToSlash(p), "/.git/hooks/") {
		return true
	}

	dir := filepath.Base(filepath.Dir(p))
	return slices.Contains(hookConfigDirs, dir) && slices.Contains(hookConfigFiles, filepath.Base(p)) ||
		slices.Contains(hookConfigDirs, dir) && strings.HasPrefix(filepath.Base(filepath.Dir(filepath.Dir(p))), ".") && filepath.Base(p) == "hooks.json" ||
		strings.Contains(filepath.ToSlash(p), "/.copilot/hooks/") || strings.Contains(filepath.ToSlash(p), "/.github/hooks/")
}

func expand(p, home string) string {
	switch {
	case p == "~" || p == "$HOME" || p == "${HOME}":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	case strings.HasPrefix(p, "$HOME/"):
		return filepath.Join(home, p[len("$HOME/"):])
	case strings.HasPrefix(p, "${HOME}/"):
		return filepath.Join(home, p[len("${HOME}/"):])
	}
	return p
}

// checkProtected blocks any non-read tool that touches a protected path, in every mode.
func checkProtected(in Input) []Finding {
	if slices.Contains(readTools, in.Tool) {
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	for _, p := range in.Paths {
		if protectedPath(p, in.Cwd, home) || protectedAncestor(p, in.Cwd, home) {
			return []Finding{{RuleSelf, "touches " + p + ", which controls codeforge's approvals or the guard itself"}}
		}
	}
	return nil
}

// protectedAncestor catches moving or deleting a folder that holds enforcement files, such as ~/.empryo/bin.
func protectedAncestor(p, cwd, home string) bool {
	p = expand(p, home)
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)

	// Project-local folders that hold hook config or git hooks, wherever the repository is.
	switch filepath.Base(p) {
	case ".empryo", ".claude", ".codex", ".copilot", ".github":
		return true
	case "hooks":
		parent := filepath.Base(filepath.Dir(p))
		return parent == ".git" || parent == ".github" || parent == ".copilot"
	}

	if home == "" {
		return false
	}
	for _, f := range []string{
		filepath.Join(home, ".empryo", "bin", "gh"),
		filepath.Join(home, ".empryo", "hooks.json"),
		filepath.Join(home, ".empryo", "config.json"),
		filepath.Join(home, ".codeforge", "state.json"),
	} {
		if p != f && strings.HasPrefix(f, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
