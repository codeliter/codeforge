package src

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/codeliter/codeforge/src/review"
)

// longComment is one comment of a review that runs past its limit.
type longComment struct {
	Place string // the finding's path:line, "" for the final comment
	Words int
}

// longComments lists the comments of review that run past their limit: each finding as posted (its text after the
// place, with its continuation lines, but not a code suggestion), and the final comment.
func longComments(text string) []longComment {
	lines := reviewLines(text)
	spans := findingSpans(lines)
	var long []longComment
	inSpan := map[int]bool{}
	for _, s := range spans {
		_, first, _ := strings.Cut(s.Raw, " — ")
		words := len(strings.Fields(first)) + proseWords(lines[s.Start+1:s.End])
		if words > maxFindingWords {
			long = append(long, longComment{Place: s.Place(), Words: words})
		}
		for i := s.Start; i < s.End; i++ {
			inSpan[i] = true
		}
	}
	var rest []string
	for i, l := range lines {
		if !inSpan[i] && !review.IsDecisionLine(l) {
			rest = append(rest, l)
		}
	}
	if words := proseWords(rest); words > maxFinalWords {
		long = append(long, longComment{Words: words})
	}
	return long
}

// proseWords counts the words in lines, leaving out fenced code: a suggestion block is code, not reading.
func proseWords(lines []string) int {
	n, fenced := 0, false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
			continue
		}
		if !fenced {
			n += len(strings.Fields(l))
		}
	}
	return n
}

// tightenReview asks the reviewer, in its own session, to cut the comments that run past their limit, once. The cut
// review is kept only when it still has every finding at its place and is shorter; otherwise, or when the turn fails,
// the review is kept as it was. clean tidies the reply as the first one was tidied.
func tightenReview(ctx context.Context, text string, turn func(context.Context, string) (string, error), clean func(string) string) string {
	long := longComments(text)
	if len(long) == 0 {
		return text
	}
	reply, err := turn(ctx, tightenPrompt(text, long))
	if err != nil {
		return text
	}
	revised := strings.TrimSpace(reply)
	if clean != nil {
		revised = clean(revised)
	}
	if revised == "" || len(strings.Fields(revised)) >= len(strings.Fields(text)) || !sameFindings(text, revised, long) {
		return text
	}
	// The suggested decision follows the blocking labels; a cut never changes it.
	if before, _ := review.ParseDecision(text); before != func() review.Decision { d, _ := review.ParseDecision(revised); return d }() {
		return text
	}
	return revised
}

// sameFindings reports whether after has the findings of before, in order, at the same places with the same labels, and
// every finding not named in long word for word.
func sameFindings(before, after string, long []longComment) bool {
	a, b := findingSpans(reviewLines(before)), findingSpans(reviewLines(after))
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Place() != b[i].Place() || !strings.EqualFold(a[i].F.Label, b[i].F.Label) {
			return false
		}
		cut := slices.ContainsFunc(long, func(c longComment) bool { return c.Place == a[i].Place() })
		if !cut && strings.Join(reviewLines(before)[a[i].Start:a[i].End], "\n") != strings.Join(reviewLines(after)[b[i].Start:b[i].End], "\n") {
			return false
		}
	}
	// The final comment, unless it was asked to be cut, stays word for word too.
	if !slices.ContainsFunc(long, func(c longComment) bool { return c.Place == "" }) &&
		strings.Join(strings.Fields(finalComment(before)), " ") != strings.Join(strings.Fields(finalComment(after)), " ") {
		return false
	}
	return true
}

// finalComment is the review without its findings and its Decision line.
func finalComment(text string) string {
	lines := reviewLines(text)
	in := map[int]bool{}
	for _, s := range findingSpans(lines) {
		for i := s.Start; i < s.End; i++ {
			in[i] = true
		}
	}
	var rest []string
	for i, l := range lines {
		if !in[i] && !review.IsDecisionLine(l) {
			rest = append(rest, l)
		}
	}
	return strings.Join(rest, "\n")
}

// tightenPrompt names the comments to cut and how far.
func tightenPrompt(text string, long []longComment) string {
	var b strings.Builder
	b.WriteString("Some comments in your review are too long for the author to take in on one read:\n")
	for _, c := range long {
		if c.Place == "" {
			fmt.Fprintf(&b, "- the final comment: %d words; keep it under %d\n", c.Words, targetFinalWords)
			continue
		}
		fmt.Fprintf(&b, "- the finding at `%s`: %d words; keep it under %d\n", c.Place, c.Words, targetFindingWords)
	}
	b.WriteString("\nCut them to what is wrong, why it matters and the change you want, in my voice as codeforge-persona " +
		"describes it, and as plain and unambiguous as before: drop walkthroughs, second examples and restated code, never " +
		"the ask. Change nothing else: keep every finding, its label and its place, the other comments word for word, and " +
		"the Decision line. Reply with the complete review in the same format and nothing else.\n\nThe review:\n\n" + text)
	return b.String()
}

// leadIDPrefix opens the id the view knows a lead by.
const leadIDPrefix = "lead:"
