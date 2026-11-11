package review

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/codeliter/codeforge/src/text"
)

// coverageCheck matches the CI checks that report test coverage: Codecov, Coveralls, Code Climate and any check or status
// with coverage in its name.
var coverageCheck = regexp.MustCompile(`(?i)codecov|coverage|coveralls|code[ _-]?climate`)

const (
	maxCoverageChecks = 6
	maxCoverageText   = 200
)

// Coverage is what one CI coverage check said about the PR's head: Codecov's "87.98% (-0.05%) compared to 70b8760" or
// "25.00% of diff hit (target 88.02%)". gh pr view leaves this text out, so it is read from the commit's check runs and
// statuses.
type Coverage struct {
	Name, Result, Text string
}

// LoadCoverage reads the coverage checks of the PR's head commit. It never fails a review: a repository without
// coverage in CI, or an API error, gives none.
func LoadCoverage(ctx context.Context, r Runner, repo, sha string) []Coverage {
	if sha == "" {
		return nil
	}
	// Every page of both: a coverage check can come after a hundred others.
	var out []Coverage
	var runs []struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
			Output     struct {
				Title   string `json:"title"`
				Summary string `json:"summary"`
			} `json:"output"`
		} `json:"check_runs"` //nolint:tagliatelle // GitHub's REST field
	}
	if ghPages(ctx, r, repoPath(repo)+"/commits/"+sha+"/check-runs?per_page=100", &runs) == nil {
		for _, page := range runs {
			for _, c := range page.CheckRuns {
				if !coverageCheck.MatchString(c.Name) || hasCoverage(out, c.Name) {
					continue
				}
				if t := checkText(c.Output.Title, c.Output.Summary); t != "" {
					out = append(out, Coverage{Name: c.Name, Result: c.Conclusion, Text: t})
				}
			}
		}
	}
	var statuses [][]struct {
		Context     string `json:"context"`
		State       string `json:"state"`
		Description string `json:"description"`
	}
	if ghPages(ctx, r, repoPath(repo)+"/commits/"+sha+"/statuses?per_page=100", &statuses) == nil {
		seen := map[string]bool{} // newest first: only the latest status of each context counts
		for _, page := range statuses {
			for _, s := range page {
				if seen[s.Context] || !coverageCheck.MatchString(s.Context) {
					continue
				}
				// The latest status decides, even one with nothing to say: an older one's number is out of date.
				seen[s.Context] = true
				// Codecov posts the same result as a check run and a status; it is shown once.
				if strings.TrimSpace(s.Description) != "" && !hasCoverage(out, s.Context) {
					out = append(out, Coverage{Name: s.Context, Result: s.State, Text: s.Description})
				}
			}
		}
	}
	return out[:min(len(out), maxCoverageChecks)]
}

// checkText is what a check run says about coverage: its title when that has a number, such as Codecov's, else the
// first line of its summary that does, else the title.
func checkText(title, summary string) string {
	if strings.Contains(title, "%") {
		return strings.TrimSpace(title)
	}
	for l := range strings.SplitSeq(summary, "\n") {
		if l = strings.TrimSpace(mdLink.ReplaceAllString(l, "$1")); strings.Contains(l, "%") {
			return l
		}
	}
	return strings.TrimSpace(title)
}

// mdLink is a Markdown link, [text](url): its text is kept and its url dropped.
var mdLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

func hasCoverage(cs []Coverage, name string) bool {
	return slices.ContainsFunc(cs, func(c Coverage) bool { return c.Name == name })
}

// HasCoverageCheck reports whether the PR's checks name a coverage check, so a PR without one costs no extra API call.
func (p PR) HasCoverageCheck() bool {
	return slices.ContainsFunc(p.StatusCheckRollup, func(c CheckStatus) bool {
		return coverageCheck.MatchString(c.Name) || coverageCheck.MatchString(c.Context)
	})
}

// coverageSection renders the coverage checks for the brief, inside <pr>: CI wrote them, and the PR's code ran there.
func coverageSection(cs []Coverage) string {
	if len(cs) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString("\ncoverage (from CI; a drop, or changed lines left untested, is evidence for a test finding even when the check passed):\n")
	for _, c := range cs {
		s.WriteString("- " + text.Cut(c.Name, maxCoverageText) + " (" + orNone(strings.ToLower(c.Result)) + "): " +
			text.Cut(strings.Join(strings.Fields(c.Text), " "), maxCoverageText) + "\n")
	}
	return s.String()
}
