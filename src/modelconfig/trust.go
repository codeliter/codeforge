// Package modelconfig is the models and provider commands: choosing the model for each role, the provider chains that
// serve them, and applying both to Empryo.
package modelconfig

import (
	"fmt"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"
)

// TrustApprover asks the user, at the terminal, before a new or changed project model configuration is used. It
// is nil when nobody is there to ask, which config.Load reports as a skipped file.
func TrustApprover() config.Approver {
	if !forms.Interactive() {
		return nil
	}
	return func(path, preview string) (bool, error) {
		return forms.AskTrust("Use this repository's model configuration?",
			fmt.Sprintf("%s is new or changed. It can select providers, models, reviewers and helper routing. Review this file before trusting it.\n\n%s", path, preview))
	}
}
