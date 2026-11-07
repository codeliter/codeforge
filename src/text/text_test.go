package text

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCut(t *testing.T) {
	require.Equal(t, "ab", Cut("abé", 3))
	require.Equal(t, "abc", Cut("abc", 5))
	require.Empty(t, Cut("é", 1))
}

func TestShorten(t *testing.T) {
	require.Equal(t, "ab…", Shorten("abcdef", 2))
	require.Equal(t, "abc", Shorten("abc", 3))
	require.Equal(t, "ab…", Shorten("abé", 3))
}
