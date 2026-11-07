package guard

import (
	"errors"
	"fmt"
	"strings"
)

var ErrGHRefused = errors.New("codeforge refused this gh command")

// CheckGH decides a real gh invocation at the binary boundary. It reuses the command rules on the exact argv
// gh received, so there is no shell text to misread: whatever built the call, these are the arguments that run.
func CheckGH(argv []string) error {
	found := classifyGH(append([]string{"gh"}, argv...))
	if len(found) == 0 {
		return nil
	}

	problems := make([]string, 0, len(found))
	for _, f := range found {
		problems = append(problems, f.Reason)
	}
	return fmt.Errorf("%w:\n- %s", ErrGHRefused, strings.Join(problems, "\n- "))
}

// ghGlobalRepo removes gh's -R/--repo wherever it appears, so the subcommand is always w[1].
func ghGlobalRepo(w []string) []string {
	out := []string{w[0]}
	for i := 1; i < len(w); i++ {
		name, _, hasValue := strings.Cut(w[i], "=")
		attached := strings.HasPrefix(w[i], "-R") && len(w[i]) > len("-R")
		if name == "-R" || name == "--repo" || attached {
			if !hasValue && !attached {
				i++
			}
			continue
		}
		out = append(out, w[i])
	}
	return out
}

// knownGH lists gh's built-in commands. Anything else may be a user alias, which gh expands into any
// command (including `pr merge`), so it is refused rather than guessed.
var knownGH = set(
	"alias", "api", "attestation", "auth", "browse", "cache", "co", "codespace", "completion", "config",
	"extension", "gist", "gpg-key", "help", "issue", "label", "org", "pr", "project", "release", "repo",
	"ruleset", "run", "search", "secret", "ssh-key", "status", "variable", "workflow", "version",
	"--version", "--help", "-h",
)

// classifyGH refuses only gh aliases and extensions, which can run any command unseen. Opening, merging and
// commenting on PRs, and every gh api call, need no approval.
func classifyGH(w []string) []Finding {
	w = ghGlobalRepo(w)
	if len(w) >= minGitTokens && !knownGH[w[1]] {
		return []Finding{{RuleOpaque, "gh " + w[1] + " is an alias or extension the guard cannot inspect; run the underlying gh command instead"}}
	}
	if len(w) >= minGitTokens && w[1] == "alias" && len(w) > 2 && w[2] != "list" {
		return []Finding{{RuleSelf, "changes gh aliases, which could hide commands from codeforge"}}
	}
	return nil
}

var apiValueFlags = set("-X", "--method", "-f", "-F", "--field", "--raw-field", "-H", "--header", "--input", "-q", "--jq", "-t", "--template", "--hostname", "--cache")

// apiWrites mirrors gh: an explicit method wins, otherwise any field or input flag makes the request a POST.
// Option values are consumed, so a field value that looks like a flag is never read as one.
func apiWrites(args []string) bool {
	method := ""
	hasBody := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, attached := strings.Cut(a, "=")
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}

		switch {
		case a == "--":
			return method != "" && !readMethod(method) || method == "" && hasBody
		case name == "-X" || name == "--method":
			if attached {
				method = value
			} else {
				method = next()
			}
		case strings.HasPrefix(a, "-X") && len(a) > len("-X"):
			method = a[len("-X"):]
		case name == "-f" || name == "-F" || name == "--field" || name == "--raw-field" || name == "--input":
			hasBody = true
			if !attached {
				next()
			}
		case len(a) > 2 && (strings.HasPrefix(a, "-f") || strings.HasPrefix(a, "-F")) && a[1] != '-':
			hasBody = true
		case apiValueFlags[name] && !attached:
			next()
		}
	}

	if method != "" {
		return !readMethod(method)
	}
	return hasBody
}

func readMethod(m string) bool {
	return strings.EqualFold(m, "GET") || strings.EqualFold(m, "HEAD")
}

// prComment reports `gh pr comment` in the one form review mode offers for approval: the text on the command line
// (-b or --body, once), at most one PR, and nothing else but --repo. The user is shown the whole call, so every option
// must be one that changes what is posted in a way the call itself shows. Anything else fails closed: a body from a file,
// an editor or stdin, a browser, an attachment (which uploads a file the user is not shown), and editing, deleting or
// reusing an earlier comment. Option values are consumed, so a body that reads like a flag stays a body.
func prComment(w []string) bool {
	if len(w) < 3 || w[1] != "pr" || w[2] != "comment" {
		return false
	}
	bodies, targets := 0, 0
	args := w[3:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		long := strings.HasPrefix(a, "--")
		switch {
		case a == "-b" || a == "--body":
			if i+1 >= len(args) {
				return false
			}
			i++
			bodies++
		case strings.HasPrefix(a, "--body="):
			bodies++
		case !long && strings.HasPrefix(a, "-b") && len(a) > len("-b"):
			bodies++
		case a == "-R" || a == "--repo":
			if i+1 >= len(args) {
				return false
			}
			i++
		case strings.HasPrefix(a, "--repo="), !long && strings.HasPrefix(a, "-R") && len(a) > len("-R"):
		case strings.HasPrefix(a, "-"):
			return false
		default:
			targets++
		}
	}
	return bodies == 1 && targets <= 1
}

func ghWrites(w []string) bool {
	if w[1] == "api" {
		return apiWrites(w[2:])
	}
	return len(w) > 2 && !hasAny(w[2:3], "view", "list", "diff", "checks", "status")
}
