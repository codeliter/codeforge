package guard

import (
	"slices"
	"strings"
)

// gitGlobals returns git's global options, the words between "git" and its subcommand, with values kept.
func gitGlobals(raw []string) []string {
	start := slices.IndexFunc(raw, func(a string) bool { return a[strings.LastIndex(a, "/")+1:] == "git" })
	if start < 0 {
		return nil
	}

	var out []string
	for i := start + 1; i < len(raw) && strings.HasPrefix(raw[i], "-"); i++ {
		out = append(out, raw[i])
		if gitValueOptions[raw[i]] && i+1 < len(raw) {
			i++
			out = append(out, raw[i])
		}
	}
	return out
}

// gitRepoOverride reports git global options that select another repository.
func gitRepoOverride(raw []string) bool {
	return slices.ContainsFunc(gitGlobals(raw), func(a string) bool {
		name, _, _ := strings.Cut(a, "=")
		return name == "--git-dir" || name == "--work-tree" || name == "--namespace"
	})
}

// gitValueOptions are git global options whose value is the next word.
var gitValueOptions = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--config-env": true, "--super-prefix": true, "--exec-path": true,
}

// gitEnv reports variables that point git at another repository or override its config.
func gitEnv(assign string) bool {
	name, _, _ := strings.Cut(assign, "=")
	return name == "GIT_DIR" || name == "GIT_WORK_TREE" || name == "GIT_INDEX_FILE" ||
		name == "GIT_COMMON_DIR" || strings.HasPrefix(name, "GIT_CONFIG")
}

// dropMessages removes commit and tag message values so their text is never read as flags.
func dropMessages(w []string) []string {
	out := make([]string, 0, len(w))
	for i := 0; i < len(w); i++ {
		switch {
		case w[i] == "-m" || w[i] == "--message" || w[i] == "-F" || w[i] == "--file":
			i++
		case strings.HasPrefix(w[i], "--message="):
		case len(w[i]) > 2 && w[i][0] == '-' && w[i][1] != '-' && strings.HasSuffix(w[i], "m"):
			out = append(out, w[i])
			i++
		default:
			out = append(out, w[i])
		}
	}
	return out
}
