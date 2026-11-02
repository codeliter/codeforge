package main

import (
	"fmt"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src"
)

func TestEngineExitCode(t *testing.T) {
	code, ok := engineExitCode(&src.EngineExitError{Code: 143})
	require.True(t, ok)
	require.Equal(t, 143, code)
	err := exec.CommandContext(t.Context(), "sh", "-c", "exit 7").Run()
	require.Error(t, err)
	_, ok = engineExitCode(fmt.Errorf("setup failed: %w", err))
	require.False(t, ok, "non-engine subprocess errors must retain their diagnostics")
}
