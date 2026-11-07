package guard

import "strings"

// expandsInShell reports whether the shell would replace part of command with text that is not written in it: a variable
// or command substitution anywhere outside single quotes, or a glob, brace or tilde outside every quote. The user approves
// a PR comment exactly as it is written, so a comment that expands is not offered. Single quotes and a backslash make
// text literal, as in 'cost $5' and "cost \$5".
func expandsInShell(command string) bool { return scanExpansion(command, true) }

// substitutes reports a variable or command substitution anywhere outside single quotes in command. The command is split
// into segments before it is judged, and a substitution is split out of the segment that holds it, so this looks at the
// whole line.
func substitutes(command string) bool { return scanExpansion(command, false) }

func scanExpansion(command string, globs bool) bool {
	var quote byte // 0 outside quotes, otherwise the quote character
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\':
			i++ // the next character is literal
		case c == '\'' && quote == 0:
			quote = '\''
		case c == '"':
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
		case c == '$' || c == '\x60':
			return true
		case globs && quote == 0 && strings.IndexByte("*?[{~", c) >= 0:
			return true
		}
	}
	return false
}
