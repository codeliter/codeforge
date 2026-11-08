package src

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/codeliter/codeforge/src/effort"
	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/review"
)

var (
	mdHeading = regexp.MustCompile(`^\s*#{1,6}\s+(.*?)\s*#*\s*$`)
	mdBullet  = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	mdFence   = regexp.MustCompile("^\\s*(```+|~~~+)\\s*([\\w+#.-]*)")
)

// findingIndent puts the lines that continue a finding under its text.
const findingIndent = "  "

// codeSource returns the code around line of file as codeSnippet renders it, or "" when it cannot be read.
type codeSource func(file string, line int) string

// renderReview lays out a review written in Markdown the way the gate report looks: Codeforge Steel colours, headings
// and Conventional Comment labels picked out, `code` and **bold** without their markers, fenced code highlighted, prose
// wrapped to the terminal, and the code around each finding that does not quote it already.
func renderReview(text string, st codeStyle, code codeSource) string {
	var out strings.Builder
	renderLines(&out, strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), st, code)
	return collapseBlank(out.String())
}

func renderLines(out *strings.Builder, lines []string, st codeStyle, code codeSource) {
	width := 0
	if st.wrap {
		width = st.width
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case mdFence.MatchString(l):
			end := fenceEnd(lines, i)
			out.WriteString(renderFence(lines[i:end], st))
			i = end - 1
		case strings.TrimSpace(l) == "":
			out.WriteString("\n")
		case mdHeading.MatchString(l):
			title := plainInline(mdHeading.FindStringSubmatch(l)[1])
			color := st.pal.bold
			if looksLikePath(title) {
				color = st.pal.accent
			}
			out.WriteString("\n" + st.paint(color, title) + "\n")
		default:
			if f, ok := review.ParseLabeled(l); ok {
				end := review.LabeledEnd(lines, i)
				// A finding followed by its own quoted code needs no second copy.
				c := code
				if j := nextNonBlank(lines, end); j < len(lines) && mdFence.MatchString(lines[j]) {
					c = nil
				}
				out.WriteString(renderFinding(f, lines[i+1:end], st, c, width))
				i = end - 1
				continue
			}
			lead, hang, rest := strings.Repeat(" ", indentOf(l)), "", strings.TrimSpace(l)
			if m := mdBullet.FindStringSubmatch(l); m != nil {
				lead, rest = m[1]+m[2]+" ", m[3]
			}
			hang = strings.Repeat(" ", len([]rune(lead)))
			out.WriteString(wrapSpans(st, width, lead, hang, inlineSpans(rest, st)...))
		}
	}
}

// renderFinding prints a finding's first line with its label coloured by kind and its place in the accent, the lines
// that continue it, and then the code around it unless the finding quotes code itself.
func renderFinding(f review.Labeled, more []string, st codeStyle, code codeSource, width int) string {
	var b strings.Builder
	spans := append([]span{{text: f.Label + ":", code: labelColor(f.Label, st)}, {text: fmt.Sprintf("%s:%d", f.Path, f.Line), code: st.pal.accent}},
		inlineSpans(strings.TrimLeft(f.Text, "—–-: "), st)...)
	b.WriteString("\n" + wrapFinding(st, width, spans...))
	quoted := false
	if len(more) > 0 {
		var inner strings.Builder
		sub := st
		if sub.wrap {
			sub.width = max(minCodeWidth, sub.width-len(findingIndent))
		}
		renderLines(&inner, dedentLines(more), sub, nil)
		for _, l := range strings.Split(strings.TrimRight(inner.String(), "\n"), "\n") {
			if strings.TrimSpace(l) == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString(findingIndent + l + "\n")
		}
		for _, l := range more {
			quoted = quoted || mdFence.MatchString(l)
		}
	}
	if !quoted && code != nil {
		b.WriteString(code(f.Path, f.Line))
	}
	return b.String()
}

// labelColor colours a Conventional Comment label by how much it asks of the author.
func labelColor(label string, st codeStyle) string {
	l := strings.ToLower(label)
	switch {
	case strings.HasPrefix(l, "issue"), strings.Contains(l, "blocking") && !strings.Contains(l, "non-blocking"):
		return st.pal.issue
	case strings.HasPrefix(l, "suggestion"), strings.HasPrefix(l, "todo"):
		return st.pal.suggestion
	case strings.HasPrefix(l, "question"):
		return st.pal.question
	case strings.HasPrefix(l, "praise"):
		return st.pal.praise
	}
	return st.pal.dim
}

// renderFence prints a fenced block's code highlighted for the language its fence names, behind a gutter.
func renderFence(block []string, st codeStyle) string {
	lang := mdFence.FindStringSubmatch(block[0])[2]
	body := block[1:]
	if n := len(body); n > 0 && mdFence.MatchString(body[n-1]) {
		body = body[:n-1]
	}
	code := make([]string, len(body))
	for i, l := range body {
		code[i] = cutColumns(strings.ReplaceAll(l, "\t", strings.Repeat(" ", tabWidth)), max(minCodeWidth, st.width-len("    │ ")))
	}
	code = trimSharedIndent(code)
	if st.color {
		code = highlightLang(lang, code, st.pal.code)
	}
	var b strings.Builder
	for _, l := range code {
		if st.color {
			b.WriteString("    " + st.paint(st.pal.dim, "│") + " " + l + "\n")
		} else {
			b.WriteString("    | " + l + "\n")
		}
	}
	return b.String()
}

// fenceEnd returns the index just past the fenced block that opens at i, or the end of lines when it never closes.
func fenceEnd(lines []string, i int) int {
	marker := strings.TrimSpace(mdFence.FindStringSubmatch(lines[i])[1])
	for j := i + 1; j < len(lines); j++ {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), marker) {
			return j + 1
		}
	}
	return len(lines)
}

// inlineSpans splits Markdown prose into spans: `code` in the inline colour and **bold** in bold, markers removed. A
// span that touches the one before it, with no space between, is glued to it.
func inlineSpans(s string, st codeStyle) []span {
	spans := splitInline(s, st)
	out := make([]span, 0, len(spans))
	for _, sp := range spans {
		if sp.text == "" {
			continue
		}
		if n := len(out); n > 0 {
			prev := out[n-1].text
			sp.glue = !strings.HasSuffix(prev, " ") && !strings.HasPrefix(sp.text, " ")
		}
		out = append(out, sp)
	}
	return out
}

func splitInline(s string, st codeStyle) []span {
	var spans []span
	for s != "" {
		tick, bold := strings.Index(s, "`"), strings.Index(s, "**")
		switch {
		case tick >= 0 && (bold < 0 || tick < bold):
			end := strings.Index(s[tick+1:], "`")
			if end < 0 {
				return append(spans, span{text: s})
			}
			spans = append(spans, span{text: s[:tick]}, span{text: s[tick+1 : tick+1+end], code: st.pal.inline})
			s = s[tick+2+end:]
		case bold >= 0:
			end := strings.Index(s[bold+2:], "**")
			if end < 0 {
				return append(spans, span{text: s})
			}
			spans = append(spans, span{text: s[:bold]}, span{text: s[bold+2 : bold+2+end], code: st.pal.bold})
			s = s[bold+4+end:]
		default:
			return append(spans, span{text: s})
		}
	}
	return spans
}

// plainInline drops the inline Markdown markers from s.
func plainInline(s string) string {
	return strings.NewReplacer("`", "", "**", "").Replace(s)
}

func looksLikePath(s string) bool {
	return s != "" && !strings.ContainsAny(s, " \t") && strings.ContainsAny(s, "./")
}

func nextNonBlank(lines []string, i int) int {
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	return i
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " \t")) }

// dedentLines removes the indentation the non-blank lines share.
func dedentLines(lines []string) []string {
	shared := -1
	for _, l := range lines {
		if strings.TrimSpace(l) != "" && (shared < 0 || indentOf(l) < shared) {
			shared = indentOf(l)
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= shared && shared > 0 {
			l = l[shared:]
		}
		out[i] = l
	}
	return out
}

// collapseBlank keeps at most one blank line in a row and none at the start.
func collapseBlank(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" && (len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == "") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

// reviewCode reads the code a review is about from where the review says it is: the working tree for local work, the
// PR's fetched head otherwise. With neither, findings are shown without code.
func reviewCode(ctx context.Context, st codeStyle, text string) codeSource {
	src := review.DisplayOf(ctx).Source()
	var read func(ctx context.Context, file string) ([]byte, error)
	switch {
	case src.Local:
		wd, err := os.Getwd()
		if err != nil {
			return nil
		}
		root, err := guard.RepoRoot(ctx, wd)
		if err != nil {
			return nil
		}
		return func(file string, line int) string { return codeAround(root, file, line, st) }
	case src.HeadRef != "":
		read = func(ctx context.Context, file string) ([]byte, error) {
			return codeRunner.Run(ctx, "git", "show", src.HeadRef+":"+file)
		}
	case src.SHA != "":
		// The PR is not in this checkout: read each file at its head commit from GitHub.
		repo := cmp.Or(src.Repo, "{owner}/{repo}")
		read = func(ctx context.Context, file string) ([]byte, error) {
			return codeRunner.Run(ctx, "gh", "api", "-H", "Accept: application/vnd.github.raw",
				"repos/"+repo+"/contents/"+escapePath(file)+"?ref="+url.QueryEscape(src.SHA))
		}
	default:
		return nil
	}
	files := prefetch(ctx, findingPaths(text), read)
	return func(file string, line int) string {
		content, ok := files[strings.TrimPrefix(file, "./")]
		if !ok {
			return ""
		}
		return codeSnippet(content, file, line, st)
	}
}

// codeRunner runs git and gh for reviewCode; tests replace it.
var codeRunner review.Runner = review.ExecRunner{}

// prefetch reads every file at once, each bounded by codeReadLimit; a file that cannot be read is left out.
func prefetch(ctx context.Context, paths []string, read func(context.Context, string) ([]byte, error)) map[string]string {
	var mu sync.Mutex
	var wg sync.WaitGroup
	files := map[string]string{}
	for _, p := range paths {
		wg.Go(func() {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codeReadLimit)
			defer cancel()
			if out, err := read(rctx, p); err == nil {
				mu.Lock()
				files[p] = string(out)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return files
}

// findingPaths lists the files the review's findings point at, once each.
func findingPaths(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if f, ok := review.ParseLabeled(l); ok && !seen[f.Path] && validRepoPath(f.Path) {
			seen[f.Path] = true
			out = append(out, f.Path)
		}
	}
	return out
}

// validRepoPath keeps a path the model wrote to a plain relative path inside the repository.
func validRepoPath(p string) bool {
	return filepath.IsLocal(p) && !strings.ContainsAny(p, "?#\\")
}

// escapePath escapes each segment of a repository path for a URL, keeping the slashes.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// reviewAccordion splits a review at its headings into the sections of an accordion: the text before the first heading
// is the intro, each heading a row whose summary counts its findings by label, and extra (the jurors' leads) a last
// section of its own. Bodies are laid out for the accordion's indent.
func reviewAccordion(text, extra string, st codeStyle, code codeSource) forms.Accordion {
	body := st
	if body.wrap {
		body.width = max(minCodeWidth, body.width-len(accordionIndent))
	}
	var a forms.Accordion
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start, title := 0, ""
	flush := func(end int) {
		chunk := lines[start:end]
		if title == "" && start == 0 {
			if intro := strings.TrimSpace(renderReview(strings.Join(chunk, "\n"), st, code)); intro != "" {
				a.Intro = intro
			}
			return
		}
		color := st.pal.bold
		if looksLikePath(title) {
			color = st.pal.accent
		}
		a.Items = append(a.Items, forms.Section{
			Title:   st.paint(color, title),
			Summary: findingCounts(chunk),
			Body:    strings.Trim(renderReview(strings.Join(chunk, "\n"), body, code), "\n"),
		})
	}
	inFence := false
	for i, l := range lines {
		if mdFence.MatchString(l) {
			inFence = !inFence
		}
		if inFence || !mdHeading.MatchString(l) {
			continue
		}
		flush(i)
		start, title = i+1, plainInline(mdHeading.FindStringSubmatch(l)[1])
	}
	flush(len(lines))
	if len(a.Items) == 0 && a.Intro != "" {
		a.Items = []forms.Section{{Title: st.paint(st.pal.bold, "Review"), Body: a.Intro}}
		a.Intro = ""
	}
	if strings.TrimSpace(extra) != "" {
		a.Items = append(a.Items, forms.Section{
			Title: st.paint(st.pal.issue, "Unconfirmed blocking leads"),
			Body:  strings.Trim(renderReview(extra, body, nil), "\n"),
		})
	}
	a.Accent, a.Dim = st.pal.accent, st.pal.dim
	return a
}

// accordionIndent is how far the accordion indents an open section's body.
const accordionIndent = "    "

// labelOrder is the order findingCounts lists labels in: what asks most of the author first.
var labelOrder = []string{"issue", "suggestion", "question", "todo", "nitpick", "nit", "chore", "thought", "note", "optional", "fyi", "praise"}

// findingCounts summarises a section's findings by label ("1 issue · 2 suggestions"), or counts its points when it has
// no labelled findings.
func findingCounts(lines []string) string {
	counts := map[string]int{}
	points := 0
	for _, l := range lines {
		if f, ok := review.ParseLabeled(l); ok {
			kind, _, _ := strings.Cut(strings.ToLower(f.Label), " ")
			counts[strings.TrimSuffix(kind, ":")]++
			continue
		}
		if m := mdBullet.FindStringSubmatch(l); m != nil && m[1] == "" {
			points++
		}
	}
	var parts []string
	for _, k := range labelOrder {
		if n := counts[k]; n > 0 {
			parts = append(parts, plural(n, k))
		}
	}
	if len(parts) == 0 && points > 0 {
		parts = append(parts, plural(points, "point"))
	}
	return strings.Join(parts, " · ")
}

func plural(n int, word string) string {
	if n == 1 || word == "fyi" || word == "praise" {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// accordionRecap is what stays in the terminal after the accordion closes: the intro and each section's row.
func accordionRecap(a forms.Accordion, st codeStyle) string {
	var b strings.Builder
	if a.Intro != "" {
		b.WriteString(a.Intro + "\n\n")
	}
	for _, s := range a.Items {
		b.WriteString("  " + s.Title)
		if s.Summary != "" {
			b.WriteString("  " + st.paint(st.pal.dim, s.Summary))
		}
		b.WriteString("\n")
	}
	if a.Outro != "" {
		b.WriteString("\n" + a.Outro + "\n")
	}
	return b.String()
}

// codeReadLimit bounds reading one file from the PR's head, locally or from GitHub.
const codeReadLimit = 8 * time.Second

// stdoutStyled reports whether the review is printed for a person: standard output is a terminal.
var stdoutStyled = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// runFooter is the dim line under a review: its steps, cost and time, as Empryo prints them in a headless run.
func runFooter(raw []byte, st codeStyle) string {
	var res effort.EmpryoResult
	if json.Unmarshal(raw, &res) != nil || res.Steps == 0 {
		return ""
	}
	const msPerSecond = 1000
	line := fmt.Sprintf("%d steps, $%.4f, %.1fs", res.Steps, res.Cost, float64(res.DurationMS)/msPerSecond)
	return st.paint(st.pal.dim, line)
}
