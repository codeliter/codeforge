package src

import (
	"fmt"
	"os"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/text"
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
		fmt.Sprintf("%s is new or changed. It becomes part of the agent's instructions in this repository.\n\n%s", path, text.Preview(content)))
	if err != nil || !ok {
		return false, err
	}
	return true, config.Trust(home, path, content)
}

func confirmTrust(title, description string) (bool, error) {
	return forms.AskTrust(title, description)
}

// runTrustModels approves the project model configuration as it is now.
func runTrustModels(home, project string) error {
	if err := config.ApproveProject(home, project); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "codeforge: trusted model configuration %s\n", config.ProjectPath(project))
	return nil
}
