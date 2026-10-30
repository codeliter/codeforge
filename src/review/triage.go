package review

import (
	"path"
	"regexp"
	"strings"
)

// Tier sizes one review: effort and step budget scale with how much can go wrong, never above high.
type Tier struct {
	Name     string
	Effort   string
	MaxSteps int
	Genome   bool
}

const (
	docsSteps     = 4
	smallSteps    = 8
	standardSteps = 14
	riskySteps    = 20
)

var (
	TierDocs     = Tier{Name: "docs", Effort: "low", MaxSteps: docsSteps}
	TierSmall    = Tier{Name: "small", Effort: "medium", MaxSteps: smallSteps}
	TierStandard = Tier{Name: "standard", Effort: "medium", MaxSteps: standardSteps, Genome: true}
	TierRisky    = Tier{Name: "risky", Effort: "high", MaxSteps: riskySteps, Genome: true}
)

const (
	smallDiffLines = 80
	largeDiffLines = 600
)

var (
	docExts = map[string]bool{".md": true, ".mdx": true, ".txt": true, ".rst": true, ".adoc": true}

	riskyPath = regexp.MustCompile(`(?i)(auth|secur|crypt|token|secret|password|session|permission|acl|migrat|schema|payment|billing)` +
		`|\.(sql|tf|tfvars|proto)$|(^|/)(deploy|k8s|helm|terraform|\.github/workflows)/|Dockerfile|jsonnet`)

	riskyCode = regexp.MustCompile(`(?m)^\+.*(\bgo func\b|sync\.|Mutex|atomic\.|exec\.Command|os/exec|unsafe\.|sql\.|\bDB\b|` +
		`password|secret|token|crypto/|tls\.|http\.Client|context\.WithoutCancel|DELETE FROM|DROP |ALTER TABLE|rm -rf)`)
)

// Triage picks the cheapest tier that still covers what the change can break.
func Triage(diff string, files []string) Tier {
	if len(files) > 0 && allDocs(files) {
		return TierDocs
	}
	if anyMatch(files, riskyPath) || riskyCode.MatchString(diff) {
		return TierRisky
	}

	switch n := changedLines(diff); {
	case n <= smallDiffLines:
		return TierSmall
	case n >= largeDiffLines:
		return TierRisky
	default:
		return TierStandard
	}
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
