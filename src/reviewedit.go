package src

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
)

// In the review view the user marks changes on the findings (rewrite, delete) and questions (about a finding, a file or
// the whole review), then sends them at once. Rewrites and deletions are applied as written; questions go to the
// reviewer in one more turn of the review's own session, which answers each one and sends the review back revised.

// findingSpan is one finding of a review: its lines [Start, End), as parsed, and the id the view knows it by.
type findingSpan struct {
	ID         string
	Start, End int
	F          review.Labeled
	// Raw is the finding's first line without its list marker, as offered for rewriting; Marker is that marker.
	Raw, Marker string
}

// Place is where the finding points, as path:line.
func (s findingSpan) Place() string { return fmt.Sprintf("%s:%d", s.F.Path, s.F.Line) }

func reviewLines(text string) []string {
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

// findingSpans finds the review's findings in order; the n-th is f<n>, so the same text always gives the same ids.
func findingSpans(lines []string) []findingSpan {
	var spans []findingSpan
	for i := 0; i < len(lines); i++ {
		f, ok := review.ParseLabeled(lines[i])
		if !ok {
			continue
		}
		end := review.LabeledEnd(lines, i)
		raw := strings.TrimSpace(lines[i])
		marker := ""
		if m := mdBullet.FindStringSubmatch(lines[i]); m != nil {
			marker, raw = m[1]+m[2]+" ", strings.TrimSpace(m[3])
		}
		spans = append(spans, findingSpan{ID: "f" + strconv.Itoa(len(spans)), Start: i, End: end, F: f, Raw: raw, Marker: marker})
		i = end - 1
	}
	return spans
}

// noteMark is what the view shows on a finding after a round of changes: a tag (revised, new, edited by you) and the
// reviewer's answer to the user's question about it.
type noteMark struct {
	Tag, Reply string
}

func (m noteMark) render(st codeStyle) string {
	var b strings.Builder
	if m.Tag != "" {
		b.WriteString("\n" + findingIndent + markTag(st, m.Tag))
	}
	if m.Reply != "" {
		width := 0
		if st.wrap {
			width = st.width
		}
		b.WriteString("\n" + strings.TrimRight(wrapSpans(st, width, findingIndent+"↳ reviewer: ", findingIndent+"  ",
			span{text: m.Reply, code: st.pal.dim}), "\n"))
	}
	return b.String()
}

// markTag shows a tag as a small badge on a colour terminal, in brackets otherwise.
func markTag(st codeStyle, tag string) string {
	if !st.color {
		return "[" + tag + "]"
	}
	color := st.pal.praise
	if tag == markEdited {
		color = st.pal.suggestion
	}
	return color + "\x1b[7m " + tag + " " + ansiReset
}

// The tags a finding can carry after a round of changes.
const (
	markRevised = "revised"
	markNew     = "new"
	markEdited  = "edited by you"
)

// reviewRound is the review after the user's changes: its text, the marks to show on its findings, and the reviewer's
// answers that belong to no finding.
type reviewRound struct {
	Text    string
	Marks   map[string]noteMark
	Answers []string
}

// roundIntro is what the view shows above the review after a round of changes: the findings the round changed, by
// place, so they can be found below, and the reviewer's answers that belong to no finding.
func roundIntro(st codeStyle, text string, round reviewRound) string {
	const answered = "answered"
	places := map[string][]string{}
	for _, s := range findingSpans(reviewLines(text)) {
		m, ok := round.Marks[s.ID]
		if !ok {
			continue
		}
		tag := m.Tag
		if tag == "" && m.Reply != "" {
			tag = answered
		}
		if tag != "" {
			places[tag] = append(places[tag], s.Place())
		}
	}
	var parts []string
	for _, tag := range []string{markRevised, markNew, markEdited, answered} {
		if len(places[tag]) > 0 {
			parts = append(parts, st.paint(st.pal.dim, tag+" ")+st.paint(st.pal.accent, strings.Join(places[tag], ", ")))
		}
	}
	var b strings.Builder
	if len(parts) > 0 {
		b.WriteString(st.paint(st.pal.bold, "This round") + "  " + strings.Join(parts, st.paint(st.pal.dim, " · ")) + "\n")
	}
	if len(round.Answers) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(st.paint(st.pal.bold, "The reviewer's answers") + "\n")
		for _, ans := range round.Answers {
			b.WriteString(wrapSpans(st, max(0, st.width-len(accordionIndent)), "  ↳ ", "    ", inlineSpans(ans, st)...))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// question is one question the user asked, with what it is about.
type question struct {
	About string // `a.go:12` (the finding's first line), a file, or "the whole review"
	Place string // the finding's path:line, "" when the question is not about a finding
	Text  string
}

// applyEdits applies the user's rewrites and deletions to the review and lists the questions, in the order asked.
// edited lists the rewritten lines as they now read.
func applyEdits(text string, changes []forms.Change) (out string, questions []question, edited []string) {
	lines := reviewLines(text)
	spans := findingSpans(lines)
	byID := map[string]findingSpan{}
	for _, s := range spans {
		byID[s.ID] = s
	}
	replace := map[int]string{} // first line of a finding -> its rewrite
	drop := map[int]bool{}      // first line of a deleted finding
	for _, c := range changes {
		s, onFinding := byID[c.Note]
		switch c.Kind {
		case forms.ChangeEdit:
			if onFinding {
				replace[s.Start] = s.Marker + strings.TrimSpace(c.Text)
				edited = append(edited, strings.TrimSpace(c.Text))
			}
		case forms.ChangeDelete:
			if onFinding {
				drop[s.Start] = true
			}
		case forms.ChangeAsk:
			q := question{Text: strings.TrimSpace(c.Text), About: "the whole review"}
			switch {
			case onFinding:
				q.About, q.Place = "`"+s.Place()+"` ("+s.Raw+")", s.Place()
			case c.Section != "":
				q.About = c.Section
			}
			questions = append(questions, q)
		}
	}
	ends := map[int]int{} // first line of a finding -> just past its last
	for _, s := range spans {
		ends[s.Start] = s.End
	}
	var kept []string
	for i := 0; i < len(lines); i++ {
		if drop[i] {
			// The whole finding goes, its continuation lines too. Spans never overlap (findingSpans resumes after each
			// one's end), so this skips only that finding's lines.
			i = ends[i] - 1
			continue
		}
		if r, ok := replace[i]; ok {
			kept = append(kept, r)
			continue
		}
		kept = append(kept, lines[i])
	}
	return collapseBlankLines(strings.Join(kept, "\n")), questions, edited
}

// collapseBlankLines keeps at most one blank line in a row, so a deleted finding leaves no gap.
func collapseBlankLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" && len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// reviseReview applies the user's changes. Rewrites and deletions take effect as written; when there are questions,
// the reviewer answers them in one more turn of the review's session (turn) and sends the review back revised.
//
// clean, when set, tidies the revised review the way the first reply was tidied (a note to the user, a verdict label,
// a follow-up round's thread verdicts), so none of it is shown or posted as review text.
func reviseReview(ctx context.Context, text string, changes []forms.Change, turn func(context.Context, string) (string, error), clean func(string) string) (reviewRound, error) {
	edited, questions, rewrites := applyEdits(text, changes)
	round := reviewRound{Text: edited}
	if len(questions) > 0 {
		reply, err := turn(ctx, revisionPrompt(edited, questions, len(rewrites) > 0 || hasDeletion(changes)))
		if err != nil {
			return reviewRound{}, err
		}
		revised, answers := splitAnswers(reply)
		if clean != nil {
			revised = clean(revised)
		}
		if strings.TrimSpace(revised) == "" {
			return reviewRound{}, errNoRevision
		}
		round.Text = revised
		round.Marks = compareFindings(edited, revised)
		byQuestion := map[int]string{}
		for _, a := range answers {
			byQuestion[a.Question] = strings.TrimSpace(a.Reply)
		}
		spans := findingSpans(reviewLines(revised))
		for i, q := range questions {
			reply := byQuestion[i+1]
			if reply == "" {
				reply = "(no answer)"
			}
			if at := slices.IndexFunc(spans, func(s findingSpan) bool { return s.Place() == q.Place }); q.Place != "" && at >= 0 {
				m := round.Marks[spans[at].ID]
				m.Reply = reply
				round.Marks[spans[at].ID] = m
				continue
			}
			round.Answers = append(round.Answers, fmt.Sprintf("About %s: %s", strings.Trim(q.About, "`"), reply))
		}
	}
	// The user's own rewrites say so, unless the reviewer revised them since.
	if len(rewrites) > 0 {
		if round.Marks == nil {
			round.Marks = map[string]noteMark{}
		}
		for _, s := range findingSpans(reviewLines(round.Text)) {
			if slices.Contains(rewrites, s.Raw) && round.Marks[s.ID].Tag == "" {
				m := round.Marks[s.ID]
				m.Tag = markEdited
				round.Marks[s.ID] = m
			}
		}
	}
	return round, nil
}

func countQuestions(changes []forms.Change) int {
	n := 0
	for _, c := range changes {
		if c.Kind == forms.ChangeAsk {
			n++
		}
	}
	return n
}

var errNoRevision = errors.New("the reviewer sent no review back, so the review is as it was")

func hasDeletion(changes []forms.Change) bool {
	return slices.ContainsFunc(changes, func(c forms.Change) bool { return c.Kind == forms.ChangeDelete })
}

// compareFindings marks the findings of after that are new or that read differently from the finding at the same
// place before.
//
// before already holds the user's own rewrites, so a rewrite the reviewer kept is not marked revised; findings at the
// same place are told apart by their order there.
func compareFindings(before, after string) map[string]noteMark {
	key := func() func(findingSpan) string {
		seen := map[string]int{}
		return func(s findingSpan) string {
			seen[s.Place()]++
			return fmt.Sprintf("%s#%d", s.Place(), seen[s.Place()])
		}
	}
	was, keyBefore := map[string]string{}, key()
	for _, s := range findingSpans(reviewLines(before)) {
		was[keyBefore(s)] = s.Raw
	}
	marks, keyAfter := map[string]noteMark{}, key()
	for _, s := range findingSpans(reviewLines(after)) {
		old, ok := was[keyAfter(s)]
		switch {
		case !ok:
			marks[s.ID] = noteMark{Tag: markNew}
		case old != s.Raw:
			marks[s.ID] = noteMark{Tag: markRevised}
		}
	}
	return marks
}

// answersTag fences the reviewer's answers after the revised review.
const answersTag = "codeforge-answers"

// revisionPrompt asks the reviewer, in its own session, to answer the user's questions and send the review back
// revised where an answer changes it.
func revisionPrompt(text string, questions []question, userChanged bool) string {
	var b strings.Builder
	b.WriteString("Before posting, the user read your review and asks you the questions below. Answer each one plainly, " +
		"as their colleague. Then revise the review wherever an answer changes it: reword a finding, change its label " +
		"(for example from issue (blocking) to suggestion when it does not block), drop it, or add a new finding with its " +
		"place. Leave everything else exactly as it is, and keep the Decision line true to the findings that remain.\n")
	if userChanged {
		b.WriteString("The user has already rewritten or deleted some findings; the review below has their changes. " +
			"Those are final: keep their wording and do not bring back what they deleted.\n")
	}
	b.WriteString("\nQuestions:\n")
	for i, q := range questions {
		fmt.Fprintf(&b, "%d. About %s: %s\n", i+1, q.About, q.Text)
	}
	b.WriteString("\nThe review as it stands:\n\n" + text + "\n\n")
	b.WriteString("Reply with the complete review in the same format as before (the findings, then the final comment, " +
		"then the Decision line), and after it one block fenced as ```" + answersTag + " holding json, with nothing after it:\n" +
		"```" + answersTag + "\n{\"answers\":[{\"question\":1,\"reply\":\"<one or two sentences>\"}]}\n```\n" +
		"Give one answer per question, by its number.")
	return b.String()
}

// revisionAnswer is the reviewer's answer to one question, by its number.
type revisionAnswer struct {
	Question int    `json:"question"`
	Reply    string `json:"reply"`
}

// splitAnswers takes the answers block off the end of the reviewer's reply: what is before it is the revised review.
// A reply without a readable block is all review, with no answers.
func splitAnswers(reply string) (string, []revisionAnswer) {
	lines := reviewLines(strings.TrimSpace(reply))
	open := -1
	for i, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l), "```"+answersTag) {
			open = i
		}
	}
	if open < 0 {
		return strings.TrimSpace(reply), nil
	}
	end := len(lines)
	for j := open + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "```" {
			end = j
			break
		}
	}
	var doc struct {
		Answers []revisionAnswer `json:"answers"`
	}
	_ = json.Unmarshal([]byte(strings.Join(lines[open+1:end], "\n")), &doc) //nolint:errcheck // unreadable answers are none
	return strings.TrimSpace(strings.Join(lines[:open], "\n")), doc.Answers
}

// revisionLimit bounds the reviewer's turn on the user's questions; it starts when the user sends them.
var revisionLimit = 3 * time.Minute

// reviewTurn runs one more headless turn in the review's saved session with prompt, the way the review itself ran
// (argv), and returns what the reviewer wrote.
func reviewTurn(argv, env []string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, prompt string) (string, error) {
		id := flagValue(argv, "--session-id")
		if id == "" {
			id = flagValue(argv, "--session")
		}
		if id == "" || len(argv) == 0 {
			return "", errSessionMissing
		}
		args := withoutPrompt(dropFlag(dropFlag(argv[1:], "--session-id"), "--session"))
		turn := append(append([]string{argv[0], "--session", id}, args...), prompt)
		// The review's own clock has run out by now; the turn gets a limit of its own from when the user sent it.
		tctx := context.WithoutCancel(ctx)
		var out, chatter bytes.Buffer
		err := runEngineTo(withEngineStderr(tctx, &chatter), turn, env, time.Now().Add(revisionLimit), &out)
		if err != nil {
			fmt.Fprint(os.Stderr, chatter.String())
			return "", fmt.Errorf("asking the reviewer: %w", err)
		}
		reply, err := review.EngineOutput(out.Bytes())
		if err != nil {
			return "", fmt.Errorf("reading the reviewer's answer: %w", err)
		}
		return reply, nil
	}
}

// withoutPrompt drops the review's prompt, its last positional argument, and keeps everything else as it was: every
// flag, and every other positional too, so a flag codeforge does not know takes the same value as in the review and
// the turn runs under the same settings.
func withoutPrompt(args []string) []string {
	prompt := -1
	for i := 0; i < len(args); i++ {
		key, _, equal := strings.Cut(args[i], "=")
		switch {
		case flagsWithValue[key] && !equal:
			i++ // its value, never the prompt
		case !strings.HasPrefix(args[i], "-"):
			prompt = i
		}
	}
	if prompt < 0 {
		return slices.Clone(args)
	}
	return slices.Concat(args[:prompt], args[prompt+1:])
}
