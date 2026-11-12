package text

import (
	"strings"
	"testing"
	"unicode/utf8"

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

func TestPreview(t *testing.T) {
	require.Equal(t, "short", Preview("short"))
	long := strings.Repeat("a", 1499) + "é" + strings.Repeat("b", 100)
	got := Preview(long)
	require.True(t, utf8.ValidString(got), "the cut never splits a character")
	require.Equal(t, strings.Repeat("a", 1499)+"\n…", got)
}
