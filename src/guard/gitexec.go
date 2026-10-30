package guard

import (
	"regexp"
	"slices"
	"strings"
)

// execConfigKey matches git config keys whose value git runs as a program.
var execConfigKey = regexp.MustCompile(`(?i)^(alias\..+|hook\..+|core\.(hookspath|pager|editor|sshcommand|fsmonitor|askpass|gitproxy)|` +
	`sequence\.editor|diff\.external|diff\..+\.(textconv|command)|merge\..+\.driver|filter\..+\.(clean|smudge|process)|` +
	`credential\.helper|credential\..+\.helper|gpg\.program|gpg\..+\.program|pager\..+|uploadpack\.packobjectshook|` +
	`remote\..+\.(uploadpack|receivepack)|url\..+\.insteadof)$`)

// gitExecConfig reports a git command that sets config git would run as a program: a one-off `-c key=value`,
// `--config-env`, or `git config key value`. Shell aliases (`!cmd`) count; plain aliases to git subcommands do not.
func gitExecConfig(raw []string) (string, bool) {
	globals := gitGlobals(raw)
	for i, a := range globals {
		switch {
		case strings.HasPrefix(a, "--config-env"):
			return "--config-env", true
		case a == "-c" && i+1 < len(globals):
			if key, ok := execSetting(globals[i+1]); ok {
				return key, true
			}
		case strings.HasPrefix(a, "-c") && len(a) > len("-c"):
			if key, ok := execSetting(a[len("-c"):]); ok {
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
	var operands []string
	for _, a := range rest[1:] {
		if !strings.HasPrefix(a, "-") {
			operands = append(operands, a)
		}
	}
	// `git config key value` sets; a key alone only reads.
	if len(operands) > 1 {
		if key, ok := execSetting(operands[0] + "=" + operands[1]); ok {
			return key, true
		}
	}
	return "", false
}

func execSetting(kv string) (string, bool) {
	key, value, _ := strings.Cut(kv, "=")
	if !execConfigKey.MatchString(key) {
		return "", false
	}
	if strings.HasPrefix(strings.ToLower(key), "alias.") && !strings.HasPrefix(strings.TrimSpace(value), "!") {
		return "", false
	}
	return key, true
}
