package guard

import (
	"os"
	"regexp"
	"slices"
	"strings"
)

// Rule names double as the argument to `codeforge allow`.
const (
	RuleForcePush    = "force-push"
	RuleNoVerify     = "no-verify"
	RuleMerge        = "merge"
	RulePRComment    = "pr-comment"
	RuleInfra        = "infra"
	RuleWholeTree    = "whole-tree"
	RuleDeleteBranch = "delete-branch"
	RuleReadOnly     = "read-only"
	RuleSelf         = "codeforge-config"
	RuleOpaque       = "opaque"
	RuleMixedPush    = "mixed-push"
	RulePushScope    = "push-scope"
	RuleScript       = "script"
)

// Gated actions are allowed once the review gate has passed for the current work.
const (
	GatePush     = "push"
	GateOpenPR   = "open-pr"
	gateNone     = ""
	minGitTokens = 2
)

type Finding struct {
	Rule   string
	Reason string
}

type Result struct {
	Blocked []Finding
	Gate    string
	// Dirs are the directories the guarded actions run in, from `cd` and `git -C`, relative to the tool's cwd.
	Dirs []string
	// PushRefs are the refspecs and ref-wide flags (--all, --tags) a push names.
	PushRefs []string
	// OtherWrites is set when the command does more than read and push.
	OtherWrites bool
	// Deferred holds rules a later boundary (git's pre-push hook, the gh shim) enforces and spends.
	Deferred map[string]bool
	// DirectGH is set when gh is called by path, which skips the shim on PATH.
	DirectGH bool

	gitEnv bool
	cwd    string
}

// Check classifies a shell command line. readOnly adds the rules for review mode.
func Check(command string, readOnly bool) Result {
	return checkIn(command, readOnly, "")
}

// checkIn is Check with the tool's working directory, so script files named in the command can be read.
func checkIn(command string, readOnly bool, cwd string) Result {
	r := Result{cwd: cwd}
	dir := ""
	for _, seg := range segments(command) {
		raw := shellWords(seg)
		if len(raw) > 0 && raw[0] == "export" && slices.ContainsFunc(raw[1:], gitEnv) {
			r.gitEnv = true
			continue
		}

		_, opaque := unwrap(raw)
		words := commandWords(seg)
		if len(words) == 0 {
			continue
		}

		if opaque {
			if readOnly {
				r.Blocked = append(r.Blocked, Finding{RuleReadOnly, "the command is wrapped in a way the read-only check cannot inspect"})
				continue
			}
			if guardedTool.MatchString(seg) {
				r.Blocked = append(r.Blocked, Finding{RuleOpaque, "the command runs git, gh, terraform or kubectl through a wrapper, another directory or a git environment override the guard cannot follow"})
				continue
			}
		}

		if words[0] == "git" && enforcementOverride(raw) {
			r.Blocked = append(r.Blocked, Finding{RuleSelf, "git -c or --config-env sets hooks, hooksPath or aliases, which could switch off codeforge's push check"})
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
			r.merge(checkIn(inner, readOnly, cwd), dir)
			continue
		}

		// A script file runs commands the hook never sees, so it must not contain guarded tools.
		if path, ok := scriptPath(unwrapped(raw)); ok && !readOnly {
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
			if guardedTool.MatchString(inner) {
				r.Blocked = append(r.Blocked, Finding{RuleOpaque, words[0] + " code calls git, gh, terraform or kubectl, which the guard cannot inspect inside an interpreter"})
			}
			continue
		}

		if f, writes := notReadOnly(words, raw); writes {
			if readOnly {
				r.Blocked = append(r.Blocked, f)
				continue
			}
			r.OtherWrites = r.OtherWrites || !isPublish(words)
		}

		found, gate := classify(words)
		deferTo(&r, words, raw, found, gate)
		if gate == GatePush {
			r.PushRefs = append(r.PushRefs, pushRefs(words)...)
			if gitConfigOverride(raw) {
				found = append(found, Finding{RulePushScope, "the push overrides git config, which can change what it publishes"})
			}
		}
		if len(found) == 0 && gate == gateNone {
			continue
		}
		r.Blocked = append(r.Blocked, found...)
		r.Gate = cmpGate(r.Gate, gate)
		r.Dirs = append(r.Dirs, joinDir(dir, gitDir(raw)))
	}

	if r.gitEnv && (r.Gate != gateNone || len(r.Blocked) > 0 || strings.Contains(command, "git")) {
		r.Blocked = append(r.Blocked, Finding{RuleOpaque, "an exported git environment variable points git at another repository or config"})
	}
	return r
}

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

// enforcementOverride reports one-off git config that could replace or skip the pre-push hook.
func enforcementOverride(raw []string) bool {
	g := gitGlobals(raw)
	for i, a := range g {
		val := ""
		switch {
		case a == "-c" && i+1 < len(g):
			val = g[i+1]
		case strings.HasPrefix(a, "-c") && len(a) > len("-c"):
			val = a[len("-c"):]
		case strings.HasPrefix(a, "--config-env"):
			return true
		default:
			continue
		}
		k := strings.ToLower(val)
		if strings.HasPrefix(k, "hook.") || strings.HasPrefix(k, "core.hookspath") || strings.HasPrefix(k, "alias.") {
			return true
		}
	}
	return false
}

// gitConfigOverride spots -c and --config-env, which can define aliases, pagers and push behaviour.
func gitConfigOverride(raw []string) bool {
	return slices.ContainsFunc(gitGlobals(raw), func(a string) bool {
		return strings.HasPrefix(a, "-c") || a == "--config-env" || strings.HasPrefix(a, "--config-env=")
	})
}

// knownGit lists git's own subcommands. Anything else is an alias or an external git-<name> program,
// which can expand into any command, so the guard refuses to guess.
var knownGit = set(
	"add", "am", "annotate", "apply", "archive", "bisect", "blame", "branch", "bundle", "cat-file", "check-attr",
	"check-ignore", "check-mailmap", "check-ref-format", "checkout", "cherry", "cherry-pick", "citool", "clean",
	"clone", "commit", "commit-graph", "commit-tree", "config", "count-objects", "describe", "diff", "diff-files",
	"diff-index", "diff-tree", "difftool", "fetch", "for-each-ref", "format-patch", "fsck", "gc", "grep",
	"hash-object", "help", "init", "log", "ls-files", "ls-remote", "ls-tree", "maintenance", "merge", "merge-base",
	"merge-file", "merge-tree", "mergetool", "mv", "name-rev", "notes", "pull", "push", "range-diff", "rebase",
	"reflog", "remote", "repack", "replace", "rerere", "reset", "restore", "rev-list", "rev-parse", "revert", "rm",
	"shortlog", "show", "show-branch", "show-ref", "sparse-checkout", "stash", "status", "submodule",
	"switch", "symbolic-ref", "tag", "update-index", "update-ref", "var", "verify-commit", "verify-tag",
	"version", "whatchanged", "worktree", "write-tree",
)

// isPublish matches the gated actions themselves, which are checked separately from other writes in the same command.
func isPublish(w []string) bool {
	_, gate := classify(w)
	return gate != gateNone
}

var pushValueFlags = map[string]bool{"--repo": true, "--receive-pack": true, "--exec": true, "-o": true, "--push-option": true}

// pushRefs returns what a push publishes besides the remote name: refspecs, and --all, --tags or --branches.
func pushRefs(w []string) []string {
	var out, pos []string
	for i := 2; i < len(w); i++ {
		a := w[i]
		switch {
		case pushValueFlags[a]:
			i++
		case a == "--all" || a == "--tags" || a == "--branches" || a == "--follow-tags":
			out = append(out, a)
		case strings.HasPrefix(a, "-"):
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) > 1 {
		out = append(out, pos[1:]...)
	}
	return out
}

// joinDir resolves next against base; an absolute next replaces base.
func joinDir(base, next string) string {
	switch {
	case next == "":
		return base
	case strings.HasPrefix(next, "/") || strings.HasPrefix(next, "~") || strings.HasPrefix(next, "$"):
		return next
	case base == "":
		return next
	}
	return base + "/" + next
}

func arg(w []string, i int) string {
	if i < len(w) {
		return w[i]
	}
	return "~"
}

// gitDir returns the -C directory of a git command, if any.
func gitDir(raw []string) string {
	dir := ""
	for i := 0; i+1 < len(raw); i++ {
		base := raw[i][strings.LastIndex(raw[i], "/")+1:]
		if base != "git" {
			continue
		}
		for j := i + 1; j+1 < len(raw) && strings.HasPrefix(raw[j], "-"); j++ {
			if raw[j] == "-C" {
				dir = joinDir(dir, raw[j+1])
				j++
			}
		}
		return dir
	}
	return dir
}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true}

func classify(w []string) ([]Finding, string) {
	switch w[0] {
	case "codeforge":
		if len(w) > 1 && hasAny(w[1:2], "allow", "trust", "init", "models") {
			return []Finding{{RuleSelf, "only you can approve actions or change codeforge settings"}}, gateNone
		}
	case "git":
		return classifyGit(w)
	case "gh":
		return classifyGH(w)
	case "terraform", "tofu", "terragrunt":
		if hasAny(w, "apply", "destroy", "import") || (has(w, "state") && hasAny(w, "rm", "mv", "push")) {
			return []Finding{{RuleInfra, "changes live infrastructure"}}, gateNone
		}
	case "kubectl":
		if hasAny(w, "apply", "delete", "edit", "patch", "replace", "scale") || (has(w, "rollout") && hasAny(w, "undo", "restart", "pause", "resume")) {
			return []Finding{{RuleInfra, "changes a live cluster"}}, gateNone
		}
	}
	return nil, gateNone
}

func classifyGit(w []string) ([]Finding, string) {
	if len(w) < minGitTokens {
		return nil, gateNone
	}

	var out []Finding
	sub, args := w[1], w[2:]
	if !knownGit[sub] {
		return []Finding{{RuleOpaque, "git " + sub + " is an alias or external command the guard cannot inspect; run the underlying git command instead"}}, gateNone
	}
	if has(args, "--no-verify") || (sub == "commit" && hasShortFlag(args, 'n')) {
		out = append(out, Finding{RuleNoVerify, "skips the repository's hooks"})
	}

	if sub == "config" && slices.ContainsFunc(args, func(a string) bool {
		k := strings.ToLower(a)
		return strings.HasPrefix(k, "hook.") || strings.HasPrefix(k, "core.hookspath") || strings.HasPrefix(k, "alias.") ||
			k == "--unset" || k == "--unset-all" || k == "--remove-section" || k == "--rename-section" || k == "--edit" || k == "-e"
	}) {
		return []Finding{{RuleSelf, "changes git hook, alias or config sections that codeforge's push check relies on"}}, gateNone
	}

	switch sub {
	case "push":
		if has(args, "--delete") || hasShortFlag(args, 'd') || slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, ":") }) {
			return append(out, Finding{RuleDeleteBranch, "deletes a remote branch"}), gateNone
		}
		if hasAny(args, "--force", "--mirror") || hasShortFlag(args, 'f') || slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "+") }) {
			return append(out, Finding{RuleForcePush, "force push without --force-with-lease"}), gateNone
		}
		return out, GatePush
	case "stash":
		if len(args) == 0 || !hasAny(args[:1], "list", "show") {
			out = append(out, Finding{RuleWholeTree, "git stash moves every uncommitted change in the checkout"})
		}
	case "reset":
		if has(args, "--hard") {
			out = append(out, Finding{RuleWholeTree, "git reset --hard discards uncommitted work"})
		}
	case "clean":
		if hasShortFlag(args, 'f') || has(args, "--force") {
			out = append(out, Finding{RuleWholeTree, "git clean deletes untracked files"})
		}
	case "checkout", "restore":
		if has(args, ".") || has(args, ":/") {
			out = append(out, Finding{RuleWholeTree, "discards changes across the whole tree"})
		}
	case "add":
		if hasAny(args, "-A", "--all", ".", ":/") {
			out = append(out, Finding{RuleWholeTree, "stages the whole tree; stage explicit paths"})
		}
	case "commit":
		if hasShortFlag(args, 'a') || has(args, "--all") {
			out = append(out, Finding{RuleWholeTree, "commits the whole tree; stage explicit paths"})
		}
	case "branch":
		if hasShortFlag(args, 'd') || hasShortFlag(args, 'D') || has(args, "--delete") {
			out = append(out, Finding{RuleDeleteBranch, "deletes a branch"})
		}
	}
	return out, gateNone
}

// ghGlobalRepo removes gh's -R/--repo wherever it appears and reports whether it was there.
func ghGlobalRepo(w []string) ([]string, bool) {
	out := []string{w[0]}
	found := false
	for i := 1; i < len(w); i++ {
		name, _, hasValue := strings.Cut(w[i], "=")
		attached := strings.HasPrefix(w[i], "-R") && len(w[i]) > len("-R")
		if name == "-R" || name == "--repo" || attached {
			found = true
			if !hasValue && !attached {
				i++
			}
			continue
		}
		out = append(out, w[i])
	}
	return out, found
}

// knownGH lists gh's built-in commands. Anything else may be a user alias, which gh expands into any
// command (including `pr merge`), so it is refused rather than guessed.
var knownGH = set(
	"alias", "api", "attestation", "auth", "browse", "cache", "co", "codespace", "completion", "config",
	"extension", "gist", "gpg-key", "help", "issue", "label", "org", "pr", "project", "release", "repo",
	"ruleset", "run", "search", "secret", "ssh-key", "status", "variable", "workflow", "version",
	"--version", "--help", "-h",
)

func classifyGH(w []string) ([]Finding, string) {
	w, otherRepo := ghGlobalRepo(w)
	if len(w) >= minGitTokens && !knownGH[w[1]] {
		return []Finding{{RuleOpaque, "gh " + w[1] + " is an alias or extension the guard cannot inspect; run the underlying gh command instead"}}, gateNone
	}
	if len(w) >= minGitTokens && w[1] == "alias" && len(w) > 2 && w[2] != "list" {
		return []Finding{{RuleSelf, "changes gh aliases, which could hide commands from codeforge"}}, gateNone
	}
	if len(w) < 3 || w[1] != "pr" {
		if len(w) >= 2 && w[1] == "api" && graphQLRead(w[2:]) {
			return nil, gateNone
		}
		if len(w) >= 2 && w[1] == "api" && apiWrites(w[2:]) {
			if otherRepo || !apiTargetsThisRepo(w[2:]) {
				return []Finding{{RulePushScope, "gh api writes must use the {owner}/{repo} placeholders so they stay in this repository"}}, gateNone
			}
			return []Finding{{RulePRComment, "writes to GitHub through the API"}}, gateNone
		}
		return nil, gateNone
	}

	crossRepo := otherRepo || slices.ContainsFunc(w[3:], func(a string) bool { return strings.Contains(a, "github.com/") })
	switch w[2] {
	case "merge", "comment", "review", "close", "edit":
		if crossRepo {
			return []Finding{{RulePushScope, "the command targets another repository than this one, where approvals do not apply"}}, gateNone
		}
		if w[2] == "merge" {
			return []Finding{{RuleMerge, "merges a PR"}}, gateNone
		}
		return []Finding{{RulePRComment, "posts or changes a PR"}}, gateNone
	case "create", "ready":
		if otherRepo || slices.ContainsFunc(w[3:], func(a string) bool {
			name, _, _ := strings.Cut(a, "=")
			return name == "--head" || name == "-H" || name == "--repo" || name == "-R" ||
				len(a) > 2 && (strings.HasPrefix(a, "-H") || strings.HasPrefix(a, "-R"))
		}) {
			return []Finding{{RulePushScope, "the PR names another head branch or repository than the one the gate reviewed"}}, GateOpenPR
		}
		return nil, GateOpenPR
	}
	return nil, gateNone
}

// knownAPIFlags are the gh api flags graphQLRead understands; anything else makes the call opaque.
var knownAPIFlags = set("-f", "-F", "--field", "--raw-field", "-X", "--method", "--input",
	"-H", "--header", "-q", "--jq", "-t", "--template", "--hostname", "--cache", "--paginate", "--slurp", "-i", "--include", "--silent", "--verbose")

var apiValueFlags = set("-X", "--method", "-f", "-F", "--field", "--raw-field", "-H", "--header", "--input", "-q", "--jq", "-t", "--template", "--hostname", "--cache")

// apiTargetsThisRepo accepts only endpoints under repos/{owner}/{repo}, which gh fills in from the current repository.
func apiTargetsThisRepo(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if apiValueFlags[a] {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		a = strings.TrimPrefix(a, "/")
		return strings.HasPrefix(a, "repos/{owner}/{repo}/")
	}
	return false
}

// graphQLRead accepts `gh api graphql` whose only fields are a query and variables, with no mutation.
func graphQLRead(args []string) bool {
	endpoint, query := "", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, attached := strings.Cut(a, "=")
		switch {
		case name == "-f" || name == "-F" || name == "--field" || name == "--raw-field":
			field := value
			if !attached && i+1 < len(args) {
				i++
				field = args[i]
			}
			if k, v, ok := strings.Cut(field, "="); ok && k == "query" {
				// -F and --field expand {owner}/{repo} and @file, so the query that runs is not the text here.
				if name == "-F" || name == "--field" {
					return false
				}
				query = v
			}
		case len(a) > 2 && (strings.HasPrefix(a, "-f") || strings.HasPrefix(a, "-F")) && a[1] != '-':
			if k, v, ok := strings.Cut(a[2:], "="); ok && k == "query" {
				if a[1] == 'F' {
					return false
				}
				query = v
			}
		case name == "-X" || name == "--method" || strings.HasPrefix(a, "-X") || name == "--input":
			return false
		case apiValueFlags[name] && !attached:
			i++
		case !strings.HasPrefix(a, "-") && endpoint == "":
			endpoint = a
		case strings.HasPrefix(a, "-") && !knownAPIFlags[name]:
			// Clustered or unknown flags can take the next word as a value, moving the real endpoint.
			return false
		}
	}

	q := strings.ToLower(strings.TrimSpace(query))
	return endpoint == "graphql" && q != "" &&
		!strings.Contains(q, "mutation") && !strings.Contains(q, "subscription") &&
		(strings.HasPrefix(q, "query") || strings.HasPrefix(q, "{"))
}

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

// guardedTool spots guarded tools named anywhere in interpreter code, however the call is spelled.
var guardedTool = regexp.MustCompile(`\b(git|gh|terraform|tofu|terragrunt|kubectl|codeforge)\b`)

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
	case name == "git" && gitConfigOverride(raw):
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
func notReadOnly(w, raw []string) (Finding, bool) {
	name := w[0]
	switch {
	case !readOnlyCommands[name]:
		return Finding{RuleReadOnly, name + " is not on the read-only allowlist"}, true
	case redirectsToFile(raw):
		return Finding{RuleReadOnly, "redirects output into a file"}, true
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

func ghWrites(w []string) bool {
	if w[1] == "api" {
		return apiWrites(w[2:])
	}
	return len(w) > 2 && !hasAny(w[2:3], "view", "list", "diff", "checks", "status")
}

// redirectsToFile works on shell words, so `echo "a > b"` is text while `2 >file` and `>file` are writes.
func redirectsToFile(raw []string) bool {
	for i, a := range raw {
		op := strings.TrimLeft(a, "0123456789&")
		if !strings.HasPrefix(op, ">") {
			continue
		}

		target := strings.TrimLeft(op, ">|&")
		if target == "" && i+1 < len(raw) {
			target = raw[i+1]
		}
		if target != "" && target != "/dev/null" && !isFD(target) {
			return true
		}
	}
	return false
}

func isFD(s string) bool {
	s = strings.TrimPrefix(s, "&")
	return s != "" && strings.Trim(s, "0123456789-") == ""
}

var interpreters = map[string]string{
	"python": "-c", "python3": "-c", "node": "-e", "deno": "eval", "ruby": "-e", "perl": "-e",
	"php": "-r", "osascript": "-e", "lua": "-e",
}

// interpreterCode returns inline code passed to an interpreter, which can run any command.
func interpreterCode(w []string) (string, bool) {
	flag, ok := interpreters[w[0]]
	if !ok {
		return "", false
	}
	for i, a := range w[1:] {
		if (a == flag || strings.HasPrefix(a, "-") && strings.HasSuffix(a, strings.TrimPrefix(flag, "-")) && len(flag) == 2) && i+2 < len(w) {
			return w[i+2], true
		}
	}
	return strings.Join(w[1:], " "), true
}

// shellScript unwraps `sh -c "..."` so a nested shell cannot hide a command.
func shellScript(w []string) (string, bool) {
	if !shells[w[0]] {
		return "", false
	}
	for i, a := range w[1:] {
		isC := a == "-c" || (len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.HasSuffix(a, "c"))
		if isC && i+2 < len(w) {
			return w[i+2], true
		}
	}
	return "", false
}

func cmpGate(have, next string) string {
	if have != gateNone {
		return have
	}
	return next
}

// segments splits on shell separators so `cd x && git push` is seen as two commands.
// Separators inside quotes are kept, so quoted code reaches its interpreter whole;
// command substitution is split out even inside double quotes, because the shell runs it.
func segments(command string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	flush := func() {
		out = append(out, cur.String())
		cur.Reset()
	}

	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == '\'' && c != '\'':
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			switch quote {
			case 0:
				quote = c
			case c:
				quote = 0
			}
			cur.WriteByte(c)
		case quote != '\'' && (c == '`' || c == '$' && i+1 < len(command) && command[i+1] == '('):
			flush()
			if c == '$' {
				i++
			}
		case quote == 0 && c == '&' && (i > 0 && (command[i-1] == '>' || command[i-1] == '<') || i+1 < len(command) && command[i+1] == '>'):
			cur.WriteByte(c)
		case quote == 0 && (c == ';' || c == '|' || c == '&' || c == '\n'):
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// commandWords drops env assignments, wrappers such as sudo or env with their options, and git's global options.
func commandWords(seg string) []string {
	words, _ := unwrap(shellWords(seg))
	if len(words) == 0 {
		return nil
	}

	words[0] = words[0][strings.LastIndex(words[0], "/")+1:]
	if words[0] != "git" {
		return words
	}

	out := []string{"git"}
	rest := words[1:]
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		if gitValueOptions[rest[0]] {
			rest = rest[min(minGitTokens, len(rest)):]
			continue
		}
		rest = rest[1:]
	}
	return append(out, dropMessages(rest)...)
}

// gitValueOptions are git global options whose value is the next word.
var gitValueOptions = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--config-env": true, "--super-prefix": true, "--exec-path": true,
}

type wrapper struct {
	valueFlags map[string]bool
	chdirFlags map[string]bool
	positional int
	opaque     bool
}

// wrappers run another command. chdir flags move it to another directory, and xargs or env -S
// build the command at run time, so those forms are opaque to the guard.
var wrappers = map[string]wrapper{
	"sudo":       {valueFlags: set("-u", "-g", "-h", "-p", "-U", "-r", "-t", "-C", "-T"), chdirFlags: set("-D", "--chdir")},
	"doas":       {valueFlags: set("-u", "-C")},
	"env":        {valueFlags: set("-u", "--unset"), chdirFlags: set("-C", "--chdir")},
	"command":    {},
	"exec":       {valueFlags: set("-a")},
	"nice":       {valueFlags: set("-n", "--adjustment")},
	"nohup":      {},
	"time":       {valueFlags: set("-f", "-o")},
	"timeout":    {valueFlags: set("-s", "-k", "--signal", "--kill-after"), positional: 1},
	"stdbuf":     {valueFlags: set("-i", "-o", "-e")},
	"caffeinate": {valueFlags: set("-t", "-w")},
	"xargs":      {opaque: true},
}

// unwrap strips leading assignments and wrapper commands. It reports opaque when the real command or its
// directory cannot be known statically; callers then fail closed.
func unwrap(words []string) ([]string, bool) {
	opaque := false
	for len(words) > 0 {
		head := words[0][strings.LastIndex(words[0], "/")+1:]
		if isAssignment(words[0]) {
			opaque = opaque || gitEnv(words[0])
			words = words[1:]
			continue
		}

		w, ok := wrappers[head]
		if !ok {
			break
		}
		opaque = opaque || w.opaque
		words = words[1:]

		var more bool
		words, opaque, more = wrapperArgs(head, w, words, opaque)
		if !more {
			return words, true
		}
	}
	if len(words) > 0 && strings.HasPrefix(words[0], "-") {
		opaque = true
	}
	return words, opaque
}

// wrapperArgs skips a wrapper's own options. more is false when the wrapper builds the command at run time.
func wrapperArgs(head string, w wrapper, words []string, opaque bool) (rest []string, isOpaque, more bool) {
	skipped := 0
	for len(words) > 0 {
		a := words[0]
		name, _, hasValue := strings.Cut(a, "=")
		switch {
		case a == "--":
			return words[1:], opaque, true
		case head == "env" && (strings.HasPrefix(a, "-S") || strings.HasPrefix(a, "--split-string")):
			return words, true, false
		case w.chdirFlags[name]:
			opaque = true
			if hasValue {
				words = words[1:]
			} else {
				words = words[min(len(words), minGitTokens):]
			}
		case w.valueFlags[a]:
			words = words[min(len(words), minGitTokens):]
		case strings.HasPrefix(a, "-"):
			words = words[1:]
		case skipped < w.positional:
			skipped++
			words = words[1:]
		default:
			return words, opaque, true
		}
	}
	return words, opaque, true
}

// unwrapped is the command with assignments and wrappers removed, keeping the executable's path.
func unwrapped(raw []string) []string {
	w, _ := unwrap(raw)
	return w
}

func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	return ok && name != "" && !strings.ContainsAny(name, "-/.$")
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

// shellWords splits like a POSIX shell for the cases that matter here: quotes group words, backslashes escape.
func shellWords(s string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote byte

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote, inWord = c, true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inWord = true
		case c == ' ' || c == '\t' || c == '(' || c == ')':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

func has(w []string, s string) bool {
	return slices.Contains(w, s)
}

func hasAny(w []string, ss ...string) bool {
	return slices.ContainsFunc(w, func(a string) bool { return slices.Contains(ss, a) })
}

// hasShortFlag matches -f, and combined short flags such as -fu.
func hasShortFlag(w []string, c byte) bool {
	return slices.ContainsFunc(w, func(a string) bool {
		return len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a[1:], c) >= 0
	})
}

var scriptRunners = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "source": true, ".": true}

// scriptPath returns the script a command executes: ./x.sh, bin/x, or `bash x.sh`.
func scriptPath(w []string) (string, bool) {
	if scriptRunners[w[0]] {
		for _, a := range w[1:] {
			if !strings.HasPrefix(a, "-") {
				return a, true
			}
		}
		return "", false
	}
	base := w[0][strings.LastIndex(w[0], "/")+1:]
	if knownBinary(base) {
		return "", false
	}
	if strings.Contains(w[0], "/") || strings.HasSuffix(w[0], ".sh") {
		return w[0], true
	}
	return "", false
}

// knownBinary is a command the guard already classifies by name, wherever it is installed.
func knownBinary(name string) bool {
	switch name {
	case "git", "gh", "terraform", "tofu", "terragrunt", "kubectl", "codeforge":
		return true
	}
	return readOnlyCommands[name] || shells[name] || interpreters[name] != ""
}

const maxScriptBytes = 256 << 10

// scriptCallsGuarded reads a local script and refuses it when it names a guarded tool or runs another script.
// Scripts the guard cannot read, such as binaries or missing files, are refused too.
func scriptCallsGuarded(path, cwd string) (Finding, bool) {
	p := path
	if !strings.HasPrefix(p, "/") {
		p = cwd + "/" + p
	}

	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxScriptBytes {
		return Finding{RuleScript, path + " cannot be inspected, so it might run guarded commands; run the commands directly instead"}, true
	}

	data, err := os.ReadFile(p)
	if err != nil || slices.Contains(data, 0) {
		return Finding{RuleScript, path + " cannot be inspected, so it might run guarded commands; run the commands directly instead"}, true
	}

	if guardedTool.Match(data) {
		return Finding{RuleScript, path + " runs git, gh, terraform, kubectl or codeforge, which the guard cannot check inside a script; run those commands directly"}, true
	}
	return Finding{}, false
}

// merge folds a nested command's result into r, resolving its directories against dir.
func (r *Result) merge(sub Result, dir string) {
	r.Blocked = append(r.Blocked, sub.Blocked...)
	r.Gate = cmpGate(r.Gate, sub.Gate)
	r.PushRefs = append(r.PushRefs, sub.PushRefs...)
	r.OtherWrites = r.OtherWrites || sub.OtherWrites
	r.DirectGH = r.DirectGH || sub.DirectGH
	r.gitEnv = r.gitEnv || sub.gitEnv
	for _, d := range sub.Dirs {
		r.Dirs = append(r.Dirs, joinDir(dir, d))
	}
	for rule := range sub.Deferred {
		if r.Deferred == nil {
			r.Deferred = map[string]bool{}
		}
		r.Deferred[rule] = true
	}
}

// deferTo marks rules that a later boundary will enforce for this command. Remote pushes, including deletions
// and force pushes, are checked by git's pre-push hook; gh calls through PATH are checked by the shim.
func deferTo(r *Result, words, raw []string, found []Finding, gate string) {
	mark := func(rule string) {
		if r.Deferred == nil {
			r.Deferred = map[string]bool{}
		}
		r.Deferred[rule] = true
	}

	switch words[0] {
	case "git":
		if len(words) >= minGitTokens && words[1] == "push" {
			mark(GatePush)
			for _, f := range found {
				if f.Rule == RuleForcePush || f.Rule == RuleDeleteBranch {
					mark(f.Rule)
				}
			}
		}
	case "gh":
		exe := unwrapped(raw)
		if len(exe) > 0 && strings.Contains(exe[0], "/") {
			r.DirectGH = true
			return
		}
		if gate != gateNone {
			mark(gate)
		}
		for _, f := range found {
			mark(f.Rule)
		}
	}
}
