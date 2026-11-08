package review

import (
	"slices"
	"strings"
	"time"
)

// Record is what codeforge posted on a pull request for the user: the commit it reviewed, the decision it went up as,
// and each finding with its place and whether it blocked. The posted text carries no labels, so a follow-up round
// learns from the record which of the user's threads were blockers. It is kept in codeforge's session database, one
// row per pull request (see store.UpdateReviewRecord).
type Record struct {
	Repo     string          `json:"repo"`
	Number   string          `json:"number"`
	Head     string          `json:"head"`
	Decision Decision        `json:"decision"`
	At       time.Time       `json:"at"`
	Findings []RecordFinding `json:"findings"`
	// Threads says, by thread id, whether each of the user's open threads blocks: a follow-up round learns it once, and
	// every later round keeps it, wherever the thread's line has moved.
	Threads map[string]bool `json:"threads,omitempty"`
	// Posted holds the GitHub id of every review codeforge posted on this PR for this account. A review's id is its key:
	// a later round tells codeforge's own reviews from ones the user left by hand by it, never by time, and adding an
	// id that is already there changes nothing.
	Posted []int64 `json:"posted,omitempty"`
}

// AddPosted records that codeforge posted review id; posting is recorded once however often it is added.
func (rec *Record) AddPosted(id int64) {
	if id != 0 && !slices.Contains(rec.Posted, id) {
		rec.Posted = append(rec.Posted, id)
	}
}

// RecordFinding is one finding as it was posted.
type RecordFinding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	End      int    `json:"end,omitempty"`
	Label    string `json:"label"`
	Blocking bool   `json:"blocking"`
	Text     string `json:"text"`
}

// RecordOf builds the record of a posted review from its text: every finding it names, with its label.
func RecordOf(repo, number, head string, decision Decision, review string) Record {
	rec := Record{Repo: repo, Number: number, Head: head, Decision: decision, At: time.Now()}
	src := strings.Split(review, "\n")
	for i := 0; i < len(src); i++ {
		f, ok := ParseLabeled(src[i])
		if !ok {
			continue
		}
		rec.Findings = append(rec.Findings, RecordFinding{
			Path: f.Path, Line: f.Line, End: f.End, Label: f.Label, Text: f.Text, Blocking: isBlocking(f.Label),
		})
		i = LabeledEnd(src, i) - 1
	}
	return rec
}

// isBlocking reports whether a Conventional Comments label blocks the change.
func isBlocking(label string) bool {
	l := strings.ToLower(label)
	return strings.Contains(l, "blocking") && !strings.Contains(l, "non-blocking")
}

// FindingAt returns the recorded finding a thread that started on path at line belongs to. When several cover the
// line, a blocking one wins: no finding recorded later may hide an earlier blocker.
func (rec Record) FindingAt(path string, line int) (RecordFinding, bool) {
	var found RecordFinding
	ok := false
	for _, f := range rec.Findings {
		if f.Path != path || (f.Line != line && (f.End <= f.Line || line < f.Line || line > f.End)) {
			continue
		}
		if !ok || (f.Blocking && !found.Blocking) {
			found, ok = f, true
		}
	}
	return found, ok
}
