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
	ticket   string
	summary  string
	details  string
	done     string
	outOfBox string
}

// startArgs collects the ticket and work details, then turns them into a task prompt for the agent.
func startArgs(args []string) ([]string, error) {
	w, rest, err := workFromArgs(args)
	if err != nil {
		return nil, err
	}

	if w.summary == "" {
		if !interactive() {
			return nil, errStartUsage
		}
		if err := askWork(&w); err != nil {
			return nil, err
		}
	}
	return append(rest, w.prompt()), nil
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
	w.summary = strings.Join(words, " ")
	return w, rest, nil
}

// askWork asks on two short pages, so each fits a normal terminal without scrolling a field out of view.
func askWork(w *work) error {
	const textLines = 3
	return runGroups("ticket form",
		huh.NewGroup(
			huh.NewInput().
				Title("Ticket").
				Description("Leave empty if there is none.").
				Value(&w.ticket),
			huh.NewInput().
				Title("What needs to happen?").
				Description("One line. This picks the effort.").
				Value(&w.summary).
				Validate(required("a summary")),
			huh.NewText().
				Title("Details").
				Description("Context, links, logs, constraints. Optional.").
				Lines(textLines).
				Value(&w.details),
		),
		huh.NewGroup(
			huh.NewText().
				Title("Done when").
				Description("How you will know it works. Optional.").
				Lines(textLines).
				Value(&w.done),
			huh.NewText().
				Title("Out of scope").
				Description("Anything the agent must not touch. Optional.").
				Lines(textLines).
				Value(&w.outOfBox),
		),
	)
}

// prompt leads with the summary so the effort judges score the work, not the template around it.
func (w work) prompt() string {
	var s strings.Builder
	s.WriteString(w.summary)
	s.WriteString("\n\nUse the codeforge-ticket-to-pr skill.")
	if w.ticket != "" {
		fmt.Fprintf(&s, "\nTicket: %s. Branch and PR title start with it.", w.ticket)
	} else {
		s.WriteString("\nNo ticket. Ask for one before creating a branch if the team uses tickets.")
	}

	section := func(title, body string) {
		if body = strings.TrimSpace(body); body != "" {
			fmt.Fprintf(&s, "\n\n%s:\n%s", title, body)
		}
	}
	section("Details", w.details)
	section("Done when", w.done)
	section("Out of scope", w.outOfBox)
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
