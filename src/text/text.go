// Package text holds the string helpers more than one codeforge package needs.
package text

import "unicode/utf8"

// Cut returns at most n bytes of s, cut at a character boundary so that a multi-byte character is never split.
func Cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Shorten is Cut with the cut marked by an ellipsis, for text a person reads.
func Shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return Cut(s, n) + "…"
}
