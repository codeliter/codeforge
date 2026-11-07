package src

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/codeliter/codeforge/src/effort"
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
func reviewCode(ctx context.Context, st codeStyle) codeSource {
	ref, local := review.DisplayOf(ctx).Source()
	switch {
	case local:
		wd, err := os.Getwd()
		if err != nil {
			return nil
		}
		root, err := guard.RepoRoot(ctx, wd)
		if err != nil {
			return nil
		}
		return func(file string, line int) string { return codeAround(root, file, line, st) }
	case ref != "":
		files := map[string]string{}
		return func(file string, line int) string {
			content, seen := files[file]
			if !seen {
				gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitShowLimit)
				out, err := review.ExecRunner{}.Run(gctx, "git", "show", ref+":"+file)
				cancel()
				if err == nil {
					content = string(out)
				}
				files[file] = content
			}
			return codeSnippet(content, file, line, st)
		}
	}
	return nil
}

// gitShowLimit bounds reading one file from the PR's head ref.
const gitShowLimit = 5 * time.Second

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
	return "\n" + st.paint(st.pal.dim, line) + "\n"
}
