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
	// Nth and Raw tell findings at the same place apart: the question is about the Nth (from 1) of them, which reads
	// Raw, as the review reads after the user's own changes. Nth is 0 when that finding is gone or is no finding any more.
	Nth  int
	Raw  string
	Text string
}

// label names what the question is about for the user: the finding's place, a file, or the whole review.
func (q question) label() string {
	if q.Place != "" {
		return q.Place
	}
	return q.About
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
		switch {
		case !onFinding:
		case c.Kind == forms.ChangeEdit:
			text := rewriteText(c.Text)
			replace[s.Start] = s.Marker + text
			edited = append(edited, text)
		case c.Kind == forms.ChangeDelete:
			drop[s.Start] = true
		}
	}
	ends := map[int]int{} // first line of a finding -> just past its last
	for _, s := range spans {
		ends[s.Start] = s.End
	}
	var kept []string
	at := map[int]int{} // first line of a kept finding -> its line in kept
	for i := 0; i < len(lines); i++ {
		if drop[i] {
			// The whole finding goes, its continuation lines too. Spans never overlap (findingSpans resumes after each
			// one's end), so this skips only that finding's lines; the review's closing Decision line, when written
			// right under it, is no part of it and stays.
			if last := ends[i] - 1; isClosingDecision(lines, last) {
				kept = append(kept, lines[last])
			}
			i = ends[i] - 1
			continue
		}
		if _, isFinding := ends[i]; isFinding {
			at[i] = len(kept)
		}
		if r, ok := replace[i]; ok {
			kept = append(kept, r)
			continue
		}
		kept = append(kept, lines[i])
	}
	// A question's finding is named as the edited review reads: a rewrite may have moved it to another place.
	keptSpans := findingSpans(kept)
	for _, c := range changes {
		if c.Kind != forms.ChangeAsk {
			continue
		}
		q := question{Text: strings.TrimSpace(c.Text), About: "the whole review"}
		s, onFinding := byID[c.Note]
		switch {
		case onFinding:
			q.About, q.Place = "`"+s.Place()+"` ("+s.Raw+")", s.Place()
			if k, ok := at[s.Start]; ok {
				if i := slices.IndexFunc(keptSpans, func(o findingSpan) bool { return o.Start == k }); i >= 0 {
					now := keptSpans[i]
					q.Place, q.Raw = now.Place(), now.Raw
					for _, o := range keptSpans[:i+1] {
						if o.Place() == now.Place() {
							q.Nth++
						}
					}
				}
			}
		case c.Section != "":
			q.About = c.Section
		}
		questions = append(questions, q)
	}
	return collapseBlankLines(strings.Join(kept, "\n")), questions, edited
}

// isClosingDecision reports whether line i is the review's Decision line: one ParseDecision reads, with nothing but
// blank lines after it.
func isClosingDecision(lines []string, i int) bool {
	if i < 0 || i >= len(lines) || !review.IsDecisionLine(lines[i]) {
		return false
	}
	for _, l := range lines[i+1:] {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

// rewriteText is a rewrite as it goes into the review: trimmed, and without a list marker the user typed before a
// finding, since the finding keeps its own. A marker before anything else is the user's text and stays.
func rewriteText(text string) string {
	text = strings.TrimSpace(text)
	if m := mdBullet.FindStringSubmatch(text); m != nil {
		if _, ok := review.ParseLabeled(strings.TrimSpace(m[3])); ok {
			return strings.TrimSpace(m[3])
		}
	}
	return text
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
//
// When the reviewer cannot answer, the error comes with the round as the user's own changes left it: those need no
// reviewer, so they are kept.
func reviseReview(ctx context.Context, text string, changes []forms.Change, turn func(context.Context, string) (string, error), clean func(string) string) (reviewRound, error) {
	edited, questions, rewrites := applyEdits(text, changes)
	round := reviewRound{Text: edited, Marks: map[string]noteMark{}}
	if len(questions) > 0 {
		revised, answers, err := askReviewer(ctx, edited, questions, len(rewrites) > 0 || hasDeletion(changes), turn, clean)
		if err != nil {
			markRewrites(round, rewrites)
			return round, err
		}
		round.Text = revised
		round.Marks = compareFindings(edited, revised)
		spans, before := findingSpans(reviewLines(revised)), findingSpans(reviewLines(edited))
		for i, q := range questions {
			reply := answers[i+1]
			if reply == "" {
				reply = "(no answer)"
			}
			if at := askedFinding(before, spans, q); at >= 0 {
				m := round.Marks[spans[at].ID]
				m.Reply = reply
				round.Marks[spans[at].ID] = m
				continue
			}
			round.Answers = append(round.Answers, fmt.Sprintf("About %s: %s", q.label(), reply))
		}
	}
	markRewrites(round, rewrites)
	return round, nil
}

// askReviewer runs the reviewer's turn on the questions and returns the revised review, cleaned, and the answers by
// question number.
func askReviewer(ctx context.Context, edited string, questions []question, userChanged bool, turn func(context.Context, string) (string, error), clean func(string) string) (string, map[int]string, error) {
	reply, err := turn(ctx, revisionPrompt(edited, questions, userChanged))
	if err != nil {
		return "", nil, err
	}
	revised, answers := splitAnswers(reply)
	if clean != nil {
		revised = clean(revised)
	}
	if strings.TrimSpace(revised) == "" {
		return "", nil, errNoRevision
	}
	byQuestion := map[int]string{}
	for _, a := range answers {
		byQuestion[a.Question] = strings.TrimSpace(a.Reply)
	}
	return revised, byQuestion, nil
}

// askedFinding is the index in after (the revised review) of the finding q asks about, or -1 when it cannot be told
// for sure, so the answer is shown by its place rather than put on another finding. It is the finding at q's place
// that still reads as asked; failing that, one the reviewer reworded in place, known by its position there while the
// place has as many findings as before.
func askedFinding(before, after []findingSpan, q question) int {
	if q.Nth < 1 {
		return -1
	}
	if i := slices.IndexFunc(after, func(s findingSpan) bool { return s.Place() == q.Place && s.Raw == q.Raw }); i >= 0 {
		return i
	}
	count := func(spans []findingSpan) int {
		n := 0
		for _, s := range spans {
			if s.Place() == q.Place {
				n++
			}
		}
		return n
	}
	if count(before) != count(after) {
		return -1
	}
	return nthAt(after, q.Place, q.Nth)
}

// nthAt is the index of the nth (from 1) finding at place; -1 when there is no such finding, so an answer whose
// finding is gone is shown by its place rather than put on another finding there.
func nthAt(spans []findingSpan, place string, nth int) int {
	if place == "" || nth < 1 {
		return -1
	}
	seen := 0
	for i, s := range spans {
		if s.Place() != place {
			continue
		}
		if seen++; seen == nth {
			return i
		}
	}
	return -1
}

// markRewrites tags the findings that read as the user rewrote them, unless the reviewer revised them since. Each
// rewrite tags one finding, the first that reads as it does, so a second finding that reads the same is not tagged
// too; which of two identical findings carries the tag does not matter.
func markRewrites(round reviewRound, rewrites []string) {
	left := slices.Clone(rewrites)
	for _, s := range findingSpans(reviewLines(round.Text)) {
		i := slices.Index(left, s.Raw)
		if i < 0 {
			continue
		}
		left = slices.Delete(left, i, i+1)
		if m := round.Marks[s.ID]; m.Tag == "" {
			m.Tag = markEdited
			round.Marks[s.ID] = m
		}
	}
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

var errNoRevision = errors.New("the reviewer sent no review back")

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
		// The review's own clock has run out by now; the turn gets a limit of its own from when the user sent it. Ctrl+C
		// still stops it: with a limit the engine runs in its own process group and runEngineTo forwards the interrupt.
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
