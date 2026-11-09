package src

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/codeliter/codeforge/src/effort"
	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
)

// A walkthrough is the reviewer's tour of the change, for whoever reads the review and does not know the change yet:
// a short overview, then the places to read in order, each with what that code does and how it fits. It comes from one
// more turn in a fork of the review's session, so it knows what the reviewer read; it is written in the background
// while the user reads the review, and shown with the code of each stop.

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

// walkJob is a walkthrough being written in the background while the user reads the review, so w opens it at once.
type walkJob struct {
	done   chan struct{}
	w      walkthrough
	err    error
	cancel context.CancelFunc
}

// startWalkthrough has the reviewer write the walkthrough in the background, in a fork of the review's session: it
// knows what the review read, and the review's own session stays free for the user's questions and the chat. It is
// bounded by revisionLimit and stopped by stop.
func startWalkthrough(ctx context.Context, argv, env []string) *walkJob {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revisionLimit)
	job := &walkJob{done: make(chan struct{}), cancel: cancel}
	fork, err := forkArgv(argv, walkthroughPrompt())
	if err != nil {
		job.err = err
		close(job.done)
		return job
	}
	go func() {
		defer close(job.done)
		out, err := runFork(ctx, fork, env)
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == exitUsage && ctx.Err() == nil {
			// A flag the review ran with may not suit a fork: once more with only what a turn needs, as reviewTurn does.
			out, err = runFork(ctx, minimalFork(argv, walkthroughPrompt()), env)
		}
		if err != nil {
			job.err = fmt.Errorf("the reviewer could not write it: %w", err)
			return
		}
		reply, err := review.EngineOutput(out)
		if err == nil {
			job.w, err = parseWalkthrough(reply)
		}
		job.err = err
	}()
	return job
}

// runFork runs one forked turn and returns what the engine printed; a failure ends with the engine's own reason.
func runFork(ctx context.Context, argv, env []string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env, cmd.Stderr = env, &stderr
	effort.Bound(cmd) // the engine and all it starts die together when the job stops
	out, err := cmd.Output()
	effort.Reap(cmd)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, lastLine(stderr.String()))
	}
	return out, nil
}

// minimalFork is a fork with only the flags a turn needs: the review's sandbox, profile, mode and model.
func minimalFork(argv []string, prompt string) []string {
	wrapper, engine := sandboxPrefix(argv)
	out := slices.Concat(wrapper, engine[:1], []string{"--fork", cmp.Or(flagValue(argv, "--session-id"), flagValue(argv, "--session")), "--headless", "--json"})
	for _, f := range []string{"--profile", "--mode", "--model"} {
		if v := flagValue(argv, f); v != "" {
			out = append(out, f, v)
		}
	}
	return append(out, prompt)
}

// walkthroughStarter starts the walkthrough for a review; tests whose engines record each call replace it.
var walkthroughStarter = startWalkthrough

// stop ends the background turn if it is still running.
func (j *walkJob) stop() { j.cancel() }

// forkArgv is the review's command line turned into a turn in a fork of its session: the sandbox it ran in and its
// flags kept, its session named with --fork, and prompt in place of the review's own.
func forkArgv(argv []string, prompt string) ([]string, error) {
	id := cmp.Or(flagValue(argv, "--session-id"), flagValue(argv, "--session"))
	wrapper, engine := sandboxPrefix(argv)
	if id == "" || len(engine) == 0 {
		return nil, errSessionMissing
	}
	args := withoutPrompt(dropFlag(dropFlag(engine[1:], "--session-id"), "--session"))
	args = slices.DeleteFunc(args, func(a string) bool { return a == "--save-session" }) // the fork is thrown away
	return slices.Concat(wrapper, engine[:1], []string{"--fork", id}, args, []string{prompt}), nil
}

// showWalkthrough shows the walkthrough job writes, waiting for it when it is not done yet. A failure is said on the
// terminal, and the review opens again as it was.
func showWalkthrough(ctx context.Context, job *walkJob) {
	select {
	case <-job.done:
	default:
		fmt.Fprintln(os.Stderr, "codeforge: the reviewer is finishing the walkthrough...")
		<-job.done
	}
	if job.err != nil {
		fmt.Fprintln(os.Stderr, "codeforge: no walkthrough: "+job.err.Error()+".")
		return
	}
	st := codeStyleFor(ctx, os.Stdout)
	a := walkthroughView(job.w, st, reviewCode(ctx, st, walkthroughText(job.w)))
	if _, _, err := showAccordion("walkthrough", a); err != nil && !errors.Is(err, forms.ErrAborted) {
		fmt.Fprintln(os.Stderr, "codeforge: could not open the walkthrough:", err)
	}
}
