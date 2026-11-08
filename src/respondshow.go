package src

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
)

// The keys the respond view offers: push the fixes and post the replies, or continue in the chat.
const (
	keyPublish = "p"
	// pushLimit bounds pushing the fixes before the replies that name them are posted.
	pushLimit = 90 * time.Second
	// maxDiffLines keeps a fix's diff to what fits beside its thread; diffIndent sets it under its "fixed in" line.
	maxDiffLines = 40
	diffIndent   = "  "
)

// errRepliesFailed is returned when some replies could not be posted; the others are up.
var errRepliesFailed = errors.New("some replies were not posted")

// runShownRespond runs the first pass of a respond session, then shows every thread with its code, the reviewers'
// comments, the agent's verdict, its fix and its draft reply. p pushes the fixes and posts the replies; c opens the
// chat; q and Ctrl+C end there, with nothing pushed or posted. A first pass without its answers block is shown as
// prose and continues in the chat, where the agent can be asked again.
func runShownRespond(ctx context.Context, argv, env []string, limit time.Time, fb *review.Feedback) (chat bool, err error) {
	if !hasFlag(argv, "--json") {
		argv = append(slices.Clone(argv), "--json")
	}
	fmt.Fprintln(os.Stderr, "codeforge: working through the review comments; they appear here with your draft replies when the model is done.")
	var chatter, out bytes.Buffer
	if err := runEngineTo(withEngineStderr(ctx, &chatter), argv, env, limit, &out); err != nil {
		fmt.Fprint(os.Stderr, chatter.String())
		fmt.Fprint(os.Stdout, out.String())
		return false, err
	}
	text, err := review.EngineOutput(out.Bytes())
	if err != nil {
		fmt.Fprint(os.Stderr, chatter.String())
		fmt.Fprint(os.Stdout, out.String())
		return false, err
	}
	st := codeStyleFor(ctx, os.Stdout)
	prose, answers, perr := review.ParseAnswers(text, *fb)
	if perr != nil || !accordionAvailable() {
		fmt.Fprint(os.Stdout, renderReview(prose, st, nil))
		if perr != nil {
			fmt.Fprintln(os.Stderr, "codeforge: the agent did not list its verdicts and replies; continue in the chat.")
		}
		return true, nil
	}

	a := respondAccordion(ctx, prose, *fb, answers, st)
	a.Outro = runFooter(out.Bytes(), st)
	replies := slices.ContainsFunc(answers, func(x review.Answer) bool { return x.Reply != "" })
	if replies {
		a.Keys = append(a.Keys, forms.Action{Key: keyPublish, Label: "push fixes and post replies"})
	}
	a.Keys = append(a.Keys, forms.Action{Key: keyChat, Label: "continue in chat"})
	key, err := showAccordion("respond", a)
	if err != nil && !errors.Is(err, forms.ErrAborted) {
		fmt.Fprintln(os.Stderr, "codeforge: could not open the view:", err)
		fmt.Fprint(os.Stdout, renderReview(prose, st, nil))
		return true, nil
	}
	fmt.Fprint(os.Stdout, accordionRecap(a, st))
	switch key {
	case keyChat:
		return true, nil
	case keyPublish:
		return false, publishAnswers(ctx, *fb, answers)
	}
	fmt.Fprintln(os.Stderr, "codeforge: nothing pushed or posted.")
	return false, nil
}

// publishAnswers pushes the branch, so every "Fixed in <sha>" names a commit the reviewer can open, then posts the
// replies. A failed push posts nothing.
func publishAnswers(ctx context.Context, fb review.Feedback, answers []review.Answer) error {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pushLimit)
	defer cancel()
	if _, err := codeRunner.Run(pctx, "git", "push", "origin", "HEAD"); err != nil {
		return fmt.Errorf("pushing the fixes, so no reply was posted: %w", err)
	}
	results := review.PostReplies(pctx, codeRunner, fb, answers)
	failed := 0
	for _, r := range results {
		t, _ := fb.ThreadByID(r.Thread)
		if r.Err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "codeforge: reply on %s:%d not posted: %v\n", t.Path, t.Line, r.Err)
			continue
		}
		fmt.Fprintf(os.Stderr, "codeforge: replied on %s:%d %s\n", t.Path, t.Line, r.URL)
	}
	if failed > 0 {
		return fmt.Errorf("%w: %d of %d", errRepliesFailed, failed, len(results))
	}
	fmt.Fprintf(os.Stderr, "codeforge: pushed and posted %d replies on PR #%d.\n", len(results), fb.Number)
	return nil
}

// respondAccordion lays out a respond pass: one row per file with its threads, then the review summaries.
func respondAccordion(ctx context.Context, prose string, fb review.Feedback, answers []review.Answer, st codeStyle) forms.Accordion {
	body := st
	if body.wrap {
		body.width = max(minCodeWidth, body.width-len(accordionIndent))
	}
	byThread := map[string]review.Answer{}
	for _, a := range answers {
		byThread[a.Thread] = a
	}
	code := threadCode(ctx, fb, body)
	var a forms.Accordion
	a.Intro = strings.TrimSpace(renderReview(prose, st, nil))
	var order []string
	threads := map[string][]review.Thread{}
	for _, t := range fb.Threads {
		if _, ok := threads[t.Path]; !ok {
			order = append(order, t.Path)
		}
		threads[t.Path] = append(threads[t.Path], t)
	}
	for _, path := range order {
		var b strings.Builder
		var verdicts []string
		for i, t := range threads[path] {
			if i > 0 {
				b.WriteString("\n")
			}
			ans, ok := byThread[t.ID]
			b.WriteString(threadBlock(ctx, t, ans, ok, body, code))
			if ok && ans.Verdict != "" {
				verdicts = append(verdicts, ans.Verdict)
			}
		}
		a.Items = append(a.Items, forms.Section{
			Title:   st.paint(st.pal.accent, path),
			Summary: verdictCounts(verdicts, len(threads[path])),
			Body:    strings.Trim(b.String(), "\n"),
		})
	}
	if len(fb.Reviews) > 0 {
		var b strings.Builder
		for _, rv := range fb.Reviews {
			b.WriteString(st.paint(st.pal.accent, "@"+rv.Author) + " " + st.paint(st.pal.dim, strings.ToLower(strings.ReplaceAll(rv.State, "_", " "))) + "\n")
			b.WriteString(renderReview(rv.Body, body, nil) + "\n")
		}
		a.Items = append(a.Items, forms.Section{
			Title:   st.paint(st.pal.bold, "Review summaries"),
			Summary: plural(len(fb.Reviews), "review"),
			Body:    strings.Trim(b.String(), "\n"),
		})
	}
	a.Accent, a.Dim = st.pal.accent, st.pal.dim
	return a
}

// threadBlock shows one thread: the code it is on (as the reviewers saw it), each comment, then the agent's verdict
// with its evidence, the fix's diff for this file and the draft reply.
func threadBlock(ctx context.Context, t review.Thread, ans review.Answer, answered bool, st codeStyle, code func(review.Thread, focus) string) string {
	var b strings.Builder
	width := 0
	if st.wrap {
		width = st.width
	}
	var said []string
	for _, c := range t.Comments {
		said = append(said, c.Body)
	}
	said = append(said, ans.Reply)
	fc := focus{Line: t.Line, End: t.Line}
	for _, s := range said {
		for _, m := range inlineCode.FindAllStringSubmatch(s, -1) {
			fc.Hints = append(fc.Hints, m[1])
		}
	}
	if card := code(t, fc); card != "" {
		b.WriteString(card + "\n")
	} else {
		place := fmt.Sprintf("%s:%d", t.Path, t.Line)
		if t.Outdated {
			place += " (outdated)"
		}
		b.WriteString(findingIndent + st.paint(st.pal.accent, place) + "\n\n")
	}
	for _, c := range t.Comments {
		lead := findingIndent + st.paint(st.pal.accent, "@"+c.Author) + " "
		b.WriteString(wrapSpans(st, width, lead, findingIndent, inlineSpans(oneParagraph(c.Body), st)...))
	}
	b.WriteString("\n")
	if !answered {
		b.WriteString(findingIndent + st.paint(st.pal.dim, "no verdict from the agent for this thread") + "\n")
		return b.String()
	}
	verdict := []span{{text: ans.Verdict + ":", code: verdictColor(ans.Verdict, st)}}
	verdict = append(verdict, inlineSpans(ans.Evidence, st)...)
	b.WriteString(wrapSpans(st, width, findingIndent, findingIndent, verdict...))
	if ans.Commit != "" {
		b.WriteString(fixDiff(ctx, ans.Commit, t.Path, st))
	}
	if ans.Reply != "" {
		lead := findingIndent + st.paint(st.pal.bold, "↳ reply") + " "
		b.WriteString(wrapSpans(st, width, lead, findingIndent+"  ", inlineSpans(oneParagraph(ans.Reply), st)...))
	}
	return b.String()
}

// threadCode reads each thread's file at the PR head the reviewers saw, once per file.
func threadCode(ctx context.Context, fb review.Feedback, st codeStyle) func(review.Thread, focus) string {
	files := map[string]string{}
	var paths []string
	for _, t := range fb.Threads {
		if !slices.Contains(paths, t.Path) && validRepoPath(t.Path) {
			paths = append(paths, t.Path)
		}
	}
	if fb.HeadSHA != "" {
		files = prefetch(ctx, paths, func(ctx context.Context, file string) ([]byte, error) {
			return codeRunner.Run(ctx, "git", "show", fb.HeadSHA+":"+file)
		})
	}
	return func(t review.Thread, fc focus) string {
		content, ok := files[t.Path]
		if !ok || t.Line < 1 || t.Outdated {
			return ""
		}
		return codeSnippetFocus(content, t.Path, fc, st)
	}
}

// fixDiff shows what commit changed in path: its subject, then the diff lines in the theme's added and removed
// colours, at most maxDiffLines of them.
func fixDiff(ctx context.Context, commit, path string, st codeStyle) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codeReadLimit)
	defer cancel()
	subject, err := codeRunner.Run(ctx, "git", "show", "-s", "--format=%h %s", commit)
	if err != nil {
		return findingIndent + st.paint(st.pal.dim, "fix "+commit+" is not in this checkout") + "\n"
	}
	var b strings.Builder
	b.WriteString(findingIndent + st.paint(st.pal.dim, "fixed in ") + st.paint(st.pal.accent, strings.TrimSpace(string(subject))) + "\n")
	diff, err := codeRunner.Run(ctx, "git", "show", "--format=", "--unified=2", commit, "--", path)
	if err != nil || strings.TrimSpace(string(diff)) == "" {
		return b.String()
	}
	shown := 0
	for _, l := range strings.Split(strings.TrimRight(string(diff), "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git"), strings.HasPrefix(l, "index "), strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "+++ "):
			continue
		case shown == maxDiffLines:
			b.WriteString(findingIndent + diffIndent + st.paint(st.pal.dim, "…") + "\n")
			return b.String()
		}
		line := cutColumns(strings.ReplaceAll(l, "\t", strings.Repeat(" ", tabWidth)), max(minCodeWidth, st.width-len(findingIndent+diffIndent)))
		switch {
		case strings.HasPrefix(l, "@@"):
			line = st.paint(st.pal.dim, line)
		case strings.HasPrefix(l, "+"):
			line = st.paint(st.pal.added, line)
		case strings.HasPrefix(l, "-"):
			line = st.paint(st.pal.removed, line)
		}
		b.WriteString(findingIndent + diffIndent + line + "\n")
		shown++
	}
	return b.String()
}

// verdictColor colours a verdict by what it means for the author: fixed, pushed back, answered, or set aside.
func verdictColor(v string, st codeStyle) string {
	switch v {
	case "valid", "partly valid":
		return st.pal.praise
	case "not valid":
		return st.pal.issue
	case "question":
		return st.pal.question
	}
	return st.pal.dim
}

// verdictCounts summarises a file's threads by verdict ("1 valid · 1 not valid"), naming those without one.
func verdictCounts(verdicts []string, threads int) string {
	order := []string{"valid", "partly valid", "not valid", "question", "out of scope", "outdated"}
	counts := map[string]int{}
	for _, v := range verdicts {
		counts[v]++
	}
	var parts []string
	for _, v := range order {
		if n := counts[v]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, v))
		}
	}
	if missing := threads - len(verdicts); missing > 0 {
		parts = append(parts, fmt.Sprintf("%d without verdict", missing))
	}
	return strings.Join(parts, " · ")
}

// oneParagraph joins a comment's lines, so it wraps as prose; code fences in a comment are kept as plain text.
func oneParagraph(s string) string { return strings.Join(strings.Fields(s), " ") }
