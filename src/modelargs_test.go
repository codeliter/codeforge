package src

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
)

func TestProviderModelFlagsAreNotPromptText(t *testing.T) {
	args := []string{"--system", "--model", "--model=Sonnet 5.5", "--system", "--effort"}
	resolved, routes, err := resolveModelArguments(args, &config.ProviderPolicy{Primary: "devpass"}, providerTestCatalog())
	require.NoError(t, err)
	require.Len(t, routes, 1)
	require.Equal(t, []string{"--system", "--model", "--model=llmgateway/claude-sonnet-5-5", "--system", "--effort"}, resolved)
	require.False(t, hasEffort(args))
	require.Empty(t, flagValue(args, "--effort"))
	require.Equal(t, []string{"--system", "--model", "--system", "--effort"}, dropFlag(args, "--model"))
	require.False(t, argumentsNeedCatalog([]string{"--system", "--model"}))
	_, _, err = resolveModelArguments([]string{"--model"}, nil, nil)
	require.ErrorIs(t, err, config.ErrModelName)
	require.True(t, slices.Equal(args, []string{"--system", "--model", "--model=Sonnet 5.5", "--system", "--effort"}))
}
