package guard

import (
	"slices"
	"strings"
)

// HelpRequested reports a standalone -h or --help before any "--". It must stay identical to src.WantsHelp, which Run
// uses to answer such a call with usage text before it touches config or state.
func HelpRequested(args []string) bool {
	if len(args) > 0 && args[0] == "gh-shim" {
		return false // everything after gh-shim is gh's own argument list
	}
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "-h", "--help":
			return true
		}
	}
	return false
}

// expandsArgs reports an argument the shell would expand (a variable, a command substitution, a glob, a brace or a tilde; a
// glob can match a file named "--", and ~- and ~+ expand to $OLDPWD and $PWD). The guard sees the text before expansion, so `s=--; codeforge models $s --help` looks like
// help but reaches codeforge as `models -- --help`. Such a call is never treated as help or as read-only.
func expandsArgs(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool { return strings.ContainsAny(a, "$\x60*?[{") || strings.HasPrefix(a, "~") })
}

// ReadOnlyCodeforge reports whether `codeforge <args>` only prints (see printsOnlyCodeforge). Run uses it to skip the
// automatic repair for such a call, so what review mode lets through really writes nothing.
func ReadOnlyCodeforge(args []string) bool {
	return printsOnlyCodeforge(append([]string{"codeforge"}, args...))
}

// printsOnlyCodeforge matches the codeforge calls review mode may run: help on any command, the version, the doctor report
// and the forms of models and provider that only print. Everything else can change state, and so can anything unknown.
func printsOnlyCodeforge(w []string) bool {
	args := w[1:]
	if expandsArgs(args) {
		return false // the shell will replace part of the call with text the guard cannot read
	}
	if HelpRequested(args) {
		return true
	}
	if len(args) == 0 {
		return false // a bare codeforge opens the mode picker
	}
	switch args[0] {
	case "help", "version", "--version":
		return true
	case "doctor":
		return len(args) == 1
	case "models":
		return printsModels(args[1:])
	case "provider", "providers":
		return printsProviders(args[1:])
	}
	return false
}

// printsModels matches the forms of `codeforge models` that only print the roles: none, or recommend.
func printsModels(args []string) bool {
	return len(args) == 0 || args[0] == "recommend"
}

// printsProviders matches `codeforge provider` and `provider show`/`list`, which only print the order.
func printsProviders(args []string) bool {
	return len(args) == 0 || len(args) == 1 && (args[0] == "show" || args[0] == "list")
}

// readsLearning matches the read-only `codeforge learn` forms: list, propose, habits, proposals and rules. Anything else, unknown forms included, may write
// evidence or run a command through codeforge, so it is refused.
func readsLearning(args []string) bool {
	return len(args) > 0 && (args[0] == "list" || args[0] == "propose" || args[0] == "habits" || args[0] == "proposals" || args[0] == "rules")
}

// readOnlyCommands is an allowlist: in review mode anything not listed here is treated as able to change files.
var readOnlyCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "less": true, "wc": true, "file": true, "stat": true,
	"grep": true, "rg": true, "ag": true, "find": true, "fd": true, "tree": true, "pwd": true, "echo": true,
	"printf": true, "diff": true, "cmp": true, "sort": true, "uniq": true, "cut": true, "tr": true, "jq": true,
	"which": true, "type": true, "date": true, "env": true, "true": true, "false": true, "test": true,
	"basename": true, "dirname": true, "realpath": true, "readlink": true, "sha256sum": true, "shasum": true,
	"md5": true, "column": true, "nl": true, "fold": true, "cd": true, "go": true,
	"gofmt": true, "gh": true, "git": true,
}

var readOnlyGit = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "blame": true, "grep": true, "ls-files": true,
	"rev-parse": true, "merge-base": true, "describe": true, "shortlog": true, "cat-file": true,
	"ls-tree": true, "for-each-ref": true, "symbolic-ref": true, "name-rev": true, "branch": true, "remote": true,
}

// readOnlyArgs narrows commands that are read-only only in some forms.
func readOnlyArgs(w, raw []string) (string, bool) {
	name := w[0]
	switch {
	case name == "go" && w[1] == "env" && slices.ContainsFunc(w, func(a string) bool { return a == "-w" || a == "-u" }):
		return "go env -w and -u change persistent Go settings", false
	case name == "go" && slices.ContainsFunc(w, func(a string) bool {
		return strings.HasPrefix(a, "-vettool") || strings.HasPrefix(a, "-exec") || strings.HasPrefix(a, "-toolexec") ||
			strings.HasPrefix(a, "-overlay") || strings.HasPrefix(a, "-modfile") || a == "-o" || strings.HasPrefix(a, "-o=")
	}):
		return "go option runs a custom program or writes files", false
	case name == "jq" && slices.ContainsFunc(w, func(a string) bool { return a == "-L" || strings.HasPrefix(a, "--library") }):
		return "jq -L loads modules from disk", false
	case name == "git" && slices.ContainsFunc(gitGlobals(raw), func(a string) bool { return strings.HasPrefix(a, "-c") || strings.HasPrefix(a, "--config-env") }):
		return "git -c can set a pager or diff driver that runs commands", false
	case name == "git" && w[1] == "remote" && len(w) > minGitTokens && !hasAny(w[2:3], "-v", "--verbose", "show", "get-url"):
		return "git remote " + w[2] + " changes the repository", false
	case name == "git" && w[1] == "symbolic-ref" && (len(nonFlags(w[2:])) > 1 || hasAny(w[2:], "-d", "--delete") || hasShortFlag(w[2:], 'd')):
		return "git symbolic-ref with a target or --delete changes a ref", false
	case name == "git" && slices.ContainsFunc(w, func(a string) bool {
		return strings.HasPrefix(a, "--output") || a == "-O" || strings.HasPrefix(a, "--open-files-in-pager") || a == "--ext-diff"
	}):
		return "git option writes a file or runs a command", false
	case (name == "sort" || name == "diff") && slices.ContainsFunc(w, func(a string) bool {
		return a == "-o" || strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "-o") && name == "sort"
	}):
		return name + " -o writes a file", false
	}
	return "", true
}

func nonFlags(w []string) []string {
	var out []string
	for _, a := range w {
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

// Test runners and build scripts execute repository code, which can write anywhere, so review mode does not run them.
var readOnlySubcommands = map[string][]string{
	"go": {"vet", "list", "doc", "version", "env"},
	"gh": {"pr", "issue", "repo", "run", "api", "search", "release"},
}

// notReadOnly decides with an allowlist, because a denylist misses every unknown way to write a file.
// raw is the redirect-free command; fileWrite reports an output redirection to a real file.
func notReadOnly(w, raw []string, fileWrite bool) (Finding, bool) {
	name := w[0]
	switch {
	case name == "codeforge":
		if fileWrite {
			return Finding{RuleReadOnly, "redirects output into a file"}, true
		}
		if !printsOnlyCodeforge(w) {
			return Finding{RuleReadOnly, "codeforge " + arg(w, 1) + " can change state; only help, version, doctor and the printing forms of models and provider are read-only"}, true
		}
		return Finding{}, false
	case !readOnlyCommands[name]:
		return Finding{RuleReadOnly, name + " is not on the read-only allowlist"}, true
	case fileWrite:
		return Finding{RuleReadOnly, "redirects output into a file"}, true
	case name == "gh" && len(raw) > 0 && raw[0] == "gh" && prComment(w):
		return Finding{RuleComment, "posts a comment on a pull request"}, true
	case name == "find" && hasAny(w, "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls", "-fprint0"):
		return Finding{RuleReadOnly, "find can delete files or run commands"}, true
	case name == "git" && (len(w) < minGitTokens || !readOnlyGit[w[1]]):
		return Finding{RuleReadOnly, "git " + arg(w, 1) + " can change the repository"}, true
	case name == "git" && w[1] == "branch" && len(w) > minGitTokens && !hasAny(w, "--list", "-a", "-r", "-v", "-vv", "--show-current", "--contains"):
		return Finding{RuleReadOnly, "git branch with arguments changes the repository"}, true
	case name == "gh" && (len(w) < minGitTokens || !slices.Contains(readOnlySubcommands[name], w[1]) || ghWrites(w)):
		return Finding{RuleReadOnly, "gh " + arg(w, 1) + " can change GitHub"}, true
	case readOnlySubcommands[name] != nil && name != "gh" && (len(w) < minGitTokens || !slices.Contains(readOnlySubcommands[name], w[1])):
		return Finding{RuleReadOnly, name + " " + arg(w, 1) + " can change files"}, true
	case name == "gofmt" && (hasShortFlag(w, 'w') || has(w, "-w")):
		return Finding{RuleReadOnly, "gofmt -w writes files"}, true
	}
	if why, ok := readOnlyArgs(w, raw); !ok {
		return Finding{RuleReadOnly, why}, true
	}
	return Finding{}, false
}
