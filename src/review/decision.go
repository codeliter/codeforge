package review

import (
	"regexp"
	"strings"
)

// Decision is the type of a GitHub review: what the reviewer decides about the change as a whole.
type Decision string

// The decisions GitHub's review API takes as its event.
const (
	DecisionComment        Decision = "COMMENT"
	DecisionApprove        Decision = "APPROVE"
	DecisionRequestChanges Decision = "REQUEST_CHANGES"
	// DecisionPlain posts no GitHub review at all: each finding goes up as a comment on its line, as in a review, and
	// the final comment as an ordinary PR comment, each of which its author can edit or delete on its own. A review
	// never suggests it; only the user picks it.
	DecisionPlain Decision = "PLAIN"
)

// Label is the decision as the user says it.
func (d Decision) Label() string {
	switch d {
	case DecisionApprove:
		return "Approve"
	case DecisionRequestChanges:
		return "Request changes"
	case DecisionPlain:
		return "separate comments (deletable)"
	}
	return "Comment"
}

var decisionLine = regexp.MustCompile(`(?i)^\s*\**\s*decision\s*:?\s*\**\s*(approve|comment|request changes)\s*\.?\s*\**\s*$`)

// IsDecisionLine reports whether line is a Decision line as ParseDecision reads one.
func IsDecisionLine(line string) bool { return decisionLine.MatchString(line) }

// ParseDecision takes the review's closing `Decision: Approve|Comment|Request changes` line off its text. A review with
// no such line is a Comment. A blocking issue in the review always makes it Request changes, whatever the line says, so
// a review can never approve past its own blocker.
func ParseDecision(review string) (Decision, string) {
	lines := strings.Split(strings.TrimRight(review, "\n "), "\n")
	d := DecisionComment
	if n := len(lines); n > 0 {
		if m := decisionLine.FindStringSubmatch(lines[n-1]); m != nil {
			switch strings.ToLower(m[1]) {
			case "approve":
				d = DecisionApprove
			case "request changes":
				d = DecisionRequestChanges
			}
			lines = lines[:n-1]
		}
	}
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	for _, l := range lines {
		if f, ok := ParseLabeled(l); ok && strings.Contains(strings.ToLower(f.Label), "blocking") && !strings.Contains(strings.ToLower(f.Label), "non-blocking") {
			d = DecisionRequestChanges
			break
		}
	}
	return d, text
}
