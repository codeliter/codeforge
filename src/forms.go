package src

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// runForm shows a form on the alternate screen. Drawing in place leaves stale copies of earlier frames in
// some terminals; a full-screen form is cleared and restored as a whole.
func runForm(name string, fields ...huh.Field) error {
	return runGroups(name, huh.NewGroup(fields...))
}

func runGroups(name string, groups ...*huh.Group) error {
	form := huh.NewForm(groups...).WithProgramOptions(tea.WithAltScreen())
	if err := form.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
