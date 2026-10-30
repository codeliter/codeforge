package review

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTriage(t *testing.T) {
	small := "+++ b/a.go\n+func A() {}\n"
	big := "+++ b/a.go\n" + strings.Repeat("+x := 1\n", largeDiffLines)

	tests := []struct {
		name  string
		diff  string
		files []string
		want  Tier
	}{
		{"docs only", "+text", []string{"README.md", "docs/guide.go"}, TierDocs},
		{"small code", small, []string{"a.go"}, TierSmall},
		{"medium code", "+++ b/a.go\n" + strings.Repeat("+x := 1\n", 200), []string{"a.go"}, TierStandard},
		{"large code", big, []string{"a.go"}, TierRisky},
		{"auth path", small, []string{"internal/auth/token.go"}, TierRisky},
		{"terraform", small, []string{"infra/cache.tf"}, TierRisky},
		{"migration", small, []string{"db/migrations/001.sql"}, TierRisky},
		{"goroutine in a small diff", "+\tgo func() {}()\n", []string{"a.go"}, TierRisky},
		{"mutex", "+\tmu sync.Mutex\n", []string{"a.go"}, TierRisky},
		{"docs plus code is not docs", small, []string{"README.md", "a.go"}, TierSmall},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want.Name, Triage(tt.diff, tt.files).Name)
		})
	}
}

func TestSymbols(t *testing.T) {
	diff := "+func Ratio(a, b int) int {\n-func (s *Store) Get() {}\n+type Options struct{}\n+export async function loadUser() {}\n+func helper() {}\n"
	require.Equal(t, []string{"Ratio", "Get", "Options", "loadUser"}, symbols(diff))
}

func TestCallers(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"git -C /r grep -n -w -e Ratio --": "div.go:4:func Ratio(a, b int) int {\napi/h.go:12:\treturn div.Ratio(x, y)\n.env:3:Ratio=secret\n",
	}}

	got := Callers(t.Context(), r, "/r", "+func Ratio(a, b int) int {\n", []string{"div.go"})
	require.Equal(t, "Ratio:\n  api/h.go:12:\treturn div.Ratio(x, y)\n", got)
}

func TestFitEffort(t *testing.T) {
	kimi := []string{"off", "low", "high", "max"}
	full := []string{"off", "low", "medium", "high", "xhigh", "max"}

	require.Equal(t, "low", fitEffort("medium", kimi), "never escalate past the tier")
	require.Equal(t, "high", fitEffort("high", kimi))
	require.Equal(t, "low", fitEffort("low", kimi))
	require.Equal(t, "medium", fitEffort("medium", full))
	require.Equal(t, "medium", fitEffort("medium", nil), "unknown support passes through")
	require.Equal(t, "high", fitEffort("low", []string{"high", "max"}), "lowest supported when nothing fits below")
}
