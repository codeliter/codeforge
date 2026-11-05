package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeJudge struct {
	answer string
	err    error
}

func (j fakeJudge) Ask(context.Context, string, string) (string, error) {
	return j.answer, j.err
}

var (
	errJudgeDown   = errors.New("down")
	errJudgeKilled = errors.New("killed")
	errHTTP401     = errors.New("HTTP 401")
)

func judged(risk, complexity int) fakeJudge {
	return fakeJudge{answer: `{"risk":` + string(rune('0'+risk)) + `,"complexity":` + string(rune('0'+complexity)) + `,"reason":"r"}`}
}

func TestClassify(t *testing.T) {
	diff := "+++ b/a.go\n" + strings.Repeat("+x := 1\n", 2*tinyDiffLines)
	files := []string{"a.go"}
	broken := fakeJudge{err: errJudgeDown}

	tests := []struct {
		name   string
		judges []Judge
		diff   string
		files  []string
		want   Tier
	}{
		{"both judges see no risk", []Judge{judged(0, 0), judged(0, 1)}, diff, files, TierSmall},
		{"one judge sees an auth change", []Judge{judged(2, 1), judged(1, 1)}, diff, files, TierRisky},
		{"judges split 2 and 0 on risk", []Judge{judged(2, 0), judged(0, 0)}, diff, files, TierStandard},
		{"ordinary behaviour change stays with one reviewer", []Judge{judged(1, 0)}, diff, files, TierSmall},
		{"risk 1 and some tracing stays with one reviewer", []Judge{judged(1, 1), judged(1, 1)}, diff, files, TierSmall},
		{"one judge alarms and the other sees nothing", []Judge{judged(2, 0), judged(0, 0)}, diff, files, TierStandard},
		{"hard to verify", []Judge{judged(0, 2)}, diff, files, TierStandard},
		{"one judge down, the other answers", []Judge{broken, judged(0, 0)}, diff, files, TierSmall},
		{"no judge answers", []Judge{broken, broken}, diff, files, TierStandard},
		{"garbage answer counts as no answer", []Judge{fakeJudge{answer: "sure"}, fakeJudge{answer: `{"risk":9,"complexity":0}`}}, diff, files, TierStandard},
		{"tiny diff asks only the first judge, and a risky answer counts", []Judge{judged(2, 2), judged(0, 0)}, "+++ b/a.go\n+func A() {}\n", files, TierRisky},
		{"tiny diff never skips the judge", []Judge{judged(0, 0)}, "+++ b/a.go\n+func A() {}\n", files, TierSmall},
		{"tiny diff with a dead judge falls back to standard", []Judge{broken}, "+++ b/a.go\n+func A() {}\n", files, TierStandard},
		{"no judges configured", nil, diff, files, TierSmall},
		{"docs skip the judges", []Judge{judged(2, 2)}, "+text", []string{"README.md"}, TierDocs},
		{"infra files are a floor", []Judge{judged(0, 0)}, diff, []string{"infra/cache.tf"}, TierRisky},
		{"large diff is a floor", []Judge{judged(0, 0)}, "+++ b/a.go\n" + strings.Repeat("+x := 1\n", judgeFloorLines), files, TierStandard},
		{"mid-size mechanical diff can be small", []Judge{judged(0, 0)}, "+++ b/a.go\n" + strings.Repeat("+x := 1\n", 200), files, TierSmall},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := Classify(t.Context(), tt.judges, tt.diff, tt.files)
			require.Equal(t, tt.want.Name, got.Name)
		})
	}
}

type flakyJudge struct {
	answers []string
	calls   *int
}

func (f flakyJudge) Ask(context.Context, string, string) (string, error) {
	i := min(*f.calls, len(f.answers)-1)
	*f.calls++
	if f.answers[i] == "" {
		return "", errJudgeKilled
	}
	return f.answers[i], nil
}

func TestClassify_RetriesAJudgeOnce(t *testing.T) {
	diff := "+++ b/a.go\n" + strings.Repeat("+x := 1\n", 2*tinyDiffLines)
	calls := 0
	good := `{"risk":0,"complexity":0,"reason":"r"}`
	tier, why := Classify(t.Context(), []Judge{flakyJudge{answers: []string{"", good}, calls: &calls}}, diff, []string{"a.go"})
	require.Equal(t, TierSmall.Name, tier.Name, "the retry's answer counts")
	require.Equal(t, 2, calls)
	require.NotContains(t, why, "judges failed")

	calls = 0
	tier, _ = Classify(t.Context(), []Judge{flakyJudge{answers: []string{"", ""}, calls: &calls}}, diff, []string{"a.go"})
	require.Equal(t, TierStandard.Name, tier.Name, "two failures fall back to standard")
	require.Equal(t, 2, calls, "one retry, never a loop")
}

func TestClassify_NamesAFailedJudge(t *testing.T) {
	diff := "+++ b/a.go\n" + strings.Repeat("+x := 1\n", 2*tinyDiffLines)
	_, why := Classify(t.Context(), []Judge{fakeJudge{err: errHTTP401}, judged(0, 0)}, diff, []string{"a.go"})
	require.Contains(t, why, "1 of 2 judges failed")
	require.Contains(t, why, "HTTP 401")
}

func TestReuse_KeepsTheEarlierTierButNotBelowTheFloor(t *testing.T) {
	small := "+++ b/a.go\n+x := 1\n"
	got, ok := Reuse(TierStandard.Name, small, []string{"a.go"})
	require.True(t, ok)
	require.Equal(t, TierStandard.Name, got.Name)

	got, _ = Reuse(TierSmall.Name, small, []string{"infra/cache.tf"})
	require.Equal(t, TierRisky.Name, got.Name, "a file-kind floor still applies on a follow-up")

	_, ok = Reuse("gone", small, []string{"a.go"})
	require.False(t, ok, "an unknown stored tier is not trusted")
}

func TestOnlyDocsUseTheLightReviewer(t *testing.T) {
	require.True(t, TierDocs.Light)
	for _, tier := range []Tier{TierSmall, TierStandard, TierRisky} {
		require.False(t, tier.Light, tier.Name)
	}
}

func TestStepBudget_NamesTheLimit(t *testing.T) {
	require.Contains(t, stepBudget(8), "at most 8 tool steps")
	require.Contains(t, stepBudget(8), "by step 7")
	require.Contains(t, stepBudget(1), "by step 1")
}

func TestPackBudget_ScalesWithTier(t *testing.T) {
	require.NotZero(t, packBudget(TierDocs), "a docs reviewer with no file can run out of steps fetching it")
	require.Less(t, packBudget(TierSmall), packBudget(TierStandard))
	require.Less(t, packBudget(TierStandard), packBudget(TierRisky))
}

func TestTriage(t *testing.T) {
	small := "+++ b/a.go\n+func A() {}\n"

	tests := []struct {
		name  string
		diff  string
		files []string
		want  Tier
	}{
		{"docs only", "+text", []string{"README.md", "docs/guide.go"}, TierDocs},
		{"small code", small, []string{"a.go"}, TierSmall},
		{"medium code", "+++ b/a.go\n" + strings.Repeat("+x := 1\n", 200), []string{"a.go"}, TierStandard},
		{"auth path is judged, not guessed", small, []string{"internal/auth/token.go"}, TierSmall},
		{"terraform", small, []string{"infra/cache.tf"}, TierRisky},
		{"migration", small, []string{"db/migrations/001.sql"}, TierRisky},
		{"proto", small, []string{"api/user.proto"}, TierRisky},
		{"workflow", small, []string{".github/workflows/ci.yml"}, TierRisky},
		{"docs plus code is not docs", small, []string{"README.md", "a.go"}, TierSmall},
		{"auth test file alone is not risky", small, []string{"internal/auth/token_test.go"}, TierSmall},
		{"risky words in a small diff are judged, not guessed", "+\tgo func() { mu.Lock() }()\n", []string{"a.go"}, TierSmall},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want.Name, Triage(tt.diff, tt.files).Name)
		})
	}
}

func TestJuryFor_SimpleReviewsSkipTheJury(t *testing.T) {
	models := []string{"x/second"}
	for _, tier := range []Tier{TierDocs, TierSmall} {
		require.Empty(t, JuryFor(tier, models), tier.Name)
		p := PromptLocal(Local{Base: "main"}, tier, JuryFor(tier, models))
		require.Contains(t, p, "skip the jury", tier.Name)
		require.NotContains(t, p, "x/second", tier.Name)
	}
	for _, tier := range []Tier{TierStandard, TierRisky} {
		require.Equal(t, models, JuryFor(tier, models), tier.Name)
	}
}

func TestJuryFor_StandardGetsOneSecondOpinion(t *testing.T) {
	models := []string{"x/second", "x/third"}
	require.Equal(t, []string{"x/second"}, JuryFor(TierStandard, models))
	require.Equal(t, models, JuryFor(TierRisky, models))
	require.Empty(t, JuryFor(TierStandard, nil))
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
