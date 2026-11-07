package guard

import (
	"os"
	"regexp"
	"slices"
	"strings"
)

// learningText spots codeforge named in code the guard cannot parse, such as interpreter code, a script file or a
// wrapped command. Such code could build `codeforge observe` in ways no pattern reads (concatenation, variables,
// line continuations), so any mention is refused outside review mode. Review mode refuses far more.
var learningText = regexp.MustCompile(`\bcodeforge\b`)

const learningReason = "only you can record or revert codeforge learning evidence"

var learningWriteWord = regexp.MustCompile(`\b(observe|correct|outcome|revert|accept|dismiss|forget|approve)\b`)

// mentionsLearningWrite reports a command that is not codeforge, git or a read-only tool but names codeforge together
// with a learning write word, as in `tmux new -d 'codeforge observe'`. Direct codeforge calls are classified exactly.
func mentionsLearningWrite(words []string, seg string) bool {
	if words[0] == "git" || words[0] == "codeforge" || readOnlyCommands[words[0]] {
		return false
	}
	return learningText.MatchString(seg) && learningWriteWord.MatchString(seg)
}

var learningWord = regexp.MustCompile(`\b(observe|learn)\b`)

// substitutedLearning reports a command that mentions codeforge and runs a substitution where it can build the call:
// at command position (`$(echo codeforge) observe`) or as an argument of codeforge (`codeforge $(echo observe)`).
// A substitution inside an argument of another command, such as git commit -m "... $(date)", only makes text.
func substitutedLearning(command string) bool {
	return (learningText.MatchString(command) || learningWord.MatchString(command)) && buildsCommand(command)
}

const binarySniffBytes = 8 << 10

// looksBinary reports a file whose first block holds a NUL byte, as every native executable does. A file that
// cannot be read counts as binary: there is nothing the guard could inspect.
func looksBinary(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return true
	}
	defer f.Close()
	head := make([]byte, binarySniffBytes)
	n, err := f.Read(head)
	return err != nil && n == 0 || slices.Contains(head[:n], 0)
}

// scriptWritesLearning reports a local script that names codeforge or is too large to read.
func scriptWritesLearning(path, cwd string) bool {
	p := path
	if !strings.HasPrefix(p, "/") {
		p = cwd + "/" + p
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	// A text script too large to read could hold the call anywhere, so it is refused. Native executables such as
	// /usr/bin/curl are large but not scripts: a NUL byte in the first block marks them as binary.
	if info.Size() > maxScriptBytes {
		return !looksBinary(p)
	}
	data, err := os.ReadFile(p)
	return err == nil && learningText.Match(data)
}
