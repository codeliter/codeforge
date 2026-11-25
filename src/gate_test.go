package src

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/review"
)

func TestReport_ShowsTheCodeAroundEachFindingByFile(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "pkg"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "pkg", "a.go"), []byte("package pkg\n\nfunc F(p *T) int {\n\treturn p.n\n}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "b.go"), []byte("package b\n"), 0o600))
	res := review.JuryResult{
		Blocking: []review.Finding{
			{File: "pkg/a.go", Line: 4, What: "nil deref when p is nil"},
			{File: "b.go", Line: 1, What: "wrong package"},
			{File: "../outside.go", Line: 1, What: "escapes"},
		},
		Should:     []review.Finding{{File: "pkg/a.go", Line: 1, What: "add a doc comment"}},
		Unverified: []review.Finding{{File: "pkg/a.go", Line: 99, What: "made up"}},
	}
	var out strings.Builder
	report(t.Context(), &out, repo, res)
	got := out.String()
	require.Contains(t, got, "Blocking:\n\n../outside.go\n- ../outside.go:1 escapes\n\nb.go\n- b.go:1 wrong package\n  > 1 | package b\n")
	require.Contains(t, got, "pkg/a.go\n- pkg/a.go:4 nil deref when p is nil\n    1 | package pkg\n    2 | \n    3 | func F(p *T) int {\n  > 4 |     return p.n\n    5 | }\n")
	require.Contains(t, got, "Should:\n\npkg/a.go\n- pkg/a.go:1 add a doc comment\n  > 1 | package pkg\n")
	require.Contains(t, got, "- pkg/a.go:99 made up\n")
	require.NotContains(t, got, "99 |", "a dropped finding shows no code")
	require.NotContains(t, got, "\x1b[", "no colour when the output is not a terminal")
}

// deepFile is a Go file whose line 55 sits deep inside func run, which starts at line 3 and is too long to show whole.
func deepFile(t *testing.T) string {
	return funcFile(t, 52)
}

// funcFile is a Go file with func run from line 3 and a for loop from line loop; the return inside it is on loop+3.
func funcFile(t *testing.T, loop int) string {
	t.Helper()
	repo := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n\nfunc run(items []string) error {\n")
	for i := 4; i < loop; i++ {
		b.WriteString("\tstep()\n")
	}
	b.WriteString("\tfor _, it := range items {\n\t\tif it == \"\" {\n\t\t\tcontinue\n")
	b.WriteString("\t\t\treturn errors.New(\"" + strings.Repeat("y", 100) + "\")\n\t\t}\n\t}\n\treturn nil\n}\n")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "p.go"), []byte(b.String()), 0o600))
	return repo
}

func TestCodeAround_ShowsTheEnclosingFunctionAndEnoughContext(t *testing.T) {
	repo := deepFile(t)
	plain := codeAround(repo, "p.go", 55, codeStyle{width: 60})
	require.True(t, strings.HasPrefix(plain, "     3 | func run(items []string) error {\n       ⋮\n    49 |     step()\n"),
		"the function the line is in heads the snippet, then a gap, then six lines before: %q", plain)
	require.Contains(t, plain, "  > 55 |             return errors.New(\"y")
	require.True(t, strings.HasSuffix(plain, "    59 | }\n"), "four lines after")
	for _, l := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
		require.LessOrEqual(t, len([]rune(l)), 60, "every line fits the width: %q", l)
	}

	near := codeAround(repo, "p.go", 6, codeStyle{width: 80})
	require.True(t, strings.HasPrefix(near, "     1 | package p\n"), "a header inside the window needs no gap: %q", near)
	require.NotContains(t, near, "⋮")
}

// telemetrySrc is common/go/telemetry/telemetry.go from ecosia/core#32569, lines 186 to 198: the review cited 192
// while its comment is about sdktrace.AlwaysSample() on 194.
const telemetrySrc = `// noExportTraceProvider creates a tracer provider that generates W3C trace/span
// IDs for log correlation without exporting spans. No span processor is attached;
// AlwaysSample ensures IsRecording() == true.
func noExportTraceProvider(serviceName string) (*sdktrace.TracerProvider, error) {
	res := resource.NewSchemaless(semconv.ServiceName(serviceName))

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	return provider, nil
}`

func telemetryLines() []string {
	pad := strings.Repeat("\n", 185) // so the file's line numbers match the PR
	return strings.Split(pad+telemetrySrc, "\n")
}

func TestMarkedLines_TheCodeTheCommentNamesIsMarked(t *testing.T) {
	all := telemetryLines()
	got := markedLines(all, focus{Line: 192, Hints: []string{"sdktrace.AlwaysSample()", "traceparent", "ParentBased(AlwaysSample())", "IsRecording() == true"}})
	require.Equal(t, map[int]bool{192: true, 194: true, 188: true}, got,
		"the cited line, the AlwaysSample call, and the doc line that says IsRecording() == true; names that are not in the code mark nothing")

	require.Equal(t, map[int]bool{192: true, 193: true, 194: true, 195: true}, markedLines(all, focus{Line: 192, End: 195}), "a cited range is marked whole")
	require.Equal(t, map[int]bool{192: true}, markedLines(all, focus{Line: 192, Hints: []string{"err", "sdk", "a"}}), "short names say nothing")
	require.Equal(t, map[int]bool{192: true}, markedLines(all, focus{Line: 192, Hints: []string{"sdktrace"}}), "a name on more than three lines says nothing")
}

func TestCodeSnippetFocus_ShowsAndMarksTheNamedCall(t *testing.T) {
	content := strings.Join(telemetryLines(), "\n")
	got := codeSnippetFocus(content, "telemetry.go", focus{Line: 192, Hints: []string{"sdktrace.AlwaysSample()"}}, codeStyle{width: 100})
	require.Contains(t, got, "  > 192 |     provider := sdktrace.NewTracerProvider(\n")
	require.Contains(t, got, "  > 194 |         sdktrace.WithSampler(sdktrace.AlwaysSample()),\n")
	require.Contains(t, got, "    193 |         sdktrace.WithResource(res),\n", "lines between stay unmarked")

	card := codeSnippetFocus(content, "telemetry.go", focus{Line: 192, End: 195}, codeStyle{color: true, width: 100, pal: paletteFor("dark")})
	require.Contains(t, card, " telemetry.go:192-195", "the title names the cited range")
	require.Equal(t, 4, strings.Count(card, "▌"), "every cited line has the accent bar")
}

func TestCodeAround_ShowsAShortFunctionWhole(t *testing.T) {
	repo := funcFile(t, 22) // func run is lines 3 to 29
	plain := codeAround(repo, "p.go", 25, codeStyle{width: 200})
	require.True(t, strings.HasPrefix(plain, "     3 | func run(items []string) error {\n     4 |     step()\n"), "from the header down, no gap: %q", plain)
	require.NotContains(t, plain, "⋮")
	require.True(t, strings.HasSuffix(plain, "    29 | }\n"), "down to the function's closing brace")
}

func TestCodeAround_ALongBlockBelowTheFindingIsShownToItsEnd(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\nfunc run() {\n")
	for range 60 {
		b.WriteString("\tstep()\n")
	}
	b.WriteString("\tif ok {\n") // 64
	for range 12 {
		b.WriteString("\t\tstep()\n") // 65-76
	}
	b.WriteString("\t}\n\tdone()\n}\n") // 77-79
	all := strings.Split(b.String(), "\n")
	from, to := enclosingSpan(all, 65, 65)
	require.Equal(t, [2]int{64, 77}, [2]int{from, to}, "the if fits, the 77-line function does not")
	got := codeSnippetFocus(b.String(), "p.go", focus{Line: 65}, codeStyle{width: 200})
	require.True(t, strings.HasSuffix(got, "    77 |     }\n"), "the block runs past four lines after; it is shown to its end: %q", got)
}

func TestEnclosingSpan_TopLevelAndPython(t *testing.T) {
	require.Equal(t, [2]int{0, 0}, func() [2]int { a, b := enclosingSpan([]string{"a", "b"}, 2, 2); return [2]int{a, b} }())
	py := strings.Split("class A:\n    def f(self):\n        x = 1\n        if x:\n            return x\n    def g(self):\n        pass\n", "\n")
	a, b := enclosingSpan(py, 5, 5)
	require.Equal(t, [2]int{2, 5}, [2]int{a, b}, "the method, ending before the next one")
	require.False(t, closesBlock("endpoint := x"))
	require.True(t, closesBlock("end"))
}

func TestEnclosingHeader(t *testing.T) {
	py := strings.Split("class A:\n    def f(self):\n        x = 1\n        if x:\n            return x\n", "\n")
	require.Equal(t, 2, enclosingHeader(py, 5), "the method, not the if")
	tf := strings.Split("resource \"aws_s3_bucket\" \"b\" {\n  bucket = \"x\"\n  acl    = \"public-read\"\n}\n", "\n")
	require.Equal(t, 1, enclosingHeader(tf, 3))
	require.Equal(t, 0, enclosingHeader(strings.Split("a\nb\n", "\n"), 2), "top level has no header")
}

func TestCodeAround_DrawsACardOnATerminal(t *testing.T) {
	repo := deepFile(t)
	pal := paletteFor("dark")
	card := codeAround(repo, "p.go", 55, codeStyle{color: true, width: 100, pal: pal})
	lines := strings.Split(strings.TrimRight(card, "\n"), "\n")
	require.Contains(t, lines[0], pal.cardTitle, "a title row on its own background")
	require.Contains(t, lines[0], pal.accent+" p.go:55")
	require.Contains(t, card, pal.cardLine+pal.accent+"▌55", "the finding's line is raised and marked")
	require.Contains(t, card, "\x1b[1m\x1b[38;2;126;168;201mfunc", "code is highlighted in Steel")
	for i, l := range lines {
		require.Equal(t, 2+100-2, ansi.StringWidth(l), "every row of the card has the same width: row %d", i)
		require.True(t, strings.HasSuffix(l, ansiReset), "the background ends with the row")
	}
	require.Len(t, lines, 1+2+11+1, "title, header and gap, lines 49 to 59, and the rest of line 55, which is too long for one row")
	require.Contains(t, card, pal.cardLine+pal.accent+"▌  "+ansiReset, "the rest of a long line sits under it, marked the same and without a number")
	require.NotContains(t, ansi.Strip(card), "…", "nothing of the code is cut off")
}

func TestCutColumns_CountsCharacters(t *testing.T) {
	require.Equal(t, "ééé", cutColumns("ééé", 3), "three accented characters fit three columns")
	require.Equal(t, "éé…", cutColumns("éééé", 3))
	require.Equal(t, []string{"ééé", "é"}, wrapColumns("éééé", 3), "a long line is shown whole, over several rows")
	require.Equal(t, []string{""}, wrapColumns("", 3))
	require.Equal(t, []string{"\x1b[31mab" + ansiReset, "\x1b[31mc" + ansiReset + "d"}, wrapStyled("\x1b[31mabc"+ansiReset+"d", 2), "the colour in force carries on to the next row")
	require.Equal(t, []string{"ab"}, wrapStyled("ab", 2))
	require.Equal(t, []string{"\u754c\u754c", "\u754c"}, wrapColumns("\u754c\u754c\u754c", 4), "a wide character takes two columns")
	require.Equal(t, []string{"\u754c" + ansiReset, "\u754c"}, wrapStyled("\u754c\u754c", 3))
	require.Equal(t, []string{"e\u0301a", "b"}, wrapColumns("e\u0301ab", 2), "a character is never split from its combining mark")
}

func TestWrapFinding_StaysWithinTheWidth(t *testing.T) {
	st := codeStyle{color: true, pal: paletteFor("dark")}
	what := strings.Repeat("word ", 30)
	got := wrapFinding(st, 40, span{text: "a/b.go:12", code: st.pal.accent}, span{text: what}, span{text: "(breaks: once)", code: st.pal.dim})
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	require.Greater(t, len(lines), 3, "a long finding wraps")
	plain := strings.NewReplacer(st.pal.accent, "", st.pal.dim, "", ansiReset, "").Replace(got)
	for i, l := range strings.Split(strings.TrimSuffix(plain, "\n"), "\n") {
		require.LessOrEqual(t, len([]rune(l)), 40, "line %d: %q", i, l)
		if i > 0 {
			require.True(t, strings.HasPrefix(l, "  "), "continuations hang under the text: %q", l)
		}
	}
	require.True(t, strings.HasPrefix(plain, "- a/b.go:12 word"))
	require.True(t, strings.HasSuffix(plain, "(breaks: once)\n"))

	one := wrapFinding(codeStyle{}, 0, span{text: "a.go:1"}, span{text: what})
	require.Equal(t, 1, strings.Count(one, "\n"), "piped output keeps a finding on one line")
}

func TestReport_OneBlankLineBetweenBlocks(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n\nvar x = 1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "b.go"), []byte("package b\n"), 0o600))
	var out strings.Builder
	report(t.Context(), &out, repo, review.JuryResult{
		Blocking: []review.Finding{{File: "a.go", Line: 1, What: "one"}, {File: "a.go", Line: 3, What: "two"}, {File: "b.go", Line: 1, What: "three"}},
		Should:   []review.Finding{{File: "b.go", Line: 1, What: "four"}},
	})
	require.NotContains(t, out.String(), "\n\n\n", "never two blank lines in a row")
	require.Contains(t, out.String(), "  > 1 | package a\n    2 | \n    3 | var x = 1\n\n- a.go:3 two\n", "findings of one file are a blank line apart")
	require.Contains(t, out.String(), "  > 1 | package b\n\nShould:\n")
}

func TestPaletteFor_UsesTheSteelThemes(t *testing.T) {
	dark, light := paletteFor("dark"), paletteFor("light")
	require.Equal(t, "\x1b[1m\x1b[38;2;126;168;201m", dark.accent, "brand #7ea8c9")
	require.Equal(t, "\x1b[1m\x1b[38;2;63;111;150m", light.accent, "light brand #3f6f96")
	require.Equal(t, "\x1b[38;2;116;128;141m", dark.dim, "textMuted #74808d")
	require.NotNil(t, dark.code)
	require.NotNil(t, light.code)
	require.Empty(t, fg("#12345"), "a malformed colour is no colour")
}

func TestReviewerFor_LightModelOnlyForLightTiers(t *testing.T) {
	rc := config.ReviewConfig{Model: "strong/reviewer", Light: "cheap/reviewer"}
	require.Equal(t, "cheap/reviewer", reviewerFor(review.TierDocs, rc))
	require.Equal(t, "strong/reviewer", reviewerFor(review.TierSmall, rc), "small code changes keep the normal reviewer")
	require.Equal(t, "strong/reviewer", reviewerFor(review.TierStandard, rc))
	require.Equal(t, "strong/reviewer", reviewerFor(review.TierRisky, rc))
	require.Equal(t, "strong/reviewer", reviewerFor(review.TierSmall, config.ReviewConfig{Model: "strong/reviewer"}), "no light model set: nothing changes")
}

func TestCodeSnippet_AddedAndRemovedLinesAreToldApart(t *testing.T) {
	content := "package p\n\nfunc run() {\n\tstart()\n\tretry()\n\tstop()\n}\n"
	fc := focus{Line: 5, Added: map[int]bool{5: true}, Removed: map[int][]string{5: {"\tsend()", "\tlog()"}, 8: {"// gone"}}}

	plain := codeSnippetFocus(content, "p.go", fc, codeStyle{width: 100})
	require.Contains(t, plain, "    4 |       start()\n      | -     send()\n      | -     log()\n  > 5 | +     retry()\n    6 |       stop()\n",
		"what was removed stands where it was, without a number, before the line that replaced it")
	require.True(t, strings.HasSuffix(plain, "    7 |   }\n      | - // gone\n"), "lines removed at the end of the file come after its last line")

	unknown := codeSnippetFocus(content, "p.go", focus{Line: 5}, codeStyle{width: 100})
	require.Contains(t, unknown, "  > 5 |     retry()\n", "without the change to go by, no line carries a sign")
	require.NotContains(t, unknown, "send()")

	pal := paletteFor("dark")
	require.NotEmpty(t, pal.addedBg)
	require.NotEmpty(t, pal.removedBg)
	require.NotEqual(t, pal.addedBg, pal.removedBg)
	card := codeSnippetFocus(content, "p.go", fc, codeStyle{width: 100, color: true, pal: pal})
	var added, removed, kept string
	for _, l := range strings.Split(card, "\n") {
		switch text := ansi.Strip(l); {
		case strings.Contains(text, "retry()"):
			added = l
		case strings.Contains(text, "send()"):
			removed = l
		case strings.Contains(text, "stop()"):
			kept = l
		}
	}
	require.Contains(t, added, pal.addedBg, "an added line is on its own background")
	require.Contains(t, added, pal.added+"+")
	require.Contains(t, ansi.Strip(added), "5  + ")
	require.Contains(t, removed, pal.removedBg, "and a removed one on another")
	require.Contains(t, removed, pal.removed+"-")
	require.NotContains(t, kept, pal.addedBg)
	require.NotContains(t, kept, pal.removedBg)

	// A removed line that opens a string it does not close colours nothing but its own run.
	nums, rows := []int{1, removedRow, 2}, []string{"a = 1", `s = """open`, "b = 2"}
	apart := highlightRows("p.py", nums, rows, pal.code)
	alone := highlight("p.py", []string{"a = 1", "b = 2"}, pal.code)
	require.Equal(t, alone, []string{apart[0], apart[2]}, "the code now is coloured as if the removed lines were not there")
	require.NotEqual(t, alone[1], highlight("p.py", rows, pal.code)[2], "which colouring them as one text would not do")
	require.Equal(t, `s = """open`, ansi.Strip(apart[1]))

	long := make([]string, maxRemovedRun+3)
	for i := range long {
		long[i] = "x()"
	}
	run := removedRun(long)
	require.Len(t, run, maxRemovedRun+1)
	require.Equal(t, "… 3 more lines removed", run[maxRemovedRun], "a long removal is cut, and says by how much")
}

func TestCodeSnippet_EveryPlaceTheChangeTouchesInTheFileIsShown(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\nfunc first() {\n\tone()\n}\n\n")
	for i := range 40 {
		fmt.Fprintf(&b, "// filler %d\n", i)
	}
	b.WriteString("func second() {\n\ttwo()\n\tthree()\n}\n\n")
	for i := range 40 {
		fmt.Fprintf(&b, "// more %d\n", i)
	}
	b.WriteString("func third() {\n\tfour()\n}\n")
	content := b.String() // first() is lines 3-5, second() 47-50, third() 92-94
	fc := focus{Line: 4, Added: map[int]bool{4: true, 49: true, 93: true}, Removed: map[int][]string{49: {"\told()"}}}
	st := codeStyle{width: 100}

	one := codeSnippetFocus(content, "p.go", fc, st)
	require.NotContains(t, one, "three()", "a finding shows its own place")

	fc.Whole = true
	whole := codeSnippetFocus(content, "p.go", fc, st)
	for _, want := range []string{"  >  4 | +     one()", "47 |   func second() {", "| -     old()", "49 | +     three()", "93 | +     four()"} {
		require.Contains(t, whole, want, "a stop of a tour shows every place of its file the change touches")
	}
	require.Equal(t, 2, strings.Count(whole, "⋮"), "with what lies between them left out")
	require.Less(t, strings.Index(whole, "one()"), strings.Index(whole, "three()"), "in the file's order")
	require.NotContains(t, whole, "In this file", "nothing is left out, so nothing is said")

	fc.Stops = []int{48}
	apart := codeSnippetFocus(content, "p.go", fc, st)
	require.NotContains(t, apart, "three()", "a place another stop is about is read there")
	require.Contains(t, apart, "four()")
	require.Contains(t, apart, "In this file: 1 more change at a stop of its own.")

	long := map[int]bool{4: true}
	for n := 9; n <= 44; n++ {
		long[n] = true
	}
	for n := 52; n <= 84; n++ {
		long[n] = true
	}
	for n := 92; n <= 94; n++ {
		long[n] = true
	}
	cut := codeSnippetFocus(content, "p.go", focus{Line: 4, Whole: true, Added: long}, st)
	require.Contains(t, cut, "1 more change not shown", "what does not fit is counted, not dropped in silence")

	// The lines between places that are joined, and their headers, count toward what is shown.
	spread := focus{Line: 4, Added: map[int]bool{4: true}}
	for n := 100; n <= 250; n += 10 {
		spread.Added[n] = true
	}
	many := strings.Repeat("x()\n", 500)
	alone := codeSnippetFocus(many, "p.txt", spread, st)
	spread.Whole = true
	with := codeSnippetFocus(many, "p.txt", spread, st)
	extra := strings.Count(with, "\n") - strings.Count(alone, "\n") - strings.Count(with, "⋮") - strings.Count(with, "In this file")
	require.LessOrEqual(t, extra, maxOtherLines, "the other places of a file take no more lines than they are given")
	require.Greater(t, extra, maxOtherLines-minOtherLines)
	require.Contains(t, with, "more changes not shown")
	// The measure of what a snippet adds to its window: rows drawn, without the gap markers and the closing note.
	added := func(content string, fc focus) int {
		fc.Whole, fc.Reach = false, false
		window := codeSnippetFocus(content, "p.txt", fc, st)
		fc.Whole = true
		all := codeSnippetFocus(content, "p.txt", fc, st)
		return strings.Count(all, "\n") - strings.Count(window, "\n") - strings.Count(all, "⋮") + strings.Count(window, "⋮") - strings.Count(all, "In this file")
	}
	near := focus{Line: 20, Added: map[int]bool{}}
	for n := 10; n <= 40; n++ {
		near.Added[n] = true
	}
	for n := 100; n <= 200; n++ {
		near.Added[n] = true
	}
	require.LessOrEqual(t, added(many, near), maxOtherLines, "a place read with the window takes from what the others get")
	late := focus{Line: 200, Added: map[int]bool{}}
	for n := 10; n <= 100; n++ {
		late.Added[n] = true
	}
	for n := 190; n <= 220; n++ {
		late.Added[n] = true
	}
	require.LessOrEqual(t, added(many, late), maxOtherLines, "and gets no more than the others leave it")
	thick := focus{Line: 20, Added: map[int]bool{}, Removed: map[int][]string{}}
	for n := 10; n <= 40; n++ {
		thick.Added[n], thick.Removed[n] = true, slices.Repeat([]string{"old()"}, 12)
	}
	require.LessOrEqual(t, added(many, thick), maxOtherLines, "with the removed lines it shows")
	gone := focus{Line: 4, Added: map[int]bool{4: true}, Removed: map[int][]string{}}
	for n := 100; n <= 200; n += 10 {
		gone.Removed[n] = slices.Repeat([]string{"old()"}, 12)
	}
	require.LessOrEqual(t, added(many, gone), maxOtherLines, "removed lines are rows too")
	// A fragment that shows none of the change is not shown: the change is counted as left out.
	bare := focus{Line: 4, Whole: true, Added: map[int]bool{4: true}, Removed: map[int][]string{100: slices.Repeat([]string{"old()"}, 12)}}
	for n := 20; n <= 85; n++ {
		bare.Added[n] = true
	}
	unseen := codeSnippetFocus(many, "p.txt", bare, st)
	require.NotContains(t, unseen, " 97 | ")
	require.Contains(t, unseen, "1 more change not shown")
	// What is said of a place is what is drawn of it: none of its changed lines, or some.
	far := focus{Line: 200, Whole: true, Added: map[int]bool{207: true}}
	for n := 20; n <= 93; n++ {
		far.Added[n] = true
	}
	hidden := codeSnippetFocus(many, "p.txt", far, st)
	require.NotContains(t, hidden, "207 | +")
	require.Contains(t, hidden, "1 more change not shown")
	split := focus{Line: 200, Whole: true, Added: map[int]bool{193: true, 199: true}, Removed: map[int][]string{205: slices.Repeat([]string{"old()"}, 12)}}
	for n := 20; n <= 85; n++ {
		split.Added[n] = true
	}
	part := codeSnippetFocus(many, "p.txt", split, st)
	require.Contains(t, part, "193 | +")
	require.NotContains(t, part, "old()")
	require.Contains(t, part, "a long change is cut short", "a part of a place left out is said, whatever else of it is shown")
	// A change of any length is cut without reading it over and over.
	vast := focus{Line: 50000, Reach: true, Added: map[int]bool{}}
	for n := 1; n <= 100000; n++ {
		vast.Added[n] = true
	}
	began := time.Now()
	require.Contains(t, codeSnippetFocus(strings.Repeat("x()\n", 100000), "p.txt", vast, st), "a long change is cut short")
	require.Less(t, time.Since(began), 5*time.Second)
	// A place whose rows do not all fit is shown as far as they do, whichever end it is cut at.
	for name, line := range map[string]int{"cut at its end": 4, "cut at its start": 131} {
		dense := focus{Line: line, Added: map[int]bool{line: true}, Removed: map[int][]string{}}
		for n := 100; n <= 129; n++ {
			dense.Added[n], dense.Removed[n] = true, []string{"a()", "b()", "c()"}
		}
		got := added(many, dense)
		require.LessOrEqual(t, got, maxOtherLines, name)
		require.Greater(t, got, maxOtherLines-minOtherLines, name)
	}
	every := map[int]bool{}
	for n := 1; n <= 400; n++ {
		every[n] = true
	}
	mid := codeSnippetFocus(strings.Repeat("x()\n", 400), "p.txt", focus{Line: 200, Reach: true, Added: every}, st)
	require.LessOrEqual(t, strings.Count(mid, "\n")-strings.Count(codeSnippetFocus(strings.Repeat("x()\n", 400), "p.txt", focus{Line: 200, Added: every}, st), "\n"), maxOtherLines+2)
	require.Contains(t, mid, "190 | + x()", "a long change is shown around the lines cited, not from its far start")
	require.NotContains(t, mid, "  1 | + x()")
	require.Contains(t, mid, "215 | + x()", "on both sides of them")
	require.Contains(t, mid, "a long change is cut short")

	// A later stop in the file shows the whole of the change it is in, and no other.
	run := map[int]bool{93: true}
	for n := 10; n <= 40; n++ {
		run[n] = true
	}
	reach := codeSnippetFocus(content, "p.go", focus{Line: 38, Reach: true, Added: run}, st)
	require.Contains(t, reach, "10 | + // filler 3", "from where the change it cites starts")
	require.NotContains(t, reach, "four()")
	all := map[int]bool{}
	for n := 1; n <= 400; n++ {
		all[n] = true
	}
	bounded := codeSnippetFocus(strings.Repeat("x()\n", 400), "p.txt", focus{Line: 200, Reach: true, Added: all}, st)
	require.Less(t, strings.Count(bounded, "\n"), maxOtherLines+30, "a change without end is not shown without end")
	require.Contains(t, bounded, "a long change is cut short")

	card := ansi.Strip(codeSnippetFocus(content, "p.go", fc, codeStyle{width: 100, color: true, pal: paletteFor("dark")}))
	require.Contains(t, card, "In this file: 1 more change at a stop of its own.")
	// A narrow card says the whole note, over several rows.
	narrow := ansi.Strip(codeSnippetFocus(content, "p.go", focus{Line: 4, Whole: true, Stops: []int{93}, Added: long}, codeStyle{width: 40, color: true, pal: paletteFor("dark")}))
	note := func(card string) string {
		_, after, found := strings.Cut(card, "In this file")
		require.True(t, found)
		return "In this file" + strings.Join(strings.Fields(after), " ")
	}
	said := note(narrow)
	require.Equal(t, "In this file: 1 more change at a stop of its own.", said)
	both := focus{Line: 200, Whole: true, Added: map[int]bool{207: true}}
	for n := 20; n <= 120; n++ {
		both.Added[n] = true
	}
	narrow = ansi.Strip(codeSnippetFocus(strings.Repeat("x()\n", 500), "p.txt", both, codeStyle{width: 30, color: true, pal: paletteFor("dark")}))
	said = note(narrow)
	require.Equal(t, "In this file: 1 more change not shown; a long change is cut short.", said)
	require.Equal(t, []string{"ab cd", "efghi", "jk"}, wrapWords("ab cd efghijk", 5))

	require.Equal(t, []lineRange{{1, 9}, {20, 22}}, joinRanges([]lineRange{{20, 22}, {5, 9}, {1, 2}}), "ranges a few lines apart are joined")
}

func TestCodeSnippet_ADeclarationTheChangeAddsIsShownWhole(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\nfunc old() {\n\tstep()\n}\n\nfunc added() error {\n")
	for range 60 {
		b.WriteString("\tstep()\n")
	}
	b.WriteString("\treturn nil\n}\n\nfunc after() {}\n")
	content := b.String()
	all := map[int]bool{}
	for n := 7; n <= 69; n++ {
		all[n] = true
	}
	st := codeStyle{width: 100}

	whole := codeSnippetFocus(content, "p.go", focus{Line: 40, Added: all}, st)
	require.Contains(t, whole, " 7 | + func added() error {", "the new function is shown from its first line, each line marked as added")
	require.Contains(t, whole, "69 | + }", "to its last")
	require.NotContains(t, whole, "⋮", "with nothing left out")
	require.NotContains(t, whole, "func old()")
	require.NotContains(t, whole, "func after()")

	onHeader := codeSnippetFocus(content, "p.go", focus{Line: 7, Added: all}, st)
	require.Contains(t, onHeader, "69 | + }", "a finding on the declaration's own line shows it whole too")

	unknown := codeSnippetFocus(content, "p.go", focus{Line: 40}, st)
	require.Contains(t, unknown, "⋮", "without the change to go by, a long function is shown as a window")
	require.NotContains(t, unknown, "69 | }")

	delete(all, 20)
	changed := codeSnippetFocus(content, "p.go", focus{Line: 40, Added: all}, st)
	require.NotContains(t, changed, "69 | + }", "a function the change only touches is not new, and is shown as a window")

	long := map[int]bool{}
	for n := 1; n <= 400; n++ {
		long[n] = true
	}
	huge := "func big() {\n" + strings.Repeat("\tstep()\n", 300) + "}\n"
	require.NotContains(t, codeSnippetFocus(huge, "p.go", focus{Line: 150, Added: long}, st), "func big()", "one too long to read at once is not")
}

func TestAddedDeclaration_FindsTheWholeDeclarationHoweverItIsWritten(t *testing.T) {
	added := func(n int) map[int]bool {
		m := map[int]bool{}
		for i := 1; i <= n; i++ {
			m[i] = true
		}
		return m
	}
	lines := func(s string) []string { return strings.Split(s, "\n") }

	goFn := lines("func run(\n    a int,\n    b int,\n) error {\n    data := load()\n    functionCall()\n    typeName := load()\n    return nil\n}")
	from, to := addedDeclaration("a.go", goFn, 5, 5, added(9))
	require.Equal(t, []int{1, 9}, []int{from, to}, "a statement that begins like a keyword is not a declaration; parameters over several lines are part of the header")
	from, to = addedDeclaration("a.go", goFn, 1, 1, added(9))
	require.Equal(t, []int{1, 9}, []int{from, to}, "a finding on the first line of a multi-line header shows the function, not the signature")

	js := lines("function run()\n{\n    step();\n}")
	from, to = addedDeclaration("a.js", js, 3, 3, added(4))
	require.Equal(t, []int{1, 4}, []int{from, to}, "a brace on its own line opens the body")

	py := lines("class A:\n    def old(self):\n        pass\n\n    def new(self):\n        x = 1\n        return x")
	onlyMethod := map[int]bool{1: true, 5: true, 6: true, 7: true}
	from, to = addedDeclaration("a.py", py, 6, 6, onlyMethod)
	require.Equal(t, []int{5, 7}, []int{from, to}, "when the class is only partly new, the new method alone is shown")
	from, to = addedDeclaration("a.py", py, 6, 6, added(7))
	require.Equal(t, []int{1, 7}, []int{from, to}, "when the class is new too and fits, all of it is")

	var big strings.Builder
	big.WriteString("class Big:\n    def long(self):\n")
	for range 120 {
		big.WriteString("        step()\n")
	}
	for range 80 {
		big.WriteString("    y = 1\n")
	}
	all := lines(strings.TrimSuffix(big.String(), "\n"))
	from, to = addedDeclaration("a.go", all, 110, 110, added(len(all)))
	require.Equal(t, []int{2, 122}, []int{from, to}, "a method far below its header is still found when its class is too long")

	require.True(t, isDeclaration("data \"aws_s3_bucket\" \"b\" {"))
	require.True(t, isDeclaration("  it('works', () => {"))
	require.True(t, isDeclaration("build() {"))
	from, to = addedDeclaration("a.go", goFn, 9, 9, added(9))
	require.Equal(t, []int{1, 9}, []int{from, to}, "a finding on the closing brace is in the function")
	from, to = addedDeclaration("a.go", goFn, 4, 4, added(9))
	require.Equal(t, []int{1, 9}, []int{from, to}, "as is one on the last line of its header")
	from, to = addedDeclaration("a.js", js, 2, 2, added(4))
	require.Equal(t, []int{1, 4}, []int{from, to}, "or on its opening brace")
	labelled := lines("func run() {\nretry:\n    step()\n    goto retry\n}")
	from, to = addedDeclaration("a.go", labelled, 3, 3, added(5))
	require.Equal(t, []int{1, 5}, []int{from, to}, "a label at the margin does not end the function")
	from, to = addedDeclaration("a.go", labelled, 2, 2, added(5))
	require.Equal(t, []int{1, 5}, []int{from, to}, "a finding on the label itself is in the function")
	for _, spelling := range []string{"r\u00e9essayer:", "retry: // retry work", "retry: step()"} {
		other := lines("func run() {\n" + spelling + "\n    step()\n    goto retry\n}")
		from, to = addedDeclaration("a.go", other, 3, 3, added(5))
		require.Equal(t, []int{1, 5}, []int{from, to}, spelling)
	}
	for _, tail := range []string{") error { // work", ") error { /* work */"} {
		commented := lines("func run(\n    a int,\n" + tail + "\n    step()\n    return nil\n}")
		from, to = addedDeclaration("a.go", commented, 1, 1, added(6))
		require.Equal(t, []int{1, 6}, []int{from, to}, tail)
		from, to = addedDeclaration("a.go", commented, 4, 4, added(6))
		require.Equal(t, []int{1, 6}, []int{from, to}, tail)
	}
	require.True(t, isDeclaration("func \u00c9mettre() {"))
	require.True(t, isDeclaration("class \u00c9metteur:"))
	for _, open := range []string{"{", "{ // body", "{ /* body */"} {
		own := lines("function run()\n" + open + "\n    step();\n}")
		from, to = addedDeclaration("a.js", own, 1, 1, added(4))
		require.Equal(t, []int{1, 4}, []int{from, to}, open)
		from, to = addedDeclaration("a.js", own, 3, 3, added(4))
		require.Equal(t, []int{1, 4}, []int{from, to}, open)
	}
	raw := lines("func run() {\n    q := `select\nfrom t\nwhere a = '{'`\n    use(q)\n}")
	from, to = addedDeclaration("a.go", raw, 1, 1, added(6))
	require.Equal(t, []int{1, 6}, []int{from, to}, "a raw string's lines are indented as they please")
	loop := lines("func run() {\nretry: for {\n    step()\n}\n    finish()\n}")
	from, to = addedDeclaration("a.go", loop, 1, 1, added(6))
	require.Equal(t, []int{1, 6}, []int{from, to}, "a labelled loop's brace is not the function's")
	odd := lines("fn f<'a>(x: &'a str) -> &'a str {\n    if x {\n        y()\n    }\n    x\n}")
	from, to = addedDeclaration("a.rs", odd, 1, 1, added(6))
	require.Equal(t, []int{1, 6}, []int{from, to}, "a lifetime is no quote")
	doc := lines("def run():\n    \"\"\"documentation\"\"\"")
	from, to = addedDeclaration("a.py", doc, 1, 1, added(2))
	require.Equal(t, []int{1, 2}, []int{from, to}, "a body that is only a docstring")
	doc = lines("def run():\n    \"\"\"def inner():\n    def other():\n    \"\"\"\n    step()")
	from, to = addedDeclaration("a.py", doc, 3, 3, added(5))
	require.Equal(t, []int{1, 5}, []int{from, to}, "a declaration written in a docstring is text")
	tplUnknown := lines("function run() {\n    const s = `text\n}\nmore\n`;\n    finish();\n}")
	from, to = addedDeclaration("notes.unknown", tplUnknown, 1, 1, added(7))
	require.Equal(t, []int{0, 0}, []int{from, to})
	unknown := lines("thing run() {\n    s = \"}\"\n    step()\n}")
	from, to = addedDeclaration("notes.unknown", unknown, 3, 3, added(4))
	require.Equal(t, []int{0, 0}, []int{from, to}, "in a language unknown a string is not told from code: the end is not known")
	for _, src := range [][2]string{
		{"a.js", "function run() {\n    const re = /}/;\n    step();\n}"},
		{"a.tf", "locals {\n  text = <<EOF\n}\nEOF\n  other = 1\n}"},
		{"a.py", "def run(\n    options={\n        \"a\": 1,\n    },\n):\n    step()"},
	} {
		unsure := lines(src[1])
		from, to = addedDeclaration(src[0], unsure, 1, 1, added(len(unsure)))
		if from != 0 {
			require.Equal(t, []int{1, len(unsure)}, []int{from, to}, src[1])
		}
	}
	for _, src := range []string{
		"function run()\n{ /* body\n*/\n    step();\n}",
		"function run() {\n    const s = `a\\`}`;\n    step();\n}",
	} {
		whole := lines(src)
		from, to = addedDeclaration("a.js", whole, 1, 1, added(len(whole)))
		require.Equal(t, []int{1, len(whole)}, []int{from, to}, src)
	}
	for _, src := range []string{
		"function run() { step();\nfinish();\n}",
		"function run() {\nif (ready) {\nconst re = /}/;\n}\nfinish();\n}",
		"function run() {\nif (ready) {\nconst re = /[}]/;\n}\nfinish();\n}",
	} {
		flat := lines(src)
		from, to = addedDeclaration("a.js", flat, 1, 1, added(len(flat)))
		require.Equal(t, []int{1, len(flat)}, []int{from, to}, src)
	}
	py = lines("def run():\n    text = \"\"\"hello\nworld\n\"\"\"\n    publish(text)")
	from, to = addedDeclaration("a.py", py, 1, 1, added(5))
	require.Equal(t, []int{1, 5}, []int{from, to}, "a string in triple quotes is text")
	from, to = addedDeclaration("a.py", py, 5, 5, added(5))
	require.Equal(t, []int{1, 5}, []int{from, to})
	from, to = addedDeclaration("a.go", raw, 5, 5, added(6))
	require.Equal(t, []int{1, 6}, []int{from, to}, "found from below a raw string's unindented lines")
	py = lines("def run():\n    text = \"\"\"hello\nworld\n\"\"\"")
	from, to = addedDeclaration("a.py", py, 1, 1, added(4))
	require.Equal(t, []int{1, 4}, []int{from, to}, "a literal that ends the declaration")
	py = lines("def run():\n    mask = 1<<bits\n    publish(mask)")
	from, to = addedDeclaration("a.py", py, 1, 1, added(3))
	require.Equal(t, []int{1, 3}, []int{from, to}, "a shift is no here-document")
	slash := lines("func a() {\n    p := `C:\\`\n}\n\nfunc b() {\n    step()\n}")
	from, to = addedDeclaration("a.go", slash, 6, 6, added(7))
	require.Equal(t, []int{5, 7}, []int{from, to}, "a raw string of Go ends at its backtick")
	flat := lines("function run() {\nfirst();\nsecond();\n}")
	from, to = addedDeclaration("a.js", flat, 3, 3, added(4))
	require.Equal(t, []int{1, 4}, []int{from, to}, "a body written at the margin")
	py = lines("def run():\n    text = \"\"\"hello\n\nworld\n\"\"\"")
	from, to = addedDeclaration("a.py", py, 1, 1, added(5))
	require.Equal(t, []int{1, 5}, []int{from, to}, "a blank line inside the literal")
	for _, shift := range []string{"mask = 1 << BITS", "marker = \"<<EOF\"", "x = 1  # <<EOF", "mask = n<<EOF"} {
		py = lines("def run():\n    " + shift + "\n\n    publish(mask)")
		from, to = addedDeclaration("a.py", py, 1, 1, added(4))
		require.Equal(t, []int{1, 4}, []int{from, to}, shift)
	}
	slash = lines("func a() {\n    p := `C:\\` + `x`\n}\n\nfunc b() {\n    step()\n}")
	from, to = addedDeclaration("a.go", slash, 6, 6, added(7))
	require.Equal(t, []int{5, 7}, []int{from, to})
	tpl := lines("function a() {\n    const s = `a\\`\n}`;\n    step();\n}")
	from, to = addedDeclaration("a.js", tpl, 1, 1, added(5))
	require.Equal(t, []int{1, 5}, []int{from, to}, "a template's escaped backtick at the end of a line")
	require.False(t, isDeclaration("    else:"))
	require.False(t, isDeclaration("default:"))
	require.True(t, isDeclaration("jobs:"))
	yaml := lines("jobs:\n  build:\n    steps:\n      - run: x")
	from, to = addedDeclaration("a.yml", yaml, 4, 4, added(4))
	require.Equal(t, []int{1, 4}, []int{from, to}, "a key that heads its own block is still a declaration")
	rb := lines("def run\n  step\nend")
	from, to = addedDeclaration("a.rb", rb, 3, 3, added(3))
	require.Equal(t, []int{1, 3}, []int{from, to})
	require.True(t, isDeclaration("func (r *T) run() {"))
	for _, shell := range []string{"test() {", "it() {", "describe() {", "async() {", "data() {", "variable() {", "locals() {", "type() {"} {
		require.True(t, isDeclaration(shell), shell)
	}
	require.True(t, isDeclaration(`describe("the thing", function () {`))
	require.True(t, isDeclaration("export const run = 1"))
	require.True(t, isDeclaration("export default function run() {"))
	for _, not := range []string{"data := load()", "items := load()", "functionCall()", "testValue := load()", "typeName := load()", "variable = 3",
		`test("value")`, `it("value")`, `describe("value")`, "export PATH=/bin", "data.foo()", "data[0] = value", "data == other", "test()", "it()", "describe()", "async()", "variable()", "data, err := f()", "fn(x)", "type = 3"} {
		require.False(t, isDeclaration(not), not)
	}
}

func TestAddedValue_ShowsANewConstantOrVariableToItsLastEntry(t *testing.T) {
	lines := func(s string) []string { return strings.Split(s, "\n") }
	added := func(from, to int) map[int]bool {
		m := map[int]bool{}
		for n := from; n <= to; n++ {
			m[n] = true
		}
		return m
	}
	for _, c := range []struct{ file, src string }{
		{"a.go", "var names = []string{\n    \"a\",\n    \"b\",\n}"},
		{"a.go", "    limits := map[string]int{\n        \"a\": 1,\n        \"b\": 2,\n    }"},
		{"a.go", "const (\n    first = iota\n    second\n)"},
		{"a.go", "var sizes = [...]int{1, 2,\n    3,\n    4}"},
		{"a.py", "LIMITS: dict[str, int] = {\n    \"a\": 1,\n    \"b\": 2,\n}"},
		{"a.py", "PAIR = (\n    1,\n    2,\n)"},
		{"a.ts", "export const routes: Record<string, string> = {\n  home: \"/\",\n  nested: { a: \"}\" },\n};"},
		{"a.rs", "static PRIMES: [u8; 3] = [\n    2,\n    3,\n];"},
		{"A.java", "    private static final List<String> NAMES = List.of(\n        \"a\",\n        \"b\");"},
		{"a.rb", "NAMES = [\n  'a',\n  'b',\n]"},
	} {
		src := lines(c.src)
		for _, at := range []int{1, 3, len(src)} {
			from, to := addedValue(c.file, src, at, at, added(1, len(src)))
			require.Equal(t, []int{1, len(src)}, []int{from, to}, "%s from line %d", c.src, at)
		}
	}
	src := lines("var names = []string{\n    \"a\",\n    \"b\",\n}")
	from, to := addedValue("a.go", src, 3, 3, added(2, 4))
	require.Equal(t, []int{0, 0}, []int{from, to}, "one the change only adds to is not new")
	from, to = addedValue("a.go", src, 3, 3, nil)
	require.Equal(t, []int{0, 0}, []int{from, to}, "what the change adds is not known")
	nested := lines("var all = map[string][]string{\n    \"a\": {\n        \"x\",\n    },\n}")
	from, to = addedValue("a.go", nested, 3, 3, added(1, 5))
	require.Equal(t, []int{1, 5}, []int{from, to}, "the outermost")
	for _, not := range []string{
		"if err := run(\n    a,\n); err != nil {\n}",
		"total += sum(\n    a,\n    b,\n)",
		"run(name=value, items=[\n    1,\n    2,\n])",
		"ok := a == (\n    b,\n)",
		"x = 1\ny = 2\nz = 3",
	} {
		src := lines(not)
		from, to = addedValue("a.go", src, 2, 2, added(1, len(src)))
		if strings.HasPrefix(not, "ok :=") {
			require.Equal(t, []int{1, 3}, []int{from, to}, "a value that holds a comparison is still a value")
			continue
		}
		require.Equal(t, []int{0, 0}, []int{from, to}, not)
	}
	var big strings.Builder
	big.WriteString("var names = []string{\n")
	for range maxAddedLines {
		big.WriteString("    \"a\",\n")
	}
	big.WriteString("}")
	long := lines(big.String())
	from, to = addedValue("a.go", long, 5, 5, added(1, len(long)))
	require.Equal(t, []int{0, 0}, []int{from, to}, "one too long to read at once")

	content := "package p\n\nfunc old() {}\n\nvar names = []string{\n" + strings.Repeat("    \"a\",\n", 30) + "}\n\nfunc other() {}\n"
	shown := codeSnippetFocus(content, "p.go", focus{Line: 20, Added: added(5, 36)}, codeStyle{width: 100})
	require.Contains(t, shown, "var names = []string{")
	require.Contains(t, shown, " 36 ")
}
