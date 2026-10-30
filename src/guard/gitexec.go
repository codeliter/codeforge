package guard

import (
	"regexp"
	"slices"
	"strings"
)

// safeConfigKey lists the git settings the agent may change. Everything else is refused, because many keys
// make git run a program (aliases, hooks, pagers, editors, helpers, drivers, filters), and a list of the
// dangerous ones keeps missing new forms.
var safeConfigKey = regexp.MustCompile(`(?i)^(user\.(name|email|signingkey)|commit\.gpgsign|tag\.gpgsign|` +
	`pull\.(rebase|ff)|push\.(default|autosetupremote|followtags)|fetch\.prune|init\.defaultbranch|` +
	`branch\.[^.]+\.(remote|merge|rebase)|remote\.[^.]+\.(url|fetch)|rebase\.(autosquash|autostash)|` +
	`merge\.conflictstyle|diff\.(algorithm|renames|colormoved)|color\.ui|advice\..+|core\.autocrlf|log\.date)$`)

// gitConfigReader are git config forms that only read.
var gitConfigReader = []string{"get", "--get", "--get-all", "--get-regexp", "list", "--list", "-l"}

// gitExecConfig reports a git command that changes git's configuration in a way the guard cannot vouch for:
// a one-off override (`-c`, `--config-env`), or `git config` setting a key outside the safe list.
func gitExecConfig(raw []string) (string, bool) {
	globals := gitGlobals(raw)
	for i, a := range globals {
		switch {
		case strings.HasPrefix(a, "--config-env"):
			return "--config-env", true
		case a == "-c" && i+1 < len(globals):
			if key, _, _ := strings.Cut(globals[i+1], "="); !safeConfigKey.MatchString(key) {
				return key, true
			}
		case strings.HasPrefix(a, "-c") && len(a) > len("-c"):
			if key, _, _ := strings.Cut(a[len("-c"):], "="); !safeConfigKey.MatchString(key) {
				return key, true
			}
		}
	}

	start := slices.IndexFunc(raw, func(a string) bool { return a[strings.LastIndex(a, "/")+1:] == "git" })
	if start < 0 {
		return "", false
	}
	rest := raw[start+1+len(globals):]
	if len(rest) == 0 || rest[0] != "config" {
		return "", false
	}
	return configWrite(rest[1:])
}

// configWrite allows `git config` only in shapes the guard reads with certainty: a read (a read option, `get` or
// `list` as the action, or a lone key), or a write whose key is on the safe list. Everything else is refused.
func configWrite(args []string) (string, bool) {
	var operands []string
	read := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case slices.Contains(gitConfigReader, a) && strings.HasPrefix(a, "-"):
			read = true
		case a == "--type" || a == "--default" || a == "--comment" || a == "--value":
			i++
		case a == "--global" || a == "--local" || a == "--system" || a == "--worktree" || a == "--bool" ||
			a == "--int" || a == "--path" || a == "--null" || a == "-z" || a == "--show-origin" || a == "--show-scope" ||
			a == "--name-only" || a == "--includes" || a == "--no-includes":
		case strings.HasPrefix(a, "-"):
			return a, true
		default:
			operands = append(operands, a)
		}
	}

	switch {
	case read:
		return "", false
	case len(operands) == 1:
		return "", false
	case len(operands) > 0 && (operands[0] == "get" || operands[0] == "list"):
		return "", false
	case len(operands) == 3 && operands[0] == "set" && safeConfigKey.MatchString(operands[1]):
		return "", false
	case len(operands) == 2 && safeConfigKey.MatchString(operands[0]):
		return "", false
	case len(operands) == 0:
		return "", false
	}
	return "git config " + strings.Join(operands, " "), true
}

// gitExecFlag reports a git invocation that makes git run another program. Subcommands are found with the same
// parser as everything else (gitGlobals skips -C and other global options), and flags are matched in attached
// and separate forms.
func gitExecFlag(raw []string) (string, bool) {
	start := slices.IndexFunc(raw, func(a string) bool { return a[strings.LastIndex(a, "/")+1:] == "git" })
	if start < 0 {
		return "", false
	}
	rest := raw[start+1+len(gitGlobals(raw)):]
	if len(rest) == 0 {
		return "", false
	}
	sub, args := rest[0], rest[1:]

	// These subcommands exist to run commands or scripts.
	switch {
	case sub == "submodule" && slices.Contains(args, "foreach"),
		sub == "bisect" && slices.Contains(args, "run"),
		sub == "filter-branch", sub == "filter-repo", sub == "difftool", sub == "mergetool":
		return "git " + sub, true
	}

	runs := map[string][]string{
		"rebase":    {"-x", "--exec"},
		"ls-remote": {"-u", "--upload-pack"},
		"fetch":     {"--upload-pack"},
		"pull":      {"--upload-pack"},
		"clone":     {"-u", "--upload-pack", "--template"},
		"push":      {"--receive-pack", "--exec"},
		"archive":   {"--exec"},
		"diff":      {"--ext-diff"},
		"log":       {"--ext-diff"},
		"show":      {"--ext-diff"},
		"init":      {"--template"},
	}[sub]
	for _, a := range args {
		for _, flag := range runs {
			if a == flag || strings.HasPrefix(a, flag+"=") || len(flag) == 2 && strings.HasPrefix(a, flag) {
				return a, true
			}
		}
	}
	return "", false
}

// gitConfigEnv reports environment variables that inject git configuration or replace git's helper programs.
func gitConfigEnv(name string) bool {
	return strings.HasPrefix(name, "GIT_CONFIG") || name == "GIT_EXEC_PATH" || name == "GIT_SSH" ||
		name == "GIT_SSH_COMMAND" || name == "GIT_EDITOR" || name == "GIT_PAGER" || name == "GIT_ASKPASS" ||
		name == "GIT_EXTERNAL_DIFF" || name == "GIT_PROXY_COMMAND" || name == "GIT_SEQUENCE_EDITOR"
}

// gitEnvOverride finds such a variable among a command's assignments, `env` arguments, or an `export`/`set`.
func gitEnvOverride(raw []string) (string, bool) {
	for _, w := range raw {
		name, _, isAssign := strings.Cut(w, "=")
		if isAssign && gitConfigEnv(name) || !isAssign && gitConfigEnv(w) {
			return name, true
		}
	}
	return "", false
}
