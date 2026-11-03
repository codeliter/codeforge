package src

import (
	"fmt"
	"io"
)

// acknowledgeTask confirms receipt before context collection or provider calls.
// This is a local status message, not a claim that the model has responded.
func acknowledgeTask(w io.Writer, mode string, args []string) {
	if hasFlag(args, "--headless") {
		return
	}
	var message string
	switch mode {
	case modeReview:
		message = "Review requested. Collecting context; draft only, nothing will be posted."
	case "respond":
		message = "Review responses requested. Collecting comments and context."
	default:
		if promptOf(args) == "" {
			return
		}
		message = "Task received. Preparing context; model response pending."
	}
	fmt.Fprintln(w, "codeforge:", message)
}
