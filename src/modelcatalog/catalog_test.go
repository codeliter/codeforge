package modelcatalog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseContexts(t *testing.T) {
	listing := `Subscriptions (subscriptions)
  source: live listing
  subscriptions/claude-opus-5-5  1000k ctx  $4/$20/M
  subscriptions/gpt-6.1-sol  1050k ctx  $2/$10/M (cache r$0.2/w$0)
  llmgateway/claude-haiku-4-5  200k ctx  $1/$5/M
  llmgateway/odd-size  big ctx
  llmgateway/no-size
`
	require.Equal(t, map[string]string{
		"subscriptions/claude-opus-5-5": "1M",
		"subscriptions/gpt-6.1-sol":     "1.1M",
		"llmgateway/claude-haiku-4-5":   "200K",
	}, ParseContexts(strings.NewReader(listing)))
}
