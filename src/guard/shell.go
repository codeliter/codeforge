package guard

import (
	"os"
	"regexp"
	"slices"
	"strings"
)

// withoutRedirects removes unquoted redirections and, when an operator stands alone (`2>& 1`, `> log`),
// the target word after it. Every operator token is followed by its target word.
func withoutRedirects(words []string, redirect []bool) []string {
	out := make([]string, 0, len(words))
	for i := 0; i < len(words); i++ {
		if redirect[i] {
			i++
			continue
		}
		out = append(out, words[i])
	}
	return out
}

// redirectsWellFormed rejects a redirect with no target and an operator used as another operator's target;
// the guard cannot read those, so it refuses them. Process substitution is caught by the tokenizer.
func redirectsWellFormed(words []string, redirect []bool) bool {
	for i := range words {
		if redirect[i] && (i+1 >= len(words) || redirect[i+1]) {
			return false
		}
	}
	return true
}

// writesFile reports an output redirection to a real file. Duplicating or closing a descriptor (>&1, 2>&-)
// and writing to /dev/null are not writes; `>&1abc` is, because its whole target is not a descriptor.
func writesFile(words []string, redirect []bool) bool {
	for i, w := range words {
		op := strings.TrimLeft(w, "0123456789")
		if !redirect[i] || !strings.ContainsRune(op, '>') || i+1 >= len(words) {
			continue
		}

		target := words[i+1]
		if target == "/dev/null" {
			continue
		}
		if strings.HasSuffix(op, "&") && op != "&>" && isDescriptor(target) {
			continue
		}
		return true
	}
	return false
}

// isDescriptor matches a duplication target: a descriptor number, optionally moved with a trailing -, or - to close.
func isDescriptor(s string) bool {
	n := strings.TrimSuffix(s, "-")
	return s == "-" || n != "" && strings.Trim(n, "0123456789") == ""
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

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true}

var commandWrappers = map[string]bool{"env": true, "sudo": true, "command": true, "exec": true, "nohup": true, "time": true, "xargs": true, "nice": true}

// buildsCommand scans for an executable $(...) or backtick substitution (not in single quotes, not escaped) and
// reports one that sits at the start of a simple command, glued to a command name, or after codeforge.
func buildsCommand(command string) bool {
	var quote byte
	start := 0
	check := func(i int) bool {
		prefix := command[start:i]
		var words []string
		assigns := false
		for _, w := range strings.Fields(prefix) {
			switch {
			case isAssignment(w):
				assigns = true
			case commandWrappers[strings.TrimLeft(w, "\"'")]:
			default:
				words = append(words, w)
			}
		}
		glued := i > start && !strings.HasSuffix(prefix, " ") && !strings.HasSuffix(prefix, "\t")
		switch {
		case len(words) == 0:
			return !assigns
		case len(words) == 1 && glued:
			return true
		}
		first := strings.Trim(words[0], "\"'")
		return first[strings.LastIndex(first, "/")+1:] == "codeforge"
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			i++
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote == '"' && c == '"':
			quote = 0
		case quote == 0 && (c == ';' || c == '|' || c == '&' || c == '\n' || c == '('):
			start = i + 1
		case c == '`':
			if check(i) {
				return true
			}
			end := i + 1
			for end < len(command) && command[end] != '`' {
				if command[end] == '\\' {
					end++
				}
				end++
			}
			i = end
			start = min(end+1, len(command))
		case c == '$' && i+1 < len(command) && command[i+1] == '(':
			if check(i) {
				return true
			}
			start = i + len("$(")
			i++
		}
	}
	return false
}

// guardedTool spots guarded tools named anywhere in interpreter code, however the call is spelled.
var guardedTool = regexp.MustCompile(`\b(gh|codeforge)\b`)

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
		case c == '\\' && i+1 < len(command):
			// An escaped backtick, $, quote or separator is literal text, not command substitution or a split.
			cur.WriteByte(c)
			i++
			cur.WriteByte(command[i])
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

// commandWords drops redirections, env assignments, wrappers such as sudo or env with their options,
// and git's global options.
func commandWords(seg string) []string {
	words, _ := unwrap(withoutRedirects(shellTokens(seg)))
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

// shellTokens splits like a POSIX shell for the cases that matter here: quotes group words, backslashes
// escape, and an unquoted redirection (>, >>, <, 2>&1, &>) is its own token even when written against a
// word, as in `'2'>/dev/null`. redirect marks those tokens; a quoted '>topic' stays an ordinary argument.
func shellTokens(s string) (words []string, redirect []bool) {
	var cur strings.Builder
	inWord, quoted := false, false
	// awaitingTarget is set after an operator: the next word is its target, never a descriptor prefix,
	// so in `2>&1>status` the 1 is the first operator's target and status the second's.
	awaitingTarget := false
	var quote byte
	emit := func(isRedir bool) {
		if inWord {
			words = append(words, cur.String())
			redirect = append(redirect, isRedir)
			cur.Reset()
			inWord, quoted = false, false
			awaitingTarget = isRedir
		}
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == '"' && c == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\", s[i+1]) >= 0:
			// Inside double quotes a backslash only escapes $, `, " and \; the shell drops it, so an inner
			// shell (sh -c "echo \$(x)") sees the unescaped text and the guard must inspect that same text.
			i++
			cur.WriteByte(s[i])
		case quote != 0:
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote, inWord, quoted = c, true, true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inWord, quoted = true, true
		case c == ' ' || c == '\t' || c == '(' || c == ')':
			emit(false)
		case c == '>' || c == '<' || c == '&' && i+1 < len(s) && s[i+1] == '>':
			// An unquoted all-digit word right before the operator is its descriptor (2>), unless that word is
			// the previous operator's target; anything else is a separate word the operator ends.
			fd := ""
			if inWord && !quoted && !awaitingTarget && c != '&' && strings.Trim(cur.String(), "0123456789") == "" {
				fd = cur.String()
				cur.Reset()
				inWord = false
			}
			emit(false)

			start := i
			for i < len(s) && strings.IndexByte("<>&|", s[i]) >= 0 {
				i++
			}
			// Process substitution, >(cmd) or <(cmd), runs a command the guard cannot read: emit an operator
			// with no target, which redirectsWellFormed refuses.
			if i < len(s) && s[i] == '(' {
				cur.WriteString(fd + s[start:i])
				inWord = true
				emit(true)
				words = append(words, "(")
				redirect = append(redirect, true)
				i--
				continue
			}
			cur.WriteString(fd + s[start:i])
			i--
			inWord = true
			emit(true)
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	emit(false)
	return words, redirect
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

// knownBinary is a command the guard knows by name, wherever it is installed, so it is never read as a script.
func knownBinary(name string) bool {
	switch name {
	case "git", "gh", "terraform", "tofu", "terragrunt", "kubectl", "codeforge":
		return true
	}
	return readOnlyCommands[name] || shells[name] || interpreters[name] != ""
}

const maxScriptBytes = 256 << 10

// UninspectableScript is said of a script the guard could not read.
const UninspectableScript = "cannot be inspected"

// scriptCallsGuarded reads a local script and refuses it when it names a guarded tool or runs another script.
// Scripts the guard cannot read, such as binaries or missing files, are refused too.
func scriptCallsGuarded(path, cwd string) (Finding, bool) {
	p := path
	if !strings.HasPrefix(p, "/") {
		p = cwd + "/" + p
	}

	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxScriptBytes {
		return Finding{RuleScript, path + " " + UninspectableScript + ", so it might run guarded commands; run the commands directly instead"}, true
	}

	data, err := os.ReadFile(p)
	if err != nil || slices.Contains(data, 0) {
		return Finding{RuleScript, path + " " + UninspectableScript + ", so it might run guarded commands; run the commands directly instead"}, true
	}

	if guardedTool.Match(data) {
		return Finding{RuleScript, path + " runs gh or codeforge, which the guard cannot check inside a script; run those commands directly"}, true
	}
	return Finding{}, false
}
