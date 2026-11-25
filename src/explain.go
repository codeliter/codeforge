package src

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
)

// codeforge explain is the tour of the user's own change, written by the agent that made it: for work the user must
// answer for and did not type. What is explained is a session, not a branch: the work or ticket session the user names
// or picks among those started in this directory. It comes from one turn in a fork of that session, read-only, so it knows
// why each choice was made; it is shown as a walkthrough is, with what a reader of their own change needs besides: the
// choices they might have made differently, what went beyond the task, what was not verified, and a few questions to
// check they could defend it.

const (
	maxDecisions   = 6
	checkQuestions = 4
	explainLimit   = 5 * time.Minute
)

// explanation is the agent's tour of the change it made.
type explanation struct {
	walkthrough
	// Decisions are the choices the user might have made differently, each with what was passed over and why.
	Decisions []string
	// Outside is what was changed that the task did not ask for, Unverified what was not tested or could not be checked.
	Outside, Unverified string
	Questions           []checkQuestion
}

// checkQuestion is a question whose answer shows the reader knows the change.
type checkQuestion struct{ Ask, Answer string }

// explainPrompt asks the agent, in a fork of its own session, for the tour.
func explainPrompt() string {
	return "Walk me through the change you made in this session, so I know it as if I had written it: I have to answer " +
		"for it in review. Change nothing. Start with two or three plain sentences on what the change does and why. Then " +
		"list the places to read, in the order that makes it easy to follow (where it starts, then what it calls), " +
		fmt.Sprintf("at most %d, each on one line as `- path:line — note`, citing a line you added or changed. Each note ", maxWalkStops) +
		"says in one or two sentences what that code does and how it fits the rest, in plain words. After the list, " +
		"each of these on one line of its own: " +
		fmt.Sprintf("up to %d lines `Decision: <what you chose> instead of <what you passed over>, because <why>`, ", maxDecisions) +
		"for the choices I might have made differently; one line `Outside the task: <what you changed that the task did " +
		"not ask for>`, or `Outside the task: none`; one line `Not verified: <what you did not test or could not " +
		"check>`, or `Not verified: none`, and be honest here: it is what I will be asked about; then " +
		fmt.Sprintf("%d lines `Question: <a question about how this change behaves, that its reviewer could ask me> — ", checkQuestions) +
		"<the answer in one or two sentences, from the code>`. Reply with the overview, the list and those lines only."
}

// explainLine is one of the lines an explanation has besides the walkthrough's.
var explainLine = regexp.MustCompile(`(?i)^\s*(?:[-*]\s+)?(decision|outside the task|not verified|question):\s*(.+)$`)

// questionParts splits a question from its answer: the question ends at its question mark.
var questionParts = regexp.MustCompile(`^(.+?\?)[\s—–-]*(.*)$`)

// parseExplanation reads the agent's answer: its own lines are taken out, and the rest is read as a walkthrough.
func parseExplanation(reply string) (explanation, error) {
	var e explanation
	var rest []string
	for _, l := range strings.Split(reply, "\n") {
		m := explainLine.FindStringSubmatch(l)
		if m == nil {
			rest = append(rest, l)
			continue
		}
		text := strings.TrimSpace(m[2])
		if strings.EqualFold(strings.TrimSuffix(text, "."), "none") {
			continue
		}
		switch strings.ToLower(m[1]) {
		case "decision":
			e.Decisions = append(e.Decisions, text)
		case "outside the task":
			e.Outside = text
		case "not verified":
			e.Unverified = text
		case "question":
			q := checkQuestion{Ask: text}
			if p := questionParts.FindStringSubmatch(text); p != nil {
				q = checkQuestion{Ask: p[1], Answer: strings.TrimSpace(p[2])}
			}
			e.Questions = append(e.Questions, q)
		}
	}
	w, err := parseWalkthrough(strings.Join(rest, "\n"))
	e.walkthrough = w
	return e, err
}

// explanationSteps is the explanation one screen a step: the walkthrough's overview and stops, then the decisions, what
// went beyond the task or was not verified, the questions, and their answers a step further on.
func explanationSteps(e explanation, st codeStyle, code codeSource) forms.Steps {
	steps := walkthroughSteps(e.walkthrough, st, code, false)
	steps.Title = "Your change"
	wrap := func(line ...span) string {
		return strings.TrimRight(wrapSpans(st, max(0, st.width), "", "", line...), "\n")
	}
	labelled := func(label, colour, text string) string {
		return wrap(append([]span{{text: label, code: colour}}, inlineSpans(text, st)...)...)
	}
	if len(e.Decisions) > 0 {
		var rows []string
		for i, d := range e.Decisions {
			rows = append(rows, labelled(fmt.Sprintf("%d. ", i+1), st.pal.accent, d))
		}
		steps.Items = append(steps.Items, forms.Step{
			Tab:  "Decisions",
			Body: st.paint(st.pal.dim, "The choices you might have made differently.") + "\n\n" + strings.Join(rows, "\n\n"),
		})
	}
	if e.Outside != "" || e.Unverified != "" {
		var rows []string
		if e.Outside != "" {
			rows = append(rows, labelled("Outside the task: ", st.pal.suggestion, e.Outside))
		}
		if e.Unverified != "" {
			rows = append(rows, labelled("Not verified: ", st.pal.issue, e.Unverified))
		}
		steps.Items = append(steps.Items, forms.Step{Tab: "Gaps", Color: st.pal.issue, Body: strings.Join(rows, "\n\n")})
	}
	if len(e.Questions) > 0 {
		var asks, answers []string
		for i, q := range e.Questions {
			n := fmt.Sprintf("%d. ", i+1)
			asks = append(asks, labelled(n, st.pal.accent, q.Ask))
			answers = append(answers, labelled(n, st.pal.accent, q.Ask)+"\n"+wrap(inlineSpans(cmpOrText(q.Answer, "(no answer was given)"), st)...))
		}
		steps.Items = append(steps.Items,
			forms.Step{
				Tab: "Check",
				Body: st.paint(st.pal.dim, "Could you answer these in review? The next step has the answers.") + "\n\n" +
					strings.Join(asks, "\n\n"),
			},
			forms.Step{Tab: "Answers", Body: strings.Join(answers, "\n\n")})
	}
	return steps
}

func cmpOrText(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// explanationText is the explanation as plain lines, for a terminal the steps cannot be shown on.
func explanationText(e explanation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", e.Overview)
	for i, s := range e.Stops {
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, walkPlace(s), s.Text)
	}
	for i, d := range e.Decisions {
		if i == 0 {
			b.WriteString("\nDecisions\n")
		}
		fmt.Fprintf(&b, "- %s\n", d)
	}
	if e.Outside != "" {
		fmt.Fprintf(&b, "\nOutside the task: %s\n", e.Outside)
	}
	if e.Unverified != "" {
		fmt.Fprintf(&b, "\nNot verified: %s\n", e.Unverified)
	}
	for i, q := range e.Questions {
		if i == 0 {
			b.WriteString("\nCheck yourself\n")
		}
		fmt.Fprintf(&b, "- %s\n  %s\n", q.Ask, q.Answer)
	}
	return b.String()
}

var (
	errNoWorkSession = errors.New("no work or ticket session here to explain: run this where the work was done, or name one with `codeforge explain SESSION` (`codeforge sessions --list` shows them)")
	errExplainUsage  = errors.New("usage: codeforge explain [SESSION | --pick]")
	errExplainPick   = errors.New("--pick asks which session to explain, and there is no terminal to ask on: name one with `codeforge explain SESSION`")
	errExplainSlow   = errors.New("the agent did not finish explaining it in time")
	// errSessionWorking is said of a session in the middle of a turn. An agent that runs `codeforge explain` itself
	// is always in one: its turn waits for the command, and the command would wait for its turn.
	errSessionWorking = errors.New("is in the middle of a turn, and its change can be explained when that turn is over: run `codeforge explain` then, in a terminal of your own (an agent cannot run it on the session it is working in)")
)

// sessionWorking reports whether an engine is in the middle of a turn of the session kept in dir. A fork of such a
// session takes no prompt until the step in flight is over, so asking it to explain would wait as long as the turn
// does: for ever when that step is this very command, run by the session's own agent.
func sessionWorking(dir string) bool {
	open := false
	read := sessionLogLines(filepath.Join(dir, "session.jsonl"), func(line []byte) {
		switch {
		case bytes.HasPrefix(line, []byte(`{"k":"user"`)):
			open = true
		case bytes.HasPrefix(line, []byte(`{"k":"assistant"`)), bytes.HasPrefix(line, []byte(`{"k":"turn-stop"`)):
			open = false
		}
	})
	return read && open && writerAlive(filepath.Join(dir, "writer.lock"))
}

// writerAlive reports whether the engine named in a session's writer lock is still running: a turn left open by an
// engine that died is no turn in progress.
func writerAlive(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var lock struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(data, &lock) != nil || lock.PID <= 0 {
		return false
	}
	err = syscall.Kill(lock.PID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// explainSession finds the session to explain among the work and ticket sessions of wd: the one named by id, or the
// one the user picks. A session belongs to the directory it was started in, not to a branch, so the latest one may be
// of another branch's work: that is why the user is asked. Tests replace it.
var explainSession = func(ctx context.Context, home, wd, id string, pick bool) (sessionInfo, error) {
	sessions, _, err := listSessions(ctx, home, wd)
	if err != nil {
		return sessionInfo{}, err
	}
	if id != "" {
		full, err := resolveResumeID(wd, id)
		if err != nil {
			return sessionInfo{}, err
		}
		id = full
	}
	sessions = slices.DeleteFunc(sessions, func(s sessionInfo) bool { return s.Mode != modeWork && s.Mode != modeTicket })
	return chooseExplained(sessions, id, pick, interactive(), func(sessions []sessionInfo) (int, error) {
		about := fmt.Sprintf("%s in %s, newest first. A session is of the directory it was started in, whatever branch was checked out.",
			plural(len(sessions), "work or ticket session"), filepath.Base(wd))
		return pickSession("Explain the change of which session?", about, sessions, time.Now(), false)
	})
}

// chooseExplained is the session to explain, of the work and ticket sessions given newest first. The one named by id
// is taken as it is. Otherwise the user is asked when there is a terminal to ask on and a choice to make, or when
// --pick says to ask; without a terminal, as in a script, it is the latest. Leaving the picker is forms.ErrAborted.
func chooseExplained(sessions []sessionInfo, id string, pick, canAsk bool, ask func([]sessionInfo) (int, error)) (sessionInfo, error) {
	if id != "" {
		if i := slices.IndexFunc(sessions, func(s sessionInfo) bool { return s.ID == id }); i >= 0 {
			return sessions[i], nil
		}
		return sessionInfo{}, errNoWorkSession
	}
	switch {
	case len(sessions) == 0:
		return sessionInfo{}, errNoWorkSession
	case pick && !canAsk:
		return sessionInfo{}, errExplainPick
	case !canAsk, !pick && len(sessions) == 1:
		return sessions[0], nil
	}
	i, err := ask(sessions)
	switch {
	case !pick && (errors.Is(err, forms.ErrOldHelper) || errors.Is(err, forms.ErrNoHelper)):
		return sessions[0], nil // nothing to ask with, and the user did not ask to be asked: the latest, as in a script
	case err != nil:
		return sessionInfo{}, err
	case i < 0:
		return sessionInfo{}, forms.ErrAborted
	}
	return sessions[i], nil
}

// readOnlyEnv is the environment of an engine that only reads: codeforge's guard holds it to that, and nothing in it
// claims a person launched it.
func readOnlyEnv() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, humanLaunchMarker+"=") })
	return append(env, "EMPRYO_HOOK_ENV=CODEFORGE_*", "CODEFORGE_GUARD=review", launchMarker+"=0", "CODEFORGE_RESUME_HINT=launcher", noAskEnv+"=1")
}

// sandboxRepo wraps an engine that must only read so that the operating system refuses its writes in the repository;
// tests replace it.
var sandboxRepo = readOnlyRepo

// sandboxedRunner runs git for the explanation's diff under the same refusal of writes: a diff or textconv driver the
// repository configures is a program, and showing a change must not let it write.
type sandboxedRunner struct{}

func (sandboxedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	argv, err := sandboxRepo(ctx, append([]string{name}, args...))
	if err != nil {
		return nil, err
	}
	return review.ExecRunner{}.Run(ctx, argv[0], argv[1:]...)
}

// runExplain has the agent of a work or ticket session explain the change it made, and shows that.
func runExplain(ctx context.Context, home, bin string, args []string, w io.Writer) error {
	id, pick := "", false
	for _, a := range args {
		switch {
		case a == "--pick":
			pick = true
		case strings.HasPrefix(a, "-"), id != "":
			return errExplainUsage
		default:
			id = a
		}
	}
	if pick && id != "" {
		return errExplainUsage
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}
	s, err := explainSession(ctx, home, wd, id, pick)
	if errors.Is(err, forms.ErrAborted) {
		return nil // the user left the picker: nothing to explain
	}
	if err != nil {
		return err
	}
	if sessionWorking(filepath.Join(s.Dir, ".empryo", "sessions", s.ID)) {
		return fmt.Errorf("the session %q %w", cmpOrText(s.Title, s.ID), errSessionWorking)
	}
	fmt.Fprintf(os.Stderr, "codeforge: asking the agent of %q to explain its change...\n", cmpOrText(s.Title, s.ID))
	tctx, cancel := context.WithTimeout(ctx, explainLimit)
	defer cancel()
	// A fork, so the session itself is left as it was; plan mode and the review guard, so the tour changes nothing.
	argv := []string{bin, "--fork", s.ID, "--headless", "--json", "--profile", profileFor(s.Mode), "--mode", "plan", explainPrompt()}
	// The operating system refuses it every write in the repository as well, as it does a review.
	if argv, err = sandboxRepo(tctx, argv); err != nil {
		return err
	}
	out, err := runFork(tctx, argv, readOnlyEnv())
	if err != nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
		// The engine was stopped at the limit: "signal: killed" says nothing of why.
		return fmt.Errorf("%w (%s)", errExplainSlow, explainLimit)
	}
	if err != nil {
		return fmt.Errorf("the agent could not explain it: %w", err)
	}
	reply, err := review.EngineOutput(out)
	if err != nil {
		return fmt.Errorf("the agent could not explain it: %w", err)
	}
	e, err := parseExplanation(reply)
	if err != nil {
		return fmt.Errorf("the agent could not explain it: %w", err)
	}
	// The code is read from the working tree, and what the branch adds is shown whole.
	display := &review.Display{}
	display.Local()
	if l, err := review.LoadLocal(tctx, sandboxedRunner{}); err == nil {
		display.SetDiff(l.Diff)
	}
	ctx = review.WithDisplay(ctx, display)
	if !stdoutStyled() || !accordionAvailable() {
		fmt.Fprint(w, explanationText(e))
		return nil
	}
	st := codeStyleFor(ctx, os.Stdout)
	reader := reviewReader(ctx, walkthroughText(e.walkthrough))
	_, err = showSteps("explain", withMap(explanationSteps(e, st, reader.source(ctx, st)), e.Stops, reader, st))
	switch {
	case errors.Is(err, forms.ErrOldHelper):
		fmt.Fprint(w, explanationText(e))
	case err != nil && !errors.Is(err, forms.ErrAborted):
		return fmt.Errorf("showing the explanation: %w", err)
	}
	return nil
}
