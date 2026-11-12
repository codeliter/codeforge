package modelconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
)

const execPerm = 0o755

// writeConfig writes a legacy JSON global configuration, which codeforge still reads when no YAML exists.
func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(config.Dir(home), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config.Dir(home), "config.json"), []byte(body), 0o600))
}

func providerTestCatalog() []string {
	return []string{"llmgateway/claude-sonnet-5-5", "llmgateway/claude-opus-5-5", "llmgateway/gpt-6-1-sol", "copilot/claude-sonnet-5.5", "copilot/claude-opus-5.5", "copilot/gpt-6.1-sol"}
}
