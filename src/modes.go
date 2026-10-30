package src

import (
	"errors"
	"fmt"
	"slices"

	"github.com/charmbracelet/huh"
)

const (
	modeTicket = "ticket"
	modeChat   = "chat"
	modeReview = "review"
)

var (
	modes          = []string{modeTicket, modeChat, modeReview}
	errUnknownMode = errors.New("unknown mode")
)

func profileFor(mode string) string {
	return "codeforge-" + mode
}

func validMode(mode string) error {
	if mode == "" || slices.Contains(modes, mode) {
		return nil
	}
	return fmt.Errorf("%w %q; use %v", errUnknownMode, mode, modes)
}

// pickMode asks only when there is no default and no task on the command line to infer from.
func pickMode(cfg Config, args []string) (string, error) {
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
			huh.NewOption("Review: a PR or my local work", modeReview),
		).
		Value(&mode))
	return mode, err
}
