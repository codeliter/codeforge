package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
)

// keyPreview opens the preview of what posting would put on the pull request.
const keyPreview = "v"

// previewView is a preview as the accordion shows it, laid out as GitHub shows a review: its body first, open, then
// each comment under its place, with the code it sits on.
func previewView(p review.Preview, decision review.Decision, st codeStyle, code codeSource) forms.Accordion {
	body := st
	if body.wrap {
		body.width = max(minCodeWidth, body.width-len(accordionIndent))
	}
	a := forms.Accordion{
		Intro: st.paint(st.pal.bold, "Preview") + st.paint(st.pal.dim, "  what goes on the PR when you post; q goes back to the review") + "\n" +
			st.paint(st.pal.dim, "As the decision you pick (suggested: ") + st.paint(decisionColor(decision, st), decision.Label()) + st.paint(st.pal.dim, ")."),
		Accent: st.pal.accent, Dim: st.pal.dim,
	}
	a.Items = append(a.Items, forms.Section{
		Title: st.paint(st.pal.bold, "Review body"),
		Body:  strings.Trim(renderReview(p.Body, body, nil), "\n"),
		Open:  true,
	})
	comment := func(ic review.InlineComment, where string) forms.Section {
		text := strings.Trim(renderReview(ic.Body, body, nil), "\n")
		if code != nil {
			if snippet := strings.Trim(code(ic.Path, focus{Line: ic.Line, End: ic.Line}), "\n"); snippet != "" {
				text = snippet + "\n\n" + text
			}
		}
		return forms.Section{Title: st.paint(st.pal.accent, ic.Place()), Summary: st.paint(st.pal.dim, where), Body: text, Open: true}
	}
	for _, ic := range p.Lines {
		a.Items = append(a.Items, comment(ic, "on the line"))
	}
	for _, ic := range p.OnFile {
		a.Items = append(a.Items, comment(ic, "on the file: its line is not in the diff"))
	}
	return a
}

// showPreview shows what posting text and leads would put on the pull request. A failure is said on the terminal.
func showPreview(ctx context.Context, post *review.PostRequest, text, leads string, decision review.Decision) {
	p, err := post.Preview(text, leads)
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge: no preview: "+err.Error()+".")
		return
	}
	st := codeStyleFor(ctx, os.Stdout)
	var all []string
	for _, ic := range append(p.Lines, p.OnFile...) {
		all = append(all, fmt.Sprintf("- fyi: %s:%d — x", ic.Path, ic.Line))
	}
	a := previewView(p, decision, st, reviewCode(ctx, st, strings.Join(all, "\n")))
	if _, _, err := showAccordion("preview", a); err != nil && !errors.Is(err, forms.ErrAborted) {
		fmt.Fprintln(os.Stderr, "codeforge: could not open the preview:", err)
	}
}
