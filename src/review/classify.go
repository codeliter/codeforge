package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/codeliter/codeforge/assets"
)

const (
	maxJudgedDiffBytes = 12_000
	// 90s: a judge is normally done in 5 to 15s, but a busy machine with reviewers running alongside it can take much longer.
	// A kill at the deadline is a paid call turned into a raised tier, which costs more than waiting.
	classifyTimeout = 90 * time.Second
	maxScore        = 2
	// judgeFloorLines is the diff size from which the judges can no longer lower a review below standard.
	judgeFloorLines = 400
	// tinyDiffLines is the size at or below which one judge is asked instead of all. A small diff can still be the
	// riskiest one (a flipped authorization check is two lines), so it is never classified without a judge.
	tinyDiffLines = 20
)

// Judge sends a system prompt and a user message to a cheap model and returns its raw answer.
type Judge interface {
	Ask(ctx context.Context, system, user string) (string, error)
}

// TriageFunc picks a tier and a short reason for it; the reason may be empty.
type TriageFunc func(ctx context.Context, diff string, files []string) (Tier, string)

// Heuristic is the offline TriageFunc: file kinds and diff size only.
func Heuristic(_ context.Context, diff string, files []string) (Tier, string) {
	return Triage(diff, files), ""
}

type scores struct {
	Risk       int    `json:"risk"`
	Complexity int    `json:"complexity"`
	Reason     string `json:"reason"`
}

func (s scores) valid() bool {
	return s.Risk >= 0 && s.Risk <= maxScore && s.Complexity >= 0 && s.Complexity <= maxScore
}

// Classify sizes a review by asking cheap judges what the change does to the system. Docs-only changes skip the
// judges; infra and schema file kinds are a floor the judges cannot lower. If no judge answers, the review falls
// back to at least the standard tier rather than guessing a simple one.
func Classify(ctx context.Context, judges []Judge, diff string, files []string) (Tier, string) {
	base := Triage(diff, files)
	if base.Name == TierDocs.Name || len(judges) == 0 {
		return base, ""
	}
	// Risky is the top tier and file kinds are a floor the judges cannot lower, so no answer could change the result.
	if base.Name == TierRisky.Name {
		return base, "infrastructure or schema files set the floor, so the judges were skipped"
	}
	if changedLines(diff) <= tinyDiffLines {
		judges = judges[:1]
	}

	// File kinds are a floor; size is only a prior, so judges can bring a mid-sized mechanical diff down to small.
	floor := base
	if floor.Name == TierStandard.Name && changedLines(diff) < judgeFloorLines {
		floor = TierSmall
	}

	ctx, cancel := context.WithTimeout(ctx, classifyTimeout)
	defer cancel()

	msg := judgeMessage(diff, files)
	votes := make([]scores, len(judges))
	ok := make([]bool, len(judges))
	errs := make([]error, len(judges))
	var wg sync.WaitGroup
	for i, j := range judges {
		wg.Go(func() {
			// One retry: a judge that spends its only step on a tool call or is killed by the timeout costs more to
			// replace than to ask again, and a missing vote can raise the tier on its own.
			for attempt := 0; attempt < 2; attempt++ {
				raw, err := j.Ask(ctx, assets.ReviewRubric, msg)
				if err != nil {
					errs[i] = err
				} else if votes[i], ok[i] = parseScores(raw); ok[i] {
					errs[i] = nil
					return
				} else {
					errs[i] = errUnusableAnswer
				}
				if ctx.Err() != nil {
					return
				}
			}
		})
	}
	wg.Wait()

	var risk, complexity, n int
	var reasons []string
	alarm := false
	for i, v := range votes {
		if !ok[i] {
			continue
		}
		risk, complexity, n = risk+v.Risk, complexity+v.Complexity, n+1
		alarm = alarm || v.Risk == maxScore
		if v.Reason != "" {
			reasons = append(reasons, v.Reason)
		}
	}
	if n == 0 {
		return higher(base, TierStandard), "no judge answered (" + firstError(errs) + "); using the standard tier"
	}

	// Rounding up means two judges that split 2 and 1 on risk call it risky. A split of 2 and 0 is a disagreement
	// about something critical, so it earns a second opinion. An ordinary behaviour change (risk 1) stays with the
	// one reviewer the gate always gave small diffs: only an alarm or hard-to-verify logic costs more.
	risk, complexity = (risk+n-1)/n, (complexity+n-1)/n
	tier := TierSmall
	switch {
	case risk == maxScore:
		tier = TierRisky
	case alarm || complexity == maxScore:
		tier = TierStandard
	}
	reason := strings.Join(reasons, " | ")
	if n < len(judges) {
		reason += fmt.Sprintf(" [%d of %d judges failed: %s]", len(judges)-n, len(judges), firstError(errs))
	}
	return higher(floor, tier), reason
}

var errUnusableAnswer = errors.New("answer was not the expected JSON scores")

// maxJudgeError keeps a judge's error short enough to sit in the one-line tier reason.
const maxJudgeError = 160

// firstError names why the judges failed, so a broken judge is visible instead of silently costing standard reviews.
func firstError(errs []error) string {
	for _, err := range errs {
		if err != nil {
			msg := err.Error()
			if len(msg) > maxJudgeError {
				msg = msg[:maxJudgeError] + "…"
			}
			return msg
		}
	}
	return "no reason"
}

// Reuse returns the tier of an earlier round of the same work, raised to the floor the current files and size set.
// A follow-up that only checks fixes does not need the judges to size the work again.
func Reuse(prev, diff string, files []string) (Tier, bool) {
	t, ok := TierByName(prev)
	if !ok {
		return Tier{}, false
	}
	return higher(t, Triage(diff, files)), true
}

func higher(a, b Tier) Tier {
	order := map[string]int{TierDocs.Name: 0, TierSmall.Name: 1, TierStandard.Name: 2, TierRisky.Name: 3} //nolint:mnd // tier order //nolint:mnd // tier order
	if order[b.Name] > order[a.Name] {
		return b
	}
	return a
}

func judgeMessage(diff string, files []string) string {
	if len(diff) > maxJudgedDiffBytes {
		diff = diff[:maxJudgedDiffBytes] + "\n[diff truncated]\n"
	}
	return fmt.Sprintf("<files>\n%s\n</files>\n<diff>\n%s\n</diff>", strings.Join(files, "\n"), diff)
}

func parseScores(raw string) (scores, bool) {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return scores{}, false
	}
	var s scores
	if err := json.Unmarshal([]byte(raw[start:end+1]), &s); err != nil || !s.valid() {
		return scores{}, false
	}
	return s, true
}
