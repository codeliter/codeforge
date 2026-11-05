package review

import (
	"path"
	"regexp"
	"strings"
)

// Tier sizes one review: effort and step budget scale with how much can go wrong, never above high.
// Jury is set for complicated or high-risk changes; simple ones are reviewed by one model alone.
// Light marks tiers where a cheaper reviewer is enough. Only docs: a five-run comparison showed the cheap reviewer
// let through a documented-contract violation on a small code change that the normal reviewer blocked every time.
type Tier struct {
	Name     string
	Effort   string
	MaxSteps int
	Genome   bool
	Jury     bool
	Light    bool
}

const (
	docsSteps     = 4
	smallSteps    = 5
	standardSteps = 14
	riskySteps    = 20
)

var (
	TierDocs     = Tier{Name: "docs", Effort: "low", MaxSteps: docsSteps, Light: true}
	TierSmall    = Tier{Name: "small", Effort: "medium", MaxSteps: smallSteps}
	TierStandard = Tier{Name: "standard", Effort: "medium", MaxSteps: standardSteps, Genome: true, Jury: true}
	TierRisky    = Tier{Name: "risky", Effort: "high", MaxSteps: riskySteps, Genome: true, Jury: true}
)

// JuryFor returns the second-opinion models a review at this tier must consult: none for simple changes,
// one for standard changes, all of them only for risky ones.
func JuryFor(t Tier, models []string) []string {
	switch {
	case !t.Jury:
		return nil
	case t.Name == TierStandard.Name:
		return models[:min(len(models), 1)]
	}
	return models
}

const smallDiffLines = 80

var (
	docExts = map[string]bool{".md": true, ".mdx": true, ".txt": true, ".rst": true, ".adoc": true}

	// Only kinds of file whose purpose is infrastructure or schema. What a code change does is judged by Classify,
	// never guessed from the words in it.
	riskyPath = regexp.MustCompile(`\.(sql|tf|tfvars|proto)$|(^|/)(migrations?|deploy|k8s|helm|terraform|\.github/workflows)/|Dockerfile`)
)

// Triage is the offline fallback tier from file kinds and diff size. Classify refines it with a judge.
func Triage(diff string, files []string) Tier {
	if len(files) > 0 && allDocs(files) {
		return TierDocs
	}
	if anyMatch(nonTests(files), riskyPath) {
		return TierRisky
	}
	if changedLines(diff) <= smallDiffLines {
		return TierSmall
	}
	return TierStandard
}

// nonTests drops test files: a test named after auth or a token does not make the change risky.
func nonTests(files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	return out
}

func allDocs(files []string) bool {
	for _, f := range files {
		if !docExts[strings.ToLower(path.Ext(f))] && !strings.HasPrefix(f, "docs/") {
			return false
		}
	}
	return true
}

func anyMatch(files []string, re *regexp.Regexp) bool {
	for _, f := range files {
		if re.MatchString(f) {
			return true
		}
	}
	return false
}

func changedLines(diff string) int {
	n := 0
	for line := range strings.SplitSeq(diff, "\n") {
		if (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) &&
			!strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
			n++
		}
	}
	return n
}
