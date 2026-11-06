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
// false positives are the weakness it fixes. A juror's blocking finding the reviewer cannot disprove stays blocking, marked unverified.
type Panel struct {
	Repo      string
	Reviewers []Reviewer
}

// Run asks every juror for a verdict on brief until the window ends and renders what came back for the reviewer's
// prompt. It returns "" when there is nobody to ask.
func (p Panel) Run(ctx context.Context, brief string, window time.Duration) string {
	text, _ := p.Collect(ctx, brief, window)
	return text
}

// Collect is Run that also counts the blocking findings that came back.
func (p Panel) Collect(ctx context.Context, brief string, window time.Duration) (string, int) {
	if len(p.Reviewers) == 0 {
		return "", 0
	}
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	res, err := Jury(ctx, p.Repo, brief, p.Reviewers, JuryOptions{})
	return renderPanel(res, err)
}

// Render lists the jurors' findings for the reviewer. A juror that returned nothing is named, so the reviewer knows
// the panel was not complete.
func Render(res JuryResult, err error) string {
	text, _ := renderPanel(res, err)
	return text
}

func renderPanel(res JuryResult, err error) (string, int) {
	var s strings.Builder
	s.WriteString("Independent jurors reviewed this change in parallel with each other before you. Their findings are leads, not verdicts: ")
	s.WriteString("check each against the code and keep the ones that hold. A blocking lead you cannot disprove stays a blocking finding, marked unverified, and the change is not approved while one stands; list their should-fix leads as unverified with path:line if time is short. This replaces any instruction above to dispatch a jury: do not dispatch jurors yourself.\n")
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
	return s.String(), n
}
