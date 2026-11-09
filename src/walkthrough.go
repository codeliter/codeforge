package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/codeliter/codeforge/src/forms"
)

// A walkthrough is the reviewer's tour of the change, for whoever reads the review and does not know the change yet:
// a short overview, then the places to read in order, each with what that code does and how it fits. It comes from one
// more turn of the review's own session, so it knows what the reviewer read, and is shown with the code of each stop.

// keyWalk opens the walkthrough from the review view.
const keyWalk = "w"

// maxWalkStops bounds the stops a walkthrough asks for.
const maxWalkStops = 12

// walkStop is one place in a walkthrough.
type walkStop struct {
	Path      string
	Line, End int
	Text      string
	// Requirement is what the PR description or its ticket asks of this code, Status whether it does it (met, partly
	// met or not met) and Why the reason; all empty when no stated requirement is about this code.
	Requirement, Status, Why string
}

// walkthrough is the reviewer's tour of the change.
type walkthrough struct {
	Overview string
	Stops    []walkStop
	// Uncovered lists the stated requirements no stop implements.
	Uncovered string
}

// requirementTail is the clause a stop's note ends with when a stated requirement is about its code:
// "Requirement: <what is asked> — met|partly met|not met[: why]".
var requirementTail = regexp.MustCompile(`(?i)\s*\brequirement:\s*(.+?)\s*[—–-]+\s*(met|partly met|not met)\b[:.,]?\s*(.*)$`)

// splitRequirement takes the requirement clause off a stop's note.
func splitRequirement(s *walkStop) {
	m := requirementTail.FindStringSubmatchIndex(s.Text)
	if m == nil {
		return
	}
	s.Requirement = strings.TrimSpace(s.Text[m[2]:m[3]])
	s.Status = strings.ToLower(s.Text[m[4]:m[5]])
	s.Why = strings.TrimSpace(strings.TrimSuffix(s.Text[m[6]:m[7]], "."))
	s.Text = strings.TrimSpace(s.Text[:m[0]])
}

// walkStopLine is one stop as the reviewer lists it: a list marker or number, the place (path:line or path:line-end, in
// backticks or not), a dash, and the note.
var walkStopLine = regexp.MustCompile("^\\s*(?:[-*]|\\d+[.)])\\s+`?([^\\s`:]+):(\\d+)(?:-(\\d+))?`?\\s*[—–:-]+\\s*(.+)$")

// errNoWalkthrough is returned when the reviewer's answer lists no stop.
var errNoWalkthrough = errors.New("the reviewer sent no walkthrough")

// parseWalkthrough reads the reviewer's answer: the prose before the first stop is the overview, every stop line a
// stop, and a line that continues a stop's note joins it.
func parseWalkthrough(reply string) (walkthrough, error) {
	var w walkthrough
	var overview []string
	for _, l := range reviewLines(reply) {
		if m := walkStopLine.FindStringSubmatch(l); m != nil && validRepoPath(m[1]) {
			line, err := strconv.Atoi(m[2])
			if err == nil {
				end, err := strconv.Atoi(m[3])
				if err != nil {
					end = 0 // no range: the stop is one line
				}
				w.Stops = append(w.Stops, walkStop{Path: m[1], Line: line, End: end, Text: strings.TrimSpace(m[4])})
				continue
			}
		}
		t := strings.TrimSpace(l)
		if rest, ok := cutPrefixFold(t, "not covered:"); ok {
			if rest = strings.TrimSpace(rest); !strings.EqualFold(strings.TrimSuffix(rest, "."), "none") {
				w.Uncovered = rest
			}
			continue
		}
		switch {
		case t == "" || strings.HasPrefix(t, "#"):
		case len(w.Stops) > 0:
			last := &w.Stops[len(w.Stops)-1]
			last.Text += " " + t
		default:
			overview = append(overview, t)
		}
	}
	if len(w.Stops) == 0 {
		return walkthrough{}, errNoWalkthrough
	}
	for i := range w.Stops {
		splitRequirement(&w.Stops[i])
	}
	w.Overview = strings.Join(overview, " ")
	return w, nil
}

// cutPrefixFold is strings.CutPrefix ignoring case.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// walkthroughPrompt asks the reviewer, in its own session, for the tour.
func walkthroughPrompt() string {
	return "Walk me through this change so I can get familiar with it before I read your review. This is not the " +
		"review: no findings, no judgement, no Decision line. Start with two or three plain sentences on what the change " +
		"does and why. Then list the places to read, in the order that makes the change easy to follow (where it starts, " +
		fmt.Sprintf("then what it calls), at most %d, each on one line as `- path:line — note`, citing a line the change ", maxWalkStops) +
		"adds or changes. Each note says in one or two sentences what that code does and how it fits the rest, in plain " +
		"words. When the PR description or its ticket states a requirement this code is for, end the note with " +
		"`Requirement: <what is asked> — met|partly met|not met: <why, in a few words>`, judged from the code, not from " +
		"what the description claims. After the list, one line `Not covered: <stated requirements no stop implements>`, " +
		"or `Not covered: none`. With no stated requirements, leave both out. Reply with the overview and the list only."
}

// walkthroughView is the walkthrough as the accordion shows it: the overview on top, then each stop with its note and
// its code, the first one open.
func walkthroughView(w walkthrough, st codeStyle, code codeSource) forms.Accordion {
	body := st
	if body.wrap {
		body.width = max(minCodeWidth, body.width-len(accordionIndent))
	}
	a := forms.Accordion{
		Intro: st.paint(st.pal.bold, "Walkthrough") + st.paint(st.pal.dim, "  q goes back to the review") + "\n" +
			strings.TrimRight(wrapSpans(st, max(0, st.width-len(accordionIndent)), "", "", inlineSpans(w.Overview, st)...), "\n"),
		Accent: st.pal.accent, Dim: st.pal.dim,
	}
	if w.Uncovered != "" {
		a.Intro += "\n" + strings.TrimRight(wrapSpans(st, max(0, st.width-len(accordionIndent)), "", "",
			append([]span{{text: "Not covered by the change: ", code: st.pal.issue}}, inlineSpans(w.Uncovered, st)...)...), "\n")
	}
	for i, s := range w.Stops {
		place := fmt.Sprintf("%s:%d", s.Path, s.Line)
		if s.End > s.Line {
			place += fmt.Sprintf("-%d", s.End)
		}
		text := strings.TrimRight(wrapSpans(body, max(0, body.width), "", "", inlineSpans(s.Text, body)...), "\n")
		summary := firstWords(s.Text, walkSummaryWords)
		if s.Requirement != "" {
			// Whether this code does what was asked of it, said beside the stop, its colour on the closed row too.
			verdict := st.paint(requirementColor(s.Status, st), s.Status)
			line := []span{{text: "Requirement: ", code: st.pal.dim}}
			line = append(line, inlineSpans(s.Requirement, body)...)
			status := span{text: s.Status, code: requirementColor(s.Status, st)}
			if s.Why != "" {
				status.text += ":" // the colon sits on the status, so no space comes between them
			}
			line = append(line, span{text: " — "}, status)
			line = append(line, inlineSpans(s.Why, body)...)
			text += "\n" + strings.TrimRight(wrapSpans(body, max(0, body.width), "", "", line...), "\n")
			summary = verdict + st.paint(st.pal.dim, " · ") + summary
		}
		if code != nil {
			if snippet := strings.Trim(code(s.Path, focus{Line: s.Line, End: s.End, Hints: inlineNames(s.Text)}), "\n"); snippet != "" {
				text += "\n\n" + snippet
			}
		}
		a.Items = append(a.Items, forms.Section{
			Title:   st.paint(st.pal.accent, fmt.Sprintf("%d. %s", i+1, place)),
			Summary: summary,
			Body:    text,
			Open:    i == 0,
		})
	}
	return a
}

// requirementColor colours a requirement's status: met as praise, not met as an issue, partly met as a suggestion.
func requirementColor(status string, st codeStyle) string {
	switch status {
	case "met":
		return st.pal.praise
	case "not met":
		return st.pal.issue
	}
	return st.pal.suggestion
}

// walkSummaryWords is how much of a stop's note its closed row shows.
const walkSummaryWords = 8

// firstWords is the first n words of s, marked when cut.
func firstWords(s string, n int) string {
	words := strings.Fields(strings.ReplaceAll(s, "`", ""))
	if len(words) <= n {
		return strings.Join(words, " ")
	}
	return strings.Join(words[:n], " ") + "…"
}

// inlineNames lists the names a note puts in backticks, so its code snippet marks the lines that hold them.
func inlineNames(text string) []string {
	var names []string
	for _, m := range inlineCode.FindAllStringSubmatch(text, -1) {
		names = append(names, m[1])
	}
	return names
}

// walkthroughText lists the stops as findings, so reviewCode fetches the files they are in.
func walkthroughText(w walkthrough) string {
	var b strings.Builder
	for _, s := range w.Stops {
		fmt.Fprintf(&b, "- fyi: %s:%d — x\n", s.Path, s.Line)
	}
	return b.String()
}

// showWalkthrough asks the reviewer for the walkthrough, once per review (cached in *w), and shows it. A failure is
// said on the terminal and the review opens again as it was.
func showWalkthrough(ctx context.Context, w **walkthrough, turn func(context.Context, string) (string, error)) {
	if *w == nil {
		fmt.Fprintln(os.Stderr, "codeforge: the reviewer is writing a walkthrough of the change...")
		reply, err := turn(ctx, walkthroughPrompt())
		if err == nil {
			var got walkthrough
			if got, err = parseWalkthrough(reply); err == nil {
				*w = &got
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "codeforge: no walkthrough: "+err.Error()+".")
			return
		}
	}
	st := codeStyleFor(ctx, os.Stdout)
	a := walkthroughView(**w, st, reviewCode(ctx, st, walkthroughText(**w)))
	if _, _, err := showAccordion("walkthrough", a); err != nil && !errors.Is(err, forms.ErrAborted) {
		fmt.Fprintln(os.Stderr, "codeforge: could not open the walkthrough:", err)
	}
}
