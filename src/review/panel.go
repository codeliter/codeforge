package review

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Panel runs the jurors from codeforge, in parallel, each on its own clock, instead of asking the reviewing model to
// dispatch them inside its own timed run. Their findings go to the reviewer as evidence to verify: a multi-review
// aggregator that drops findings it cannot confirm is what lifts review quality (SWR-Bench, arXiv:2509.01494), and
// false positives are the weakness it fixes, so nothing here blocks on a juror's word alone.
type Panel struct {
	Repo      string
	Reviewers []Reviewer
}

// Run asks every juror for a verdict on brief until the window ends and renders what came back for the reviewer's
// prompt. It returns "" when there is nobody to ask.
func (p Panel) Run(ctx context.Context, brief string, window time.Duration) string {
	if len(p.Reviewers) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	res, err := Jury(ctx, p.Repo, brief, p.Reviewers, JuryOptions{})
	return Render(res, err)
}

// Render lists the jurors' findings for the reviewer. A juror that returned nothing is named, so the reviewer knows
// the panel was not complete.
func Render(res JuryResult, err error) string {
	var s strings.Builder
	s.WriteString("Independent jurors reviewed this change in parallel before you. Their findings are leads, not verdicts: ")
	s.WriteString("check each against the code, keep the ones that hold and drop the ones you can disprove. This replaces any instruction above to dispatch a jury: do not dispatch jurors yourself.\n")
	n := 0
	for _, v := range res.Votes {
		if v.Err != nil || v.Verdict.Verdict == "" {
			fmt.Fprintf(&s, "- %s: no verdict in time\n", v.Reviewer)
			continue
		}
		fmt.Fprintf(&s, "- %s: %s. %s\n", v.Reviewer, v.Verdict.Verdict, v.Verdict.Summary)
		for _, f := range v.Verdict.Blocking {
			fmt.Fprintf(&s, "  blocking %s:%d %s\n", f.File, f.Line, f.What)
			n++
		}
		for _, f := range v.Verdict.Should {
			fmt.Fprintf(&s, "  should %s:%d %s\n", f.File, f.Line, f.What)
		}
	}
	if n == 0 && err != nil {
		s.WriteString("The panel could not reach a quorum; review the change fully yourself.\n")
	}
	return s.String()
}
