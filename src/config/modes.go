package config

import (
	"errors"
	"fmt"
	"slices"
)

// The codeforge modes: what a session is for.
const (
	ModeTicket = "ticket"
	ModeChat   = "chat"
	ModeWork   = "work"
	ModeReview = "review"
)

// Modes lists every mode, in the order the picker shows them.
var Modes = []string{ModeTicket, ModeChat, ModeWork, ModeReview}

// ErrUnknownMode is returned for a mode name that is not one of Modes.
var ErrUnknownMode = errors.New("unknown mode")

// ValidMode reports whether mode names a mode; "" is valid, for "not chosen yet".
func ValidMode(mode string) error {
	if mode == "" || slices.Contains(Modes, mode) {
		return nil
	}
	return fmt.Errorf("%w %q; use %v", ErrUnknownMode, mode, Modes)
}
