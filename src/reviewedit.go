package src

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

// finalNoteID is the id the view knows the review's final comment by.
const finalNoteID = "final"

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
	final, finalChanged := "", false
	for _, c := range changes {
		if c.Note == finalNoteID && c.Kind != forms.ChangeAsk {
			// The final comment is rewritten whole, or deleted: the words between the findings and the Decision line.
			final, finalChanged = strings.TrimSpace(c.Text), true
			if c.Kind == forms.ChangeEdit {
				edited = append(edited, final)
			}
			continue
		}
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
		case c.Note == finalNoteID:
			q.About = "your final comment"
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
	out = collapseBlankLines(strings.Join(kept, "\n"))
	if finalChanged {
		out = withFinalComment(out, final)
	}
	return out, questions, edited
}

// withFinalComment is text with its final comment (the words that are no finding and no Decision line) replaced by
// final: the findings first, then final, then the Decision line.
func withFinalComment(text, final string) string {
	lines := reviewLines(text)
	var findings []string
	for _, s := range findingSpans(lines) {
		findings = append(findings, strings.Join(lines[s.Start:s.End], "\n"))
	}
	parts := []string{strings.Join(findings, "\n\n")}
	if final != "" {
		parts = append(parts, final)
	}
	for _, l := range lines {
		if review.IsDecisionLine(l) {
			parts = append(parts, strings.TrimSpace(l))
		}
	}
	return collapseBlankLines(strings.Join(parts, "\n\n"))
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
		// A review runs inside the read-only sandbox (sandbox-exec -p PROFILE empryo ...): the turn keeps that wrapper
		// and the engine behind it, and changes only the engine's own arguments.
		wrapper, engine := sandboxPrefix(argv)
		if len(engine) == 0 {
			return "", errSessionMissing
		}
		bin := slices.Concat(wrapper, engine[:1])
		args := withoutPrompt(dropFlag(dropFlag(engine[1:], "--session-id"), "--session"))
		reply, err := resumedTurn(ctx, slices.Concat(bin, []string{"--session", id}, args, []string{prompt}), env)
		var exit *EngineExitError
		if err == nil || ctx.Err() != nil || !errors.As(err, &exit) || exit.Code != exitUsage {
			return reply, err // done, stopped by the user, or failed for a reason a plainer call does not change
		}
		// A flag the review ran with may not suit a resumed turn: try once more with only what a turn needs, the
		// review's profile, mode and model, so a question still gets its answer.
		minimal := slices.Concat(bin, []string{"--session", id, "--headless", "--json"})
		for _, f := range []string{"--profile", "--mode", "--model"} {
			if v := flagValue(argv, f); v != "" {
				minimal = append(minimal, f, v)
			}
		}
		if reply, again := resumedTurn(ctx, append(minimal, prompt), env); again == nil {
			return reply, nil
		}
		return "", err
	}
}

// sandboxPrefix splits argv into the sandbox wrapper it runs under, if any (sandbox-exec -p PROFILE), and the command
// the wrapper runs.
func sandboxPrefix(argv []string) (wrapper, command []string) {
	const wrapperArgs = 3 // sandbox-exec, -p, the profile
	if len(argv) >= wrapperArgs && argv[0] == sandboxExec && argv[1] == "-p" {
		return argv[:wrapperArgs], argv[wrapperArgs:]
	}
	return nil, argv
}

// exitUsage is the status an engine exits with when its command line is wrong (EX_USAGE): the one failure a turn with
// fewer flags can get past.
const exitUsage = 64

// resumedTurn runs one turn of the engine, argv in full, and returns what it wrote. On a failure the error ends with
// the engine's own last line of complaint, so the user sees why, not only an exit status.
func resumedTurn(ctx context.Context, argv, env []string) (string, error) {
	// The review's own clock has run out by now; the turn gets a limit of its own from when the user sent it. Ctrl+C
	// still stops it: with a limit the engine runs in its own process group and runEngineTo forwards the interrupt.
	tctx := context.WithoutCancel(ctx)
	var out, chatter bytes.Buffer
	if err := runEngineTo(withEngineStderr(tctx, &chatter), argv, env, time.Now().Add(revisionLimit), &out); err != nil {
		if why := lastLine(chatter.String()); why != "" {
			return "", fmt.Errorf("asking the reviewer: %w (%s)", err, why)
		}
		return "", fmt.Errorf("asking the reviewer: %w", err)
	}
	reply, err := review.EngineOutput(out.Bytes())
	if err != nil {
		return "", fmt.Errorf("reading the reviewer's answer: %w", err)
	}
	return reply, nil
}

// lastLine is the last line of s that holds anything, cut to a length that fits a message.
func lastLine(s string) string {
	const maxWhy = 200
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return clip(strings.TrimSpace(lines[len(lines)-1]), maxWhy)
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

// voiceLeads has the reviewer rewrite the unconfirmed blocking leads, which another reviewer wrote in its own words, as
// the user would leave them: short, plain and in the persona's voice, one line each, every lead kept at its place. The
// leads are returned as they were when the turn fails or its answer does not keep every lead, so a blocker is never
// lost to a rewrite.
func voiceLeads(ctx context.Context, leads string, turn func(context.Context, string) (string, error)) string {
	header, body, found := strings.Cut(leads, review.LeadsHeader)
	if !found || strings.TrimSpace(header) != "" {
		return leads
	}
	want := leadPlaces(body)
	if len(want) == 0 || len(want) != nonEmptyLines(body) {
		return leads // a lead in a shape that cannot be checked after a rewrite is posted as it was written
	}
	// Whatever the turn gives, no lead is posted longer than its plain form: what breaks and the fix, without the
	// walkthrough.
	var compact []string
	for _, l := range reviewLines(body) {
		if l = strings.TrimSpace(l); l != "" {
			compact = append(compact, compactLead(l))
		}
	}
	fallback := review.LeadsHeader + strings.Join(compact, "\n") + "\n"
	reply, err := turn(ctx, leadsPrompt(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge: the leads could not be put in your words ("+err.Error()+"); posting them shortened.")
		return fallback
	}
	var lines []string
	for _, l := range reviewLines(reply) {
		if l = strings.TrimSpace(l); leadLine.MatchString(l) {
			lines = append(lines, l)
		}
	}
	if !slices.Equal(leadPlaces(strings.Join(lines, "\n")), want) {
		return fallback
	}
	// A rewrite that says next to nothing has lost the lead, and one no shorter than the plain form gains nothing: the
	// plain form stands for it.
	for i, l := range lines {
		if n := len(strings.Fields(l)) - leadPrefixWords; n < minLeadWords || len(strings.Fields(l)) >= len(strings.Fields(compact[i])) && n > maxLeadWords {
			lines[i] = compact[i]
		}
	}
	return review.LeadsHeader + strings.Join(lines, "\n") + "\n"
}

// compactLead is a lead line cut to what a reader needs: its place, the sentence that says what breaks, and the
// sentence that says the fix. Examples and walkthroughs between them go; a lead with nothing to cut stays as written.
func compactLead(line string) string {
	m := leadLine.FindStringSubmatch(line)
	if m == nil || len(strings.Fields(line)) <= maxLeadWords {
		return line
	}
	prefix := "- `" + m[1] + "` "
	sentences := splitSentences(strings.TrimPrefix(line, prefix))
	if len(sentences) == 1 {
		return line // one sentence: nothing between what breaks and the fix to cut
	}
	// The fix is the sentence that says so, or else the last one: a reviewer ends a lead with what to do.
	fix := sentences[len(sentences)-1]
	for _, s := range sentences[1:] {
		if fixLead.MatchString(s) {
			fix = s
			break
		}
	}
	kept := []string{sentences[0], fixLead.ReplaceAllString(fix, "")}
	for i, s := range kept {
		s = strings.TrimSpace(s)
		if !strings.HasSuffix(s, ".") {
			s += "."
		}
		r, w := utf8.DecodeRuneInString(s)
		kept[i] = string(unicode.ToUpper(r)) + s[w:]
	}
	return prefix + strings.Join(kept, " ")
}

// splitSentences splits prose into its sentences at each sentenceBreak, keeping every character but the space between.
// A dot after an abbreviation or a single letter ("e.g.", "vs.", "A.") ends no sentence.
func splitSentences(text string) []string {
	var out []string
	start := 0
	for _, m := range sentenceBreak.FindAllStringIndex(text, -1) {
		words := strings.Fields(text[start : m[0]+1])
		if len(words) == 0 {
			continue
		}
		last := strings.ToLower(strings.Trim(words[len(words)-1], "(`"))
		if abbreviations[last] || len([]rune(strings.TrimSuffix(last, "."))) <= 1 {
			continue
		}
		out = append(out, text[start:m[0]+1])
		start = m[1] - 1 // the letter or backtick that opens the next sentence
	}
	return append(out, text[start:])
}

// abbreviations end with a dot that ends no sentence.
var abbreviations = map[string]bool{"e.g.": true, "i.e.": true, "vs.": true, "etc.": true, "fig.": true, "cf.": true, "approx.": true, "no.": true}

var (
	// sentenceBreak splits prose at a full stop that ends a sentence: one followed by a space and a capital or a
	// backtick, so a dot inside code or a number stays.
	sentenceBreak = regexp.MustCompile(`\.\s+(?:[A-Za-z\x60])`)
	// fixLead opens the sentence that says the fix.
	fixLead = regexp.MustCompile(`(?i)^\s*(fix|to fix|the fix)\s*(is)?\s*:\s*`)
)

// nonEmptyLines counts the lines of text that hold anything.
func nonEmptyLines(text string) int {
	n := 0
	for _, l := range reviewLines(text) {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// leadLine is one lead as listed: a list item that opens with its place in backticks.
var leadLine = regexp.MustCompile("^- `([^`]+:\\d+)` \\S")

// leadPlaces lists the places of the leads in text, in order.
func leadPlaces(text string) []string {
	var places []string
	for _, l := range reviewLines(text) {
		if m := leadLine.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			places = append(places, m[1])
		}
	}
	return places
}

// maxLeadWords bounds one rewritten lead: a reader takes it in at a glance. minLeadWords is the fewest that can still
// say what breaks and what to do.
const (
	maxLeadWords = 45
	minLeadWords = 5
	// leadPrefixWords are the list marker and the place a lead's line opens with.
	leadPrefixWords = 2
)

// leadsPrompt asks the reviewer, in its own session, to put the leads in the user's words.
func leadsPrompt(leads string) string {
	return "Another check of this change found the possible blockers below, which your review does not confirm. " +
		"They go on the PR under your review, so rewrite each one as I would leave it, in my voice as codeforge-persona " +
		"describes it (codeforge-voice where it is silent): what breaks, then what to do, in plain words, " +
		fmt.Sprintf("at most two sentences and %d words, with no hedging, no walkthrough and no mention of models, tools or ", maxLeadWords) +
		"a second check. Keep every lead, in the same order, each on one line that starts with its place exactly as " +
		"given (`- `path:line` text`). Reply with those lines and nothing else.\n\n" + leads
}

// isBlocking reports whether a finding's label blocks the merge, as ParseDecision reads it.
func isBlocking(label string) bool {
	l := strings.ToLower(label)
	return strings.Contains(l, "blocking") && !strings.Contains(l, "non-blocking")
}

// leadNearby is how many lines from a finding a lead may sit and still be the same point.
const leadNearby = 5

// dropCoveredLeads leaves out the leads the review already makes as a finding of its own: one in the same file within
// leadNearby lines. Posted twice, the same blocker reads as two. The rest keep their order; with none left, no leads.
func dropCoveredLeads(leads, text string) string {
	header, body, found := strings.Cut(leads, review.LeadsHeader)
	if !found || strings.TrimSpace(header) != "" {
		return leads
	}
	spans := findingSpans(reviewLines(text))
	var kept []string
	for _, l := range reviewLines(body) {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		m := leadLine.FindStringSubmatch(l)
		if m == nil {
			kept = append(kept, l) // a lead it cannot place is never dropped
			continue
		}
		path, line, _ := strings.Cut(m[1], ":")
		n, err := strconv.Atoi(line)
		// Only a blocking finding covers a lead: a nit nearby says nothing about the blocker.
		covered := err == nil && slices.ContainsFunc(spans, func(s findingSpan) bool {
			return isBlocking(s.F.Label) && s.F.Path == path && s.F.Line >= n-leadNearby && s.F.Line <= n+leadNearby
		})
		if !covered {
			kept = append(kept, l)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return review.LeadsHeader + strings.Join(kept, "\n") + "\n"
}

// The most words a posted comment may have before the reviewer is asked to cut it: the instructions ask for about 40
// per finding and 80 for the final comment, and these leave room for a needed example.
const (
	maxFindingWords = 60
	maxFinalWords   = 120
	// What a cut asks for: the instructions' own targets.
	targetFindingWords = 40
	targetFinalWords   = 80
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
