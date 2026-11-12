package modelconfig

import (
	"os"
	"testing"

	"github.com/codeliter/codeforge/src/config"
)

// TestMain drops the model overrides of the shell that runs the tests, so a developer's or the gate's own settings
// never decide what a test loads. A test that wants one sets it with t.Setenv.
func TestMain(m *testing.M) {
	for _, name := range config.EnvironmentNames {
		_ = os.Unsetenv(name) //nolint:errcheck // a fixed, valid name can always be unset
	}
	os.Exit(m.Run())
}
