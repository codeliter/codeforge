package src

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHelpListsRespond(t *testing.T) {
	require.Contains(t, helpText, "respond [PR] [--watch]")
}
