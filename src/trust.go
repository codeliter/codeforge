package src

import (
	"fmt"
	"os"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"
)

// trusted asks before a new or changed repository persona reaches the system prompt,
// because a cloned repository could use it to steer the agent.
func trusted(home, path, content string) (bool, error) {
	ts, err := config.LoadTrust(home)
	if err != nil {
		return false, err
	}
	if ts[path] == config.Digest(content) {
		return true, nil
	}
	if !interactive() {
		fmt.Fprintf(os.Stderr, "codeforge: skipping %s: new or changed since you last approved it. Run `codeforge trust` to use it.\n", path)
		return false, nil
	}
	ok, err := confirmTrust("Use this repository's persona?",
		fmt.Sprintf("%s is new or changed. It becomes part of the agent's instructions in this repository.\n\n%s", path, preview(content)))
	if err != nil || !ok {
		return false, err
	}
	return true, config.Trust(home, path, content)
}

// modelTrustApprover asks the user, at the terminal, before a new or changed project model configuration is used. It
// is nil when nobody is there to ask, which config.Load reports as a skipped file.
func modelTrustApprover() config.Approver {
	if !interactive() {
		return nil
	}
	return func(path, preview string) (bool, error) {
		return confirmTrust("Use this repository's model configuration?",
			fmt.Sprintf("%s is new or changed. It can select providers, models, reviewers and helper routing. Review this file before trusting it.\n\n%s", path, preview))
	}
}

func confirmTrust(title, description string) (bool, error) {
	a, err := forms.Ask("trust prompt", forms.Field{Kind: forms.Confirm, Title: title, Description: description, Affirmative: "Trust it", Negative: "Skip"})
	return a.Yes, err
}

func preview(content string) string {
	const maxPreview = 1500
	if len(content) <= maxPreview {
		return content
	}
	return content[:maxPreview] + "\n…"
}

// runTrustModels approves the project model configuration as it is now.
func runTrustModels(home, project string) error {
	if err := config.ApproveProject(home, project); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "codeforge: trusted model configuration %s\n", config.ProjectPath(project))
	return nil
}
