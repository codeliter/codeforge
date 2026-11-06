package review

import (
	"os/exec"

	"github.com/codeliter/codeforge/src/effort"
)

// noPrompts keeps gh and git from waiting for a person: a prompt nobody can answer would hold the review until its deadline.
var noPrompts = []string{"GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0"}

// Bound makes a context-bound command die as a whole, descendants and open pipes included.
func Bound(cmd *exec.Cmd) { effort.Bound(cmd) }

// Reap kills what a finished bound command left running.
func Reap(cmd *exec.Cmd) { effort.Reap(cmd) }
