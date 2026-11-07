package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	require.Contains(t, got, "pkg/a.go\n- pkg/a.go:4 nil deref when p is nil\n    2 | \n    3 | func F(p *T) int {\n  > 4 |     return p.n\n    5 | }\n")
	require.Contains(t, got, "Should:\n\npkg/a.go\n- pkg/a.go:1 add a doc comment\n  > 1 | package pkg\n")
	require.Contains(t, got, "- pkg/a.go:99 made up\n")
	require.NotContains(t, got, "99 |", "a dropped finding shows no code")
	require.NotContains(t, got, "\x1b[", "no colour when the output is not a terminal")
}

func TestCodeAround_TrimsSharedIndentKeepsWidthAndHighlightsOnATerminal(t *testing.T) {
	repo := t.TempDir()
	src := "package p\nfunc f() {\n\tfor {\n\t\tif x {\n\t\t\treturn \"" + strings.Repeat("y", 100) + "\"\n\t\t}\n\t}\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, "p.go"), []byte(src), 0o600))

	plain := codeAround(repo, "p.go", 5, codeStyle{width: 40})
	require.Contains(t, plain, "    3 | for {\n    4 |     if x {\n", "the indentation the lines share is removed")
	require.Contains(t, plain, "  > 5 |         return \"y")
	for _, l := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
		require.LessOrEqual(t, len([]rune(l)), 40, "every line fits the width: %q", l)
	}

	pal := paletteFor("dark")
	colored := codeAround(repo, "p.go", 4, codeStyle{color: true, width: 120, pal: pal})
	require.Contains(t, colored, "\x1b[1m\x1b[38;2;126;168;201mif", "keywords take the Steel brand colour #7ea8c9")
	require.Contains(t, colored, pal.accent+"▶ 4", "the finding's line is marked in the Steel accent")
	require.NotContains(t, colored, "\x1b[48;2;", "the terminal's background is left alone")
	require.Equal(t, 5, strings.Count(colored, "\n"), "highlighting keeps one line per line")
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
