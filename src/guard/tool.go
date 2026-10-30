package guard

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Input is the PreToolUse payload Empryo sends a hook on stdin.
type Input struct {
	Event string          `json:"hook_event_name"`
	Tool  string          `json:"tool_name"`
	Cwd   string          `json:"cwd"`
	Args  json.RawMessage `json:"tool_input"`
	Paths []string        `json:"tool_paths"`
}

var editTools = []string{
	"Edit", "Write", "MultiEdit", "NotebookEdit",
	"edit_file", "multi_edit", "ast_edit", "structural_edit", "refactor", "undo_edit",
}

var shellTools = []string{"Bash", "shell", "Shell"}

// CheckTool applies the rules to one tool call, whatever tool the agent used to run it.
func CheckTool(in Input, readOnly bool) Result {
	if f := checkProtected(in); len(f) > 0 {
		return Result{Blocked: f}
	}
	if readOnly && slices.Contains(editTools, in.Tool) {
		return Result{Blocked: []Finding{{RuleReadOnly, in.Tool + " edits files"}}}
	}

	var args map[string]any
	if err := json.Unmarshal(in.Args, &args); err != nil {
		return Result{}
	}

	if in.Tool == "settings" && strings.Contains(strings.ToLower(string(in.Args)), "hook") {
		return Result{Blocked: []Finding{{RuleSelf, "changing Empryo's hook settings can switch the guard off"}}}
	}

	switch {
	case slices.Contains(shellTools, in.Tool):
		return checkIn(str(args["command"]), readOnly, in.Cwd)
	case in.Tool == "Git" || in.Tool == "git":
		return Check(gitCommand(args), readOnly)
	case in.Tool == "code_script":
		return checkScript(str(args["code"]), readOnly)
	}

	// File content is data, not a command; only tools that can execute something are scanned.
	if slices.Contains(editTools, in.Tool) || slices.Contains(readTools, in.Tool) {
		return Result{}
	}
	return Check(strings.Join(commandStrings(args), "\n"), readOnly)
}

// gitCommand rebuilds a shell command from Empryo's structured git tool.
func gitCommand(a map[string]any) string {
	action := str(a["action"])
	parts := []string{"git", strings.ReplaceAll(action, "_", "-")}

	switch action {
	case "stage":
		parts[1] = "add"
		if len(strs(a["files"])) == 0 {
			parts = append(parts, "-A")
		}
	case "stash":
		parts = append(parts, str(a["sub_action"]))
	case "reset":
		if m := str(a["mode"]); m != "" {
			parts = append(parts, "--"+m)
		}
	case "branch":
		if str(a["sub_action"]) == "delete" {
			parts = append(parts, "-D", str(a["name"]))
		}
	}

	parts = append(parts, strs(a["flags"])...)
	return strings.Join(append(parts, strs(a["files"])...), " ")
}

// commandStrings catches commands passed through script and meta tools, such as code_script's api.run("git push").
func commandStrings(a map[string]any) []string {
	var out []string
	for _, v := range a {
		switch t := v.(type) {
		case string:
			if strings.Contains(t, "git ") || strings.Contains(t, "gh ") || strings.Contains(t, "terraform ") || strings.Contains(t, "kubectl ") {
				out = append(out, extractRunArgs(t)...)
			}
		case map[string]any:
			out = append(out, commandStrings(t)...)
		}
	}
	return out
}

// extractRunArgs keeps only quoted strings from script code, so prose that mentions git is not mistaken for a command.
func extractRunArgs(code string) []string {
	var out []string
	for _, q := range []string{`"`, `'`, "`"} {
		parts := strings.Split(code, q)
		for i := 1; i < len(parts); i += 2 {
			out = append(out, parts[i])
		}
	}
	if len(out) == 0 {
		return []string{code}
	}
	return out
}

func str(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func strs(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

// scriptCall finds api.run(...) and api.tool(...) calls, allowing whitespace around the dots and parenthesis.
var (
	scriptCall    = regexp.MustCompile(`\bapi\s*\.\s*(run|tool)\s*\(`)
	execToken     = regexp.MustCompile(`\b(run|tool)\b`)
	dynamicAccess = regexp.MustCompile(`\bapi\s*\[|\bapi\s*\)|[(,=:]\s*api\s*[,)};]|\.\.\.\s*api\b`)
	execTools     = set("shell", "Bash", "git", "code_script")
)

const opaqueScript = "code_script runs a command the guard cannot read statically; pass plain string literals to api.run or use the shell tool"

// checkScript allows script commands only when every way the script could execute something is a plain
// literal call the guard can read. Aliases (`const {run} = api`), dynamic access (`api[x]`), passing `api`
// around and computed commands are all refused, because the hook sees source, not the executed command.
func checkScript(code string, readOnly bool) Result {
	if readOnly && strings.Contains(code, "api.edit") {
		return Result{Blocked: []Finding{{RuleReadOnly, "code_script edits files"}}}
	}

	plain := blankJS(code)
	if dynamicAccess.MatchString(plain) || hasTemplateExpression(code) {
		return Result{Blocked: []Finding{{RuleOpaque, opaqueScript}}}
	}

	var r Result
	calls := scriptCall.FindAllStringSubmatchIndex(plain, -1)
	if len(execToken.FindAllStringIndex(plain, -1)) != len(calls) {
		return Result{Blocked: []Finding{{RuleOpaque, opaqueScript}}}
	}

	nocomments := stripComments(code)
	for _, c := range calls {
		cmd, ok := scriptCommand(nocomments, plain[c[2]:c[3]], c[1])
		if !ok {
			r.Blocked = append(r.Blocked, Finding{RuleOpaque, opaqueScript})
			continue
		}
		if cmd == "" {
			continue
		}

		r.merge(Check(cmd, readOnly), "")
	}
	return r
}

// scriptCommand returns the shell command a call runs, "" for a tool that runs none, or false when unreadable.
func scriptCommand(code, kind string, args int) (string, bool) {
	if kind == "run" {
		return literalArg(code[args:])
	}

	name, ok := literalArg(code[args:])
	if !ok {
		return "", false
	}
	if !execTools[name] {
		return "", true
	}

	rest := code[args:]
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", false
	}
	return literalArg(rest[comma+1:])
}

// stripComments blanks comments only, keeping strings and byte positions.
func stripComments(code string) string {
	return scanJS(code, false)
}

// blankJS keeps every byte position but blanks comments and string contents,
// so token searches only see code and offsets still match the original source.
func blankJS(code string) string {
	return scanJS(code, true)
}

func scanJS(code string, blankStrings bool) string {
	b := []byte(code)
	blank := func(i int) {
		if b[i] != '\n' {
			b[i] = ' '
		}
	}

	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			for ; i < len(b) && b[i] != '\n'; i++ {
				blank(i)
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			for ; i+1 < len(b) && (b[i] != '*' || b[i+1] != '/'); i++ {
				blank(i)
			}
			if i+1 < len(b) {
				blank(i)
				blank(i + 1)
				i++
			}
		case b[i] == '"' || b[i] == '\'' || b[i] == '`':
			q := b[i]
			for i++; i < len(b) && b[i] != q; i++ {
				if b[i] == '\\' && i+1 < len(b) {
					if blankStrings {
						blank(i)
					}
					i++
				}
				if blankStrings {
					blank(i)
				}
			}
		}
	}
	return string(b)
}

// hasTemplateExpression reports a ${...} inside a template literal, which can run code the scanner blanks.
func hasTemplateExpression(code string) bool {
	nocomments := stripComments(code)
	var quote byte
	for i := 0; i < len(nocomments); i++ {
		c := nocomments[i]
		switch {
		case quote != 0 && c == '\\':
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote == '`' && c == '$' && i+1 < len(nocomments) && nocomments[i+1] == '{':
			return true
		case quote == 0 && (c == '"' || c == '\'' || c == '`'):
			quote = c
		}
	}
	return false
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// objectEnd returns the index just past the object literal starting at rest[0], or len(rest).
func objectEnd(rest string) int {
	depth := 0
	var quote byte
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case quote != 0 && c == '\\':
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(rest)
}

var singleCommandObject = regexp.MustCompile(`^\{\s*["']?command["']?\s*:\s*("[^"\\]*"|'[^'\\]*'|\x60[^\x60\\]*\x60)\s*,?\s*\}$`)

// literalArg reads the first argument after a call's opening parenthesis when it is a complete string literal,
// or an object whose command field is one; anything computed is reported as not literal.
func literalArg(rest string) (string, bool) {
	rest = strings.TrimLeft(rest, " \t\n,")
	if strings.HasPrefix(rest, "{") {
		// Only `{command: "<literal>"}` is trusted. Duplicate, computed, escaped or spread keys can make
		// JavaScript run a different command than the one the guard reads, so any other shape is refused.
		end := objectEnd(rest)
		after := strings.TrimLeft(rest[end:], " \t\n")
		if after == "" || after[0] != ')' {
			return "", false
		}
		m := singleCommandObject.FindStringSubmatch(rest[:end])
		if m == nil {
			return "", false
		}
		rest = m[1]
	}
	if rest == "" {
		return "", false
	}

	q := rest[0]
	if q != '"' && q != '\'' && q != '`' {
		return "", false
	}

	var s strings.Builder
	for i := 1; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c == '\\':
			// JavaScript escapes such as \u0067 or \x67 can spell a command the guard would not see.
			return "", false
		case q == '`' && c == '$' && i+1 < len(rest) && rest[i+1] == '{':
			return "", false
		case c == q:
			next := strings.TrimLeft(rest[i+1:], " \t\n")
			if next == "" || next[0] == ')' || next[0] == ',' || next[0] == '}' {
				return s.String(), true
			}
			return "", false
		default:
			s.WriteByte(c)
		}
	}
	return "", false
}
