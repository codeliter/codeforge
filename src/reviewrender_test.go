package src

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/review"
)

const sampleReview = "Moves tracing behind one seam.\n\n" +
	"### search/serper/main.go\n\n" +
	"suggestion: search/serper/main.go:108 — `kit.InstrumentHTTPClient` sends **baggage** to the vendor.\n" +
	"  Drop propagation for vendor clients.\n\n" +
	"- **nitpick (non-blocking):** `search/serper/main.go:72` — Nit: remove the commented-out code.\n\n" +
	"```go\n\t// gprclogging.PayloadReceived,\n```\n\n" +
	"### Across files\n\n" +
	"- Split the unrelated `go.mod` bumps into their own PR.\n"

func fakeCode(file string, line int) string {
	return "    CODE " + file + ":" + strings.Repeat("|", line%3) + "\n"
}

func TestRenderReview_Plain(t *testing.T) {
	got := renderReview(sampleReview, codeStyle{wrap: true, width: 60}, fakeCode)
	require.NotContains(t, got, "###")
	require.NotContains(t, got, "`")
	require.NotContains(t, got, "**")
	require.NotContains(t, got, "\x1b[", "no colour without a colour terminal")
	require.NotContains(t, got, "\n\n\n")
	require.Contains(t, got, "\nsearch/serper/main.go\n")
	require.Contains(t, got, "- suggestion: search/serper/main.go:108\n  kit.InstrumentHTTPClient sends baggage to the vendor.\n")
	require.Contains(t, got, "  Drop propagation for vendor clients.\n    CODE search/serper/main.go:\n", "the code follows the finding and its continuation")
	require.Contains(t, got, "- nitpick (non-blocking): search/serper/main.go:72 Nit:\n  remove the commented-out code.\n\n    | // gprclogging.PayloadReceived,\n",
		"a finding that quotes its code gets no second copy")
	require.Equal(t, 1, strings.Count(got, "CODE "))
	require.Contains(t, got, "- Split the unrelated go.mod bumps into their own PR.\n")
	for _, l := range strings.Split(got, "\n") {
		require.LessOrEqual(t, len([]rune(l)), 60, "wrapped to the width: %q", l)
	}
}

func TestRenderReview_KeepsPunctuationAgainstInlineCode(t *testing.T) {
	got := renderReview("Spreads to `codeforge respond`; see **this**, and `a`/`b`.", codeStyle{}, nil)
	require.Equal(t, "Spreads to codeforge respond; see this, and a/b.\n", got)
}

func TestRenderReview_SteelColours(t *testing.T) {
	st := codeStyle{color: true, wrap: true, width: 100, pal: paletteFor("dark")}
	got := renderReview("### a/b.go\n\nissue (blocking): a/b.go:3 — drops `rows`\nquestion: a/b.go:4 — why?\n\n```go\nfunc f() {}\n```\n", st, nil)
	require.Contains(t, got, st.pal.accent+"a/b.go"+ansiReset+"\n", "a file heading is in the accent")
	require.Contains(t, got, st.pal.issue+"issue")
	require.Contains(t, got, st.pal.question+"question:")
	require.Contains(t, got, st.pal.inline+"rows"+ansiReset)
	require.Contains(t, got, "\x1b[1m\x1b[38;2;126;168;201mfunc", "fenced code is highlighted in Steel")
}

func TestRunReviewEngine_StylesTheReviewOnATerminal(t *testing.T) {
	f := newPostFixture(t, `printf '%s' '{"output":"Verdict: Comment. Fine.\n\n### a.go\n\nsuggestion: a.go:1 — use x","steps":3,"cost":0.5,"duration":2000}'`, 0)
	old := stdoutStyled
	stdoutStyled = func() bool { return true }
	t.Cleanup(func() { stdoutStyled = old })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	oldOut := os.Stdout
	os.Stdout = w
	runErr := runReviewEngine(reviewContext(t, false), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
	os.Stdout = oldOut
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, runErr)

	args, err := os.ReadFile(f.engineArgs)
	require.NoError(t, err)
	require.Contains(t, string(args), "--json", "the review is read as JSON to be laid out")
	require.NotContains(t, string(out), "Verdict")
	require.Contains(t, string(out), "Fine.\n\na.go\n\n- suggestion: a.go:1 use x\n")
	require.Contains(t, string(out), "3 steps, $0.5000, 2.0s")
	require.NoFileExists(t, f.ghArgs, "showing a review never posts it")
}

func TestReviewCode_ReadsWhereTheReviewSays(t *testing.T) {
	st := codeStyle{width: 80}
	require.Nil(t, reviewCode(t.Context(), st), "outside a review there is nothing to read")
	d := &review.Display{}
	ctx := review.WithDisplay(context.Background(), d)
	require.Nil(t, reviewCode(ctx, st), "a PR whose head was not fetched")
	d.Local()
	require.NotNil(t, reviewCode(ctx, st))
	d.PR("refs/codeforge/pr-1")
	ref, local := d.Source()
	require.Equal(t, "refs/codeforge/pr-1", ref)
	require.False(t, local)
}
