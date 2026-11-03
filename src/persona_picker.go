package src

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
)

func starterOptions(choices []starter) []huh.Option[string] {
	options := make([]huh.Option[string], 0, len(choices))
	for _, choice := range choices {
		options = append(options, huh.NewOption(choice.Name+": "+choice.Description, choice.Name))
	}
	return options
}

// resolveStarter keeps explicit names non-interactive and propagates picker cancellation.
func resolveStarter(name string, choose func() (string, error)) (string, error) {
	if name == "" {
		var err error
		name, err = choose()
		if err != nil {
			return "", err
		}
	}
	if _, err := starterPersona(name); err != nil {
		return "", err
	}
	return name, nil
}

func pickStarter() (string, error) {
	choices := starters()
	if len(choices) == 0 {
		return "", fmt.Errorf("%w: no starter personas available", errInitUsage)
	}
	if !interactive() {
		var descriptions strings.Builder
		for _, choice := range choices {
			fmt.Fprintf(&descriptions, "  %s: %s\n", choice.Name, choice.Description)
		}
		return "", fmt.Errorf("%w: --persona needs a name without a terminal. Available choices:\n%s", errInitUsage, descriptions.String())
	}
	selected := choices[0].Name
	err := runForm("persona picker", huh.NewSelect[string]().
		Title("Pick a starter persona").
		Description("Choose with the arrow keys, then press Enter.").
		Options(starterOptions(choices)...).
		Value(&selected))
	return selected, err
}
