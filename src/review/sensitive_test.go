package review

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSensitiveMatch(t *testing.T) {
	files := []string{"internal/auth/login.go", "auth/token.go", "src/guard/rules.go", "db/schema.sql", "build/Dockerfile", "docs/readme.md", "oauth/x.go"}
	tests := []struct {
		pattern string
		want    []string
	}{
		{"auth/", []string{"internal/auth/login.go", "auth/token.go"}},
		{"auth/**", []string{"internal/auth/login.go", "auth/token.go"}},
		{"src/guard/*.go", []string{"src/guard/rules.go"}},
		{"*.sql", []string{"db/schema.sql"}},
		{"Dockerfile", []string{"build/Dockerfile"}},
		{"internal/", []string{"internal/auth/login.go"}},
		{"docs/readme.md", []string{"docs/readme.md"}},
		{"auth*/", []string{"internal/auth/login.go", "auth/token.go"}},
		{"src/*/**", []string{"src/guard/rules.go"}},
		{"./auth/", []string{"internal/auth/login.go", "auth/token.go"}},
		{"./*.sql", []string{"db/schema.sql"}},
		{"src/**/x.go", nil},
		{"", nil},
		{"/", nil},
		{"[", nil},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, SensitiveMatch(files, []string{tt.pattern}), tt.pattern)
	}
	require.Nil(t, SensitiveMatch(files, nil))
}

func TestValidSensitivePattern(t *testing.T) {
	for _, ok := range []string{"auth/", "src/guard/*.go", "*.sql", "Dockerfile", "payments/**", "./auth/", "foo..bar", "src/*/**", "auth*/"} {
		require.Empty(t, ValidSensitivePattern(ok), ok)
	}
	for _, bad := range []string{"", " ", "/etc/passwd", "~/x", "../secrets", "a/../b", "[", "src/**/x.go", "**", "**/x", "a//b", "a**b/"} {
		require.NotEmpty(t, ValidSensitivePattern(bad), bad)
	}
}

func TestRaiseForPaths(t *testing.T) {
	tier, why := RaiseForPaths(TierSmall, "typo fix", []string{"auth/login.go", "README.md"}, []string{"auth/"})
	require.Equal(t, TierRisky.Name, tier.Name)
	require.Contains(t, why, "typo fix; sensitive path: auth/login.go")

	tier, why = RaiseForPaths(TierStandard, "", []string{"a.go"}, []string{"auth/"})
	require.Equal(t, TierStandard.Name, tier.Name, "no match leaves the tier alone")
	require.Empty(t, why)

	tier, _ = RaiseForPaths(TierRisky, "", []string{"auth/a.go"}, []string{"auth/"})
	require.Equal(t, TierRisky.Name, tier.Name, "patterns never lower a tier")

	_, why = RaiseForPaths(TierSmall, "", []string{"auth/1.go", "auth/2.go", "auth/3.go", "auth/4.go", "auth/5.go"}, []string{"auth/"})
	require.Contains(t, why, "and 2 more")
}

func TestSizeNote(t *testing.T) {
	require.Empty(t, SizeNote(largeDiffBytes-1))
	note := SizeNote(250_000)
	require.Contains(t, note, "250 KB")
	require.True(t, strings.Contains(note, "splitting"))
}
