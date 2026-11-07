package guard

import (
	"strings"
)

// Merges, PR creation, PR comments and infra changes follow the user's requests, not approvals. Git itself is never
// guarded: it is the user's tool, and review mode keeps it read-only through the sandbox and the read-only check instead.
const (
	RuleReadOnly = "read-only"
	RuleSelf     = "codeforge-config"
	RuleLearning = "learning-evidence"
	RuleOpaque   = "opaque"
	RuleScript   = "script"
	// RuleComment marks a PR comment with its text on the command line: the one write a review session may offer to
	// the user to approve, since they see the exact comment before it goes up.
	RuleComment = "pr-comment"
)

const minGitTokens = 2

type Finding struct {
	Rule   string
	Reason string
}

type Result struct {
	Blocked []Finding

	// commands counts the commands the line runs, so a PR comment is offered for approval only when it is the only one.
	commands int

	cwd string
}

// Check classifies a shell command line. readOnly adds the rules for review mode.
func Check(command string, readOnly bool) Result {
	return checkIn(command, readOnly, "")
}

// checkIn is Check with the tool's working directory, so script files named in the command can be read.
func checkIn(command string, readOnly bool, cwd string) Result {
	// A backslash-newline is a line continuation: the shell joins the lines, so the guard must too.
	command = strings.ReplaceAll(command, "\\\n", "")
	r := Result{cwd: cwd}
	if !readOnly && substitutedLearning(command) {
		r.Blocked = append(r.Blocked, Finding{RuleLearning, learningReason})
		return r
	}
	dir := ""
	for _, seg := range segments(command) {
		// Every check reads the same redirect-free words, so an operator such as `>/dev/null` placed among
		// git's options cannot hide them. Only the write check needs the redirects themselves.
		tokens, redirect := shellTokens(seg)
		if !redirectsWellFormed(tokens, redirect) {
			r.Blocked = append(r.Blocked, Finding{RuleOpaque, "the command uses process substitution or a redirect the guard cannot read"})
			continue
		}
		raw := withoutRedirects(tokens, redirect)

		// A file write through a redirect counts before any other check, whatever the command is.
		if readOnly && writesFile(tokens, redirect) {
			r.Blocked = append(r.Blocked, Finding{RuleReadOnly, "redirects output into a file"})
			continue
		}

		// Environment that injects git config or replaces git's helper programs runs code outside the guard,
		// whether set inline (`GIT_CONFIG_KEY_0=… git x`), through env, or exported for later commands.
		if name, ok := gitEnvOverride(raw); ok {
			r.Blocked = append(r.Blocked, Finding{RuleOpaque, name + " changes what git runs; set it yourself if you need it"})
			continue
		}

		_, opaque := unwrap(raw)
		words := commandWords(seg)
		if strings.TrimSpace(seg) != "" {
			// A segment with no command in it, such as PATH=x, still changes what the next command does.
			r.commands++
		}
		if len(words) == 0 {
			continue
		}

		// A wrapper such as tmux or script can hand a quoted `codeforge observe` to a shell the guard never sees.
		if !readOnly && mentionsLearningWrite(words, seg) {
			r.Blocked = append(r.Blocked, Finding{RuleLearning, learningReason})
			continue
		}

		// A command name built from a variable can spell `codeforge observe` where no pattern reads it. This only
		// sees the name in the same command string; one carried across a shell boundary in the environment is not caught.
		if !readOnly && strings.HasPrefix(words[0], "$") && learningText.MatchString(command) {
			r.Blocked = append(r.Blocked, Finding{RuleLearning, learningReason})
			continue
		}

		if opaque {
			if readOnly {
				r.Blocked = append(r.Blocked, Finding{RuleReadOnly, "the command is wrapped in a way the read-only check cannot inspect"})
				continue
			}
			if learningText.MatchString(seg) {
				r.Blocked = append(r.Blocked, Finding{RuleLearning, learningReason})
				continue
			}
			if guardedTool.MatchString(seg) {
				r.Blocked = append(r.Blocked, Finding{RuleOpaque, "the command runs gh or codeforge through a wrapper, another directory or a git environment override the guard cannot follow"})
				continue
			}
		}

		// Git is the user's own tool: outside review mode every git command is allowed as written, except setting
		// config that makes git run another program (shell aliases, hooks, pagers, helpers). Those launch commands
		// outside the agent's tool calls, so gh or codeforge could run unchecked through them.
		if words[0] == "git" && !readOnly {
			if key, ok := gitExecConfig(raw); ok {
				r.Blocked = append(r.Blocked, Finding{RuleOpaque, "setting " + key + " makes git run another program outside the guard; set it yourself if you need it"})
			} else if flag, ok := gitExecFlag(raw); ok {
				r.Blocked = append(r.Blocked, Finding{RuleOpaque, flag + " makes git run another program outside the guard; run it yourself if you need it"})
			}
			continue
		}

		if words[0] == "git" && gitRepoOverride(raw) {
			if readOnly {
				r.Blocked = append(r.Blocked, Finding{RuleReadOnly, "git config or repository overrides can run commands or change another repository"})
				continue
			}
			r.Blocked = append(r.Blocked, Finding{RuleOpaque, "--git-dir or --work-tree points git at another repository"})
			continue
		}

		if words[0] == "cd" || words[0] == "pushd" {
			dir = joinDir(dir, arg(words, 1))
			continue
		}

		if inner, ok := shellScript(words); ok {
			r.merge(checkIn(inner, readOnly, cwd))
			continue
		}

		// A script file runs commands the hook never sees, so it must not contain guarded tools.
		if path, ok := scriptPath(unwrapped(raw)); ok && !readOnly {
			if scriptWritesLearning(joinDir(dir, path), r.cwd) {
				r.Blocked = append(r.Blocked, Finding{RuleLearning, learningReason})
				continue
			}
			if f, bad := scriptCallsGuarded(joinDir(dir, path), r.cwd); bad {
				r.Blocked = append(r.Blocked, f)
				continue
			}
		}

		if inner, ok := interpreterCode(words); ok {
			if readOnly {
				r.Blocked = append(r.Blocked, Finding{RuleReadOnly, words[0] + " runs code that can change files"})
				continue
			}
			if learningText.MatchString(inner) {
				r.Blocked = append(r.Blocked, Finding{RuleLearning, learningReason})
				continue
			}
			if guardedTool.MatchString(inner) {
				r.Blocked = append(r.Blocked, Finding{RuleOpaque, words[0] + " code calls gh or codeforge, which the guard cannot inspect inside an interpreter"})
			}
			continue
		}

		if f, writes := notReadOnly(words, raw, false); writes && readOnly {
			r.Blocked = append(r.Blocked, f)
			continue
		}
		r.Blocked = append(r.Blocked, classify(words)...)
	}

	// A PR comment is offered for approval only as written. A variable or command substitution anywhere in the command
	// line would be replaced by the shell with text the user was never shown (the split into segments above hides a
	// substitution from the segment that holds the body), so the comment is refused instead of offered.
	if expandsInShell(command) {
		for i, f := range r.Blocked {
			if f.Rule == RuleComment {
				r.Blocked[i] = Finding{RuleReadOnly, "gh pr comment with a variable or command substitution would post text you cannot see"}
			}
		}
	}
	return r
}

func classify(w []string) []Finding {
	switch w[0] {
	case "script", "expect", "unbuffer", "socat", "tmux", "screen", "zpty":
		// These give a program a pseudo-terminal the agent controls, which could answer codeforge trust's prompt.
		return []Finding{{RuleSelf, w[0] + " can drive a terminal prompt such as codeforge trust; run it yourself if you need it"}}
	case "codeforge":
		if len(w) > 1 && (hasAny(w[1:2], "trust", "init", "repair", "reinit") || w[1] == "models" && !printsModels(w[2:]) || (w[1] == "provider" || w[1] == "providers") && !printsProviders(w[2:])) {
			return []Finding{{RuleSelf, "only you can change codeforge settings"}}
		}
		// A subcommand built from a variable (`o=observe; codeforge $o`) can name observe or learn where no literal shows.
		if len(w) > 1 && strings.ContainsAny(w[1], "$`") {
			return []Finding{{RuleLearning, learningReason}}
		}
		if len(w) > 1 && (w[1] == "observe" || w[1] == "learn" && !readsLearning(w[2:])) {
			return []Finding{{RuleLearning, "only you can record or revert codeforge learning evidence"}}
		}
	case "gh":
		return classifyGH(w)
	}
	return nil
}

// merge folds a nested command's result into r.
func (r *Result) merge(sub Result) {
	r.Blocked = append(r.Blocked, sub.Blocked...)
	r.commands += sub.commands
}
