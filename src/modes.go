package src

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"github.com/codeliter/codeforge/src/review"
)

const (
	modeTicket = "ticket"
	modeChat   = "chat"
	modeWork   = "work"
	modeReview = "review"
)

var (
	modes          = []string{modeTicket, modeChat, modeWork, modeReview}
	errUnknownMode = errors.New("unknown mode")
)

func profileFor(mode string) string {
	return "codeforge-" + mode
}

// modeTitle names the Codeforge mode powering an Empryo window, e.g. "Codeforge · work".
func modeTitle(mode string) string {
	return "Codeforge · " + mode
}

// showModeTitle puts the mode in the terminal's window and tab title while Empryo runs, because Empryo's
// TUI does not set a title of its own. It pushes the previous title and pops it back on return.
func showModeTitle(w io.Writer, mode string) func() {
	f, ok := w.(*os.File)
	if !ok || validMode(mode) != nil || mode == "" || !term.IsTerminal(int(f.Fd())) || os.Getenv("TERM") == "dumb" || os.Getenv("NO_COLOR") != "" {
		return func() {}
	}
	fmt.Fprintf(w, "\x1b[22;0t\x1b]0;%s\x07", modeTitle(mode))
	return func() { fmt.Fprint(w, "\x1b[23;0t") }
}

func validMode(mode string) error {
	if mode == "" || slices.Contains(modes, mode) {
		return nil
	}
	return fmt.Errorf("%w %q; use %v", errUnknownMode, mode, modes)
}

var (
	prLink = regexp.MustCompile(`^https://github\.com/[\w.-]+/[\w.-]+/pull/\d+(?:[/?#]\S*)?$`)
	prRef  = regexp.MustCompile(`^#\d+$`)
)

// suggestMode reads an unambiguous intent from the arguments, so the user is not asked what they already said:
// --ticket means ticket mode, and a lone github.com PR link or #number means review. It returns "" when unsure,
// and the reason is a short line that tells the user why the mode was chosen.
func suggestMode(args []string) (mode, reason string) {
	if hasFlag(args, "--ticket") {
		return modeTicket, "--ticket given; starting ticket mode"
	}
	if prompt := promptOf(args); (prLink.MatchString(prompt) || prRef.MatchString(prompt)) && validPRRef(prompt) {
		return modeReview, "PR reference given; starting review mode"
	}
	return "", ""
}

// validPRRef reports whether review can load the reference, so a suggestion never leads to a usage error.
func validPRRef(ref string) bool {
	_, err := review.ParseRef(ref)
	return err == nil
}

// pickMode asks for new launches only; the resume check defends this invariant for other callers.
func pickMode(cfg Config, args []string) (string, error) {
	if _, resume, err := sessionID(args); err != nil {
		return "", err
	} else if resume {
		return "", errSessionMode
	}
	if mode, _ := suggestMode(args); mode != "" {
		return mode, nil
	}
	if cfg.DefaultMode != "" {
		return cfg.DefaultMode, nil
	}
	if promptOf(args) != "" || !interactive() {
		return modeChat, nil
	}
	mode := modeTicket
	err := runForm("mode picker", huh.NewSelect[string]().
		Title("What are we doing?").
		Options(
			huh.NewOption("Ticket: take a task to a verified change and draft PR", modeTicket),
			huh.NewOption("Chat: questions, explanations, small edits", modeChat),
			huh.NewOption("Work: conversational tasks through implementation and verification", modeWork),
			huh.NewOption("Review: a PR or my local work", modeReview),
		).Value(&mode))
	return mode, err
}
