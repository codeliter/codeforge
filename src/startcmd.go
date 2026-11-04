package src

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
)

var (
	errStartUsage = errors.New(`usage: codeforge ticket [--ticket ID] ["what to do"]`)
	errRequired   = errors.New("required")
)

type work struct {
	ticket      string
	description string
}

// startArgs collects the ticket and description, then turns them into a task prompt for the agent.
// It also returns the description alone, which the effort judges score.
func startArgs(args []string) ([]string, string, error) {
	w, rest, err := workFromArgs(args)
	if err != nil {
		return nil, "", err
	}

	if w.description == "" {
		if !interactive() {
			return nil, "", errStartUsage
		}
		if err := askWork(&w); err != nil {
			return nil, "", err
		}
	}
	return append(rest, w.prompt()), strings.TrimSpace(w.description), nil
}

func workFromArgs(args []string) (work, []string, error) {
	var w work
	var rest, words []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--ticket" && i+1 < len(args):
			w.ticket = args[i+1]
			i++
		case flagsWithValue[args[i]] && i+1 < len(args):
			rest = append(rest, args[i], args[i+1])
			i++
		case strings.HasPrefix(args[i], "-"):
			rest = append(rest, args[i])
		default:
			words = append(words, args[i])
		}
	}

	if err := checkEffort(rest); err != nil {
		return w, nil, err
	}
	w.description = strings.Join(words, " ")
	return w, rest, nil
}

// askWork asks for the ticket and what needs doing, on one page.
func askWork(w *work) error {
	const descriptionLines = 6
	return runGroups("ticket form",
		huh.NewGroup(
			huh.NewInput().
				Title("Ticket").
				Description("Leave empty if there is none.").
				Value(&w.ticket),
			huh.NewText().
				Title("Description").
				Description("What needs to happen, with any context, links or limits. This also sets the effort.").
				Lines(descriptionLines).
				Value(&w.description).
				Validate(required("a description")),
		),
	)
}

// prompt leads with the description; the skill and ticket lines follow it.
func (w work) prompt() string {
	var s strings.Builder
	s.WriteString(strings.TrimSpace(w.description))
	s.WriteString("\n\nUse the codeforge-ticket-to-pr skill.")
	if w.ticket != "" {
		fmt.Fprintf(&s, "\nTicket: %s. Branch and PR title start with it.", w.ticket)
	} else {
		s.WriteString("\nNo ticket. Ask for one before creating a branch if the team uses tickets.")
	}
	return s.String()
}

func required(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("enter %s: %w", what, errRequired)
		}
		return nil
	}
}
