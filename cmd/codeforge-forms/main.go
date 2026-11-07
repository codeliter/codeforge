// Command codeforge-forms asks the user the questions codeforge sends it. It reads a forms.Form as JSON on descriptor 3,
// shows it on the terminal (descriptors 0, 1 and 2) and writes the forms.Answers as JSON on descriptor 4. It exits 2
// when the user leaves the form without answering, 1 on any other failure.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"

	"github.com/codeliter/codeforge/src/forms"
)

const (
	specFD    = 3
	answersFD = 4
)

func main() {
	spec := os.NewFile(specFD, "form")
	out := os.NewFile(answersFD, "answers")
	if spec == nil || out == nil {
		fmt.Fprintln(os.Stderr, "codeforge-forms: run by codeforge, not by hand: it needs the form on descriptor 3 and writes the answers on 4")
		os.Exit(1)
	}
	var f forms.Form
	if err := json.NewDecoder(spec).Decode(&f); err != nil {
		fmt.Fprintln(os.Stderr, "codeforge-forms: reading the form:", err)
		os.Exit(1)
	}
	answers, err := ask(f)
	if errors.Is(err, huh.ErrUserAborted) {
		os.Exit(forms.AbortedExit)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge-forms:", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(out).Encode(answers); err != nil {
		fmt.Fprintln(os.Stderr, "codeforge-forms: writing the answers:", err)
		os.Exit(1)
	}
}

// ask shows the form on the alternate screen. Drawing in place leaves stale copies of earlier frames in some
// terminals; a full-screen form is cleared and restored as a whole.
func ask(f forms.Form) (forms.Answers, error) {
	form, answers := build(f)
	if err := form.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name, err)
	}
	return answers(), nil
}
