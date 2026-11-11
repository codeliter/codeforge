package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var errNoAPI = errors.New("no such call")

type ghAPIFake map[string]string

func (f ghAPIFake) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if out, ok := f[strings.Join(args, " ")]; ok {
		return []byte(out), nil
	}
	return nil, errNoAPI
}

func TestLoadCoverage_ReadsCodecovChecksAndStatuses(t *testing.T) {
	r := ghAPIFake{
		// Two pages: the coverage checks come after the first page's unrelated ones.
		"api --paginate --slurp repos/acme/w/commits/abc/check-runs?per_page=100": `[{"check_runs":[
{"name":"build","conclusion":"success","output":{"title":"ok"}}]},{"check_runs":[
{"name":"codecov/patch","conclusion":"success","output":{"title":"25.00% of diff hit (target 88.02%)"}},
{"name":"codecov/project","conclusion":"success","output":{"title":"87.98% (-0.05%) compared to 70b8760"}},
{"name":"Coverage report","conclusion":"neutral","output":{"title":"Coverage report","summary":"[Details](https://x)\nTotal: 71.4% (-0.8%)"}}]}]`,
		"api --paginate --slurp repos/acme/w/commits/abc/statuses?per_page=100": `[[{"context":"EasyCLA","state":"success","description":"passed"}],
[{"context":"coverage/coveralls","state":"failure","description":"Coverage decreased (-1.2%) to 80.1%"},
{"context":"coverage/coveralls","state":"pending","description":"Coverage pending (older)"}]]`,
	}
	got := LoadCoverage(t.Context(), r, "acme/w", "abc")
	require.Equal(t, []Coverage{
		{Name: "codecov/patch", Result: "success", Text: "25.00% of diff hit (target 88.02%)"},
		{Name: "codecov/project", Result: "success", Text: "87.98% (-0.05%) compared to 70b8760"},
		{Name: "Coverage report", Result: "neutral", Text: "Total: 71.4% (-0.8%)"},
		{Name: "coverage/coveralls", Result: "failure", Text: "Coverage decreased (-1.2%) to 80.1%"},
	}, got, "every page is read, a summary gives the number a title lacks, and only a context's latest status counts")

	s := coverageSection(got)
	require.Contains(t, s, "- codecov/patch (success): 25.00% of diff hit (target 88.02%)\n")
	require.Contains(t, s, "even when the check passed")
}

func TestLoadCoverage_OneEndpointFailingKeepsTheOtherAndTheListIsCapped(t *testing.T) {
	var many strings.Builder
	many.WriteString(`[{"check_runs":[`)
	for i := range 9 {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"name":"codecov/flag` + strings.Repeat("x", i) + `","conclusion":"success","output":{"title":"80%"}}`)
	}
	many.WriteString(`]}]`)
	r := ghAPIFake{
		"api --paginate --slurp repos/acme/w/commits/abc/check-runs?per_page=100": many.String(),
		"api --paginate --slurp repos/acme/w/commits/abc/statuses?per_page=100":   `not json`,
	}
	require.Len(t, LoadCoverage(t.Context(), r, "acme/w", "abc"), maxCoverageChecks)
}

func TestLoadCoverage_TheLatestStatusDecidesAndEachResultIsShownOnce(t *testing.T) {
	r := ghAPIFake{
		"api --paginate --slurp repos/acme/w/commits/abc/check-runs?per_page=100": `[{"check_runs":[
{"name":"codecov/project","conclusion":"success","output":{"title":"88%"}},
{"name":"Code Climate","conclusion":"neutral","output":{"title":"Coverage","summary":"Coverage: [71.4%](https://x) ([details](https://y))"}}]}]`,
		"api --paginate --slurp repos/acme/w/commits/abc/statuses?per_page=100": `[[{"context":"codecov/project","state":"success","description":"88%"},
{"context":"code-climate/coverage","state":"pending","description":null}],
[{"context":"code-climate/coverage","state":"success","description":"90% (old)"}]]`,
	}
	require.Equal(t, []Coverage{
		{Name: "codecov/project", Result: "success", Text: "88%"},
		{Name: "Code Climate", Result: "neutral", Text: "Coverage: 71.4% (details)"},
	}, LoadCoverage(t.Context(), r, "acme/w", "abc"),
		"a check run and a status of the same name count once, a pending status hides the older number, and links keep their text")
}

func TestPR_HasCoverageCheck(t *testing.T) {
	require.True(t, PR{StatusCheckRollup: []CheckStatus{{Context: "coverage/coveralls"}}}.HasCoverageCheck())
	require.True(t, PR{StatusCheckRollup: []CheckStatus{{Name: "codecov/patch"}}}.HasCoverageCheck())
	require.False(t, PR{StatusCheckRollup: []CheckStatus{{Name: "build"}}}.HasCoverageCheck(), "no coverage check, no extra API call")
}

func TestLoadCoverage_NoneWithoutCoverageInCI(t *testing.T) {
	require.Empty(t, LoadCoverage(t.Context(), ghAPIFake{}, "acme/w", "abc"), "an API error gives none and never fails the review")
	require.Empty(t, LoadCoverage(t.Context(), ghAPIFake{}, "acme/w", ""))
	require.Empty(t, coverageSection(nil))
}

func TestPrompt_CarriesCoverageInsideThePR(t *testing.T) {
	p := Prompt(Brief{Coverage: []Coverage{{Name: "codecov/project", Result: "SUCCESS", Text: "80% (-2%)"}}}, Tier{}, nil)
	cov := strings.Index(p, "- codecov/project (success): 80% (-2%)")
	require.Positive(t, cov)
	require.Less(t, cov, strings.Index(p, "</pr>"), "CI's text is data, inside the fence")
}
