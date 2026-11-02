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
