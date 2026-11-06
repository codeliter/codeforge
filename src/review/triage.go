package review

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// maxTimedSteps and maxTimedEffort bound a review that has a clock: a long thinking budget or a deep tool loop is how
// a review runs out of time without a verdict.
const (
	maxTimedSteps  = 14
	maxTimedEffort = "medium"
)

// Timed returns the tier a time-boxed review runs at: the same files and jury, but a shallower loop.
func (t Tier) Timed() Tier {
	t.MaxSteps = min(t.MaxSteps, maxTimedSteps)
	if t.Effort == "high" {
		t.Effort = maxTimedEffort
	}
	return t
}

// Panel returns t without its jury wording. The panel is run by codeforge, in parallel, so the brief must not tell the
// model to dispatch a jury of its own.
func (t Tier) Panel() Tier {
	t.Name, t.Jury = "", false
	return t
}

// Limits are the Empryo flags that bound one review run: a step budget and a wall-clock timeout in milliseconds.
func (t Tier) Limits(left time.Duration) []string {
	return []string{"--max-steps", strconv.Itoa(t.MaxSteps), "--timeout", strconv.FormatInt(left.Milliseconds(), 10)}
}

// LimitNote is the standing time rule of every review session, the interactive one that follows included.
func LimitNote() string {
	return fmt.Sprintf("The review run must be complete within %d seconds of wall clock, and a run that is slower is killed with nothing saved; the interactive session that follows is not timed. "+
		"Work from the diff and files already in the brief, read more only for a finding that depends on it. Give the verdict first, then every blocking issue as soon as you find it; foresight, suggestions and nits come last and are dropped when time runs out.",
		int(AgentLimit/time.Second))
}

// WithBudget tells the reviewer its step budget and how long it has, so it answers before it is cut off.
func WithBudget(prompt string, t Tier, left time.Duration) string {
	return prompt + stepBudget(t.MaxSteps) + "\n\n" + TimeNote(left)
}

// TimeNote tells a reviewer how long it has, so it can plan its steps and answer before the clock ends.
func TimeNote(left time.Duration) string {
	return fmt.Sprintf("Time limit: you have %d seconds of wall clock for this whole review, and a run that exceeds it is killed with nothing saved. "+
		"Work from the diff and files already in the brief, read more only for a finding that depends on it. Give the verdict first, then every blocking issue as soon as you find it; foresight, suggestions and nits come last and are dropped when time runs out.",
		int(left/time.Second))
}

const (
	docsSteps     = 4
	smallSteps    = 5
	standardSteps = 20
	riskySteps    = 30
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
