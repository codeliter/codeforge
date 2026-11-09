package src

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"

	"golang.org/x/term"

	"github.com/codeliter/codeforge/src/review"
)

const (
	modeTicket = config.ModeTicket
	modeChat   = config.ModeChat
	modeWork   = config.ModeWork
	modeReview = config.ModeReview
)

var (
	modes          = config.Modes
	errUnknownMode = config.ErrUnknownMode
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
	if !ok || config.ValidMode(mode) != nil || mode == "" || !term.IsTerminal(int(f.Fd())) || os.Getenv("TERM") == "dumb" || os.Getenv("NO_COLOR") != "" {
		return func() {}
	}
	fmt.Fprintf(w, "\x1b[22;0t\x1b]0;%s\x07", modeTitle(mode))
	return func() { fmt.Fprint(w, "\x1b[23;0t") }
}

// commands are the words Run handles before or instead of a mode; any of them is never a task.
var commands = []string{"start", "respond", "init", "setup", "gate", "trust", "doctor", "repair", "reinit", "models",
	"provider", "providers", "learn", "allow", "version", "help", "completion", "sessions", "persona"}

// internalCommands are dispatched by main before Run: the hooks Empryo and git call, which read stdin.
var internalCommands = []string{"hook", "pre-push", "gh-shim", "session-end", "observe"}

// RegisteredCommands lists every word the binary dispatches on: the modes, the commands, and the internal hook commands.
// Each of them answers -h and --help with usage and changes nothing.
func RegisteredCommands() []string {
	all := slices.Concat(modes, commands, internalCommands)
	slices.Sort(all)
	return slices.Compact(all)
}

// commandWord is what a mistyped mode or command looks like: one lowercase word, as opposed to a task in prose.
var commandWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// unknownCommand rejects a lone word that is not a mode or command, such as `codeforge zsh`. Without it the word
// becomes a task and is sent to the judges and the model, which costs money and does nothing useful. A task of one
// word can still be given after an explicit mode, as in `codeforge chat zsh`.
func unknownCommand(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || !commandWord.MatchString(args[0]) || promptOf(args) != args[0] ||
		slices.Contains(modes, args[0]) || slices.Contains(commands, args[0]) {
		return nil
	}
	return fmt.Errorf("%w %q; use one of %v or a command from `codeforge help`. For a one-word task, name the mode: codeforge chat %s", errUnknownMode, args[0], modes, args[0])
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
func pickMode(cfg config.Config, args []string) (string, error) {
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
	return forms.Choose("What are we doing?", "", []forms.Option{
		{Label: "Ticket: take a task to a verified change and draft PR", Value: modeTicket},
		{Label: "Chat: questions, explanations, small edits", Value: modeChat},
		{Label: "Work: conversational tasks through implementation and verification", Value: modeWork},
		{Label: "Review: a PR or my local work", Value: modeReview},
	}, modeTicket)
}
