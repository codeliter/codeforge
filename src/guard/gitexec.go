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

// configWrite finds the key a `git config` invocation would change. Pure reads pass; a write to a key outside
// the safe list, or a form the guard cannot read with certainty (edit, unset, rename, files), is refused.
func configWrite(args []string) (string, bool) {
	var operands []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case slices.Contains(gitConfigReader, a):
			return "", false
		case a == "--file" || a == "-f" || a == "--blob" || a == "--type" || a == "--default" || a == "--value" || a == "--comment":
			if a == "--file" || a == "-f" || a == "--blob" {
				return a, true
			}
			i++
		case strings.HasPrefix(a, "-"):
			if a == "--edit" || a == "-e" || strings.HasPrefix(a, "--unset") || strings.HasPrefix(a, "--rename") ||
				strings.HasPrefix(a, "--remove") || strings.HasPrefix(a, "--replace") || strings.HasPrefix(a, "--add") ||
				strings.HasPrefix(a, "--file=") {
				return a, true
			}
		default:
			operands = append(operands, a)
		}
	}

	// Modern form: git config <subcommand> key [value].
	if len(operands) > 0 && slices.Contains([]string{"set", "unset", "rename-section", "remove-section", "edit"}, operands[0]) {
		if operands[0] != "set" || len(operands) < 2 || !safeConfigKey.MatchString(operands[1]) {
			return "git config " + operands[0], true
		}
		return "", false
	}
	if len(operands) > 0 && slices.Contains([]string{"get", "list"}, operands[0]) {
		return "", false
	}

	// Classic form: git config key [value]; a key alone only reads.
	if len(operands) > 1 && !safeConfigKey.MatchString(operands[0]) {
		return operands[0], true
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
