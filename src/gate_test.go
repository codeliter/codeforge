package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// deepFile is a Go file whose line 25 sits deep inside func run, which starts at line 3.
func deepFile(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n\nfunc run(items []string) error {\n")
	for i := 4; i < 22; i++ {
		b.WriteString("\tstep()\n")
	}
	b.WriteString("\tfor _, it := range items {\n\t\tif it == \"\" {\n\t\t\tcontinue\n")                          // 22-24
	b.WriteString("\t\t\treturn errors.New(\"" + strings.Repeat("y", 100) + "\")\n\t\t}\n\t}\n\treturn nil\n}\n") // 25-29
	require.NoError(t, os.WriteFile(filepath.Join(repo, "p.go"), []byte(b.String()), 0o600))
	return repo
}

func TestCodeAround_ShowsTheEnclosingFunctionAndEnoughContext(t *testing.T) {
	repo := deepFile(t)
	plain := codeAround(repo, "p.go", 25, codeStyle{width: 60})
	require.True(t, strings.HasPrefix(plain, "     3 | func run(items []string) error {\n       ⋮\n    19 |     step()\n"),
		"the function the line is in heads the snippet, then a gap, then six lines before: %q", plain)
	require.Contains(t, plain, "  > 25 |             return errors.New(\"y")
	require.True(t, strings.HasSuffix(plain, "    29 | }\n"), "four lines after")
	for _, l := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
		require.LessOrEqual(t, len([]rune(l)), 60, "every line fits the width: %q", l)
	}

	near := codeAround(repo, "p.go", 6, codeStyle{width: 80})
	require.True(t, strings.HasPrefix(near, "     1 | package p\n"), "a header inside the window needs no gap: %q", near)
	require.NotContains(t, near, "⋮")
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
	card := codeAround(repo, "p.go", 25, codeStyle{color: true, width: 100, pal: pal})
	lines := strings.Split(strings.TrimRight(card, "\n"), "\n")
	require.Contains(t, lines[0], pal.cardTitle, "a title row on its own background")
	require.Contains(t, lines[0], pal.accent+" p.go:25")
	require.Contains(t, card, pal.cardLine+pal.accent+"▌25", "the finding's line is raised and marked")
	require.Contains(t, card, "\x1b[1m\x1b[38;2;126;168;201mfunc", "code is highlighted in Steel")
	for i, l := range lines {
		require.Equal(t, 2+100-2, ansi.StringWidth(l), "every row of the card has the same width: row %d", i)
		require.True(t, strings.HasSuffix(l, ansiReset), "the background ends with the row")
	}
	require.Len(t, lines, 1+2+11, "title, header and gap, and lines 19 to 29")
}

func TestCutColumns_CountsCharacters(t *testing.T) {
	require.Equal(t, "ééé", cutColumns("ééé", 3), "three accented characters fit three columns")
	require.Equal(t, "éé…", cutColumns("éééé", 3))
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
