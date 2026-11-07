package src

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
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

const styledEngine = `echo "memory.embedder: Failed to wire" >&2; printf '%s' '{"output":"Verdict: Comment. Fine.\n\n### a.go\n\nsuggestion: a.go:1 — use x\nnitpick: a.go:2 — tidy","steps":3,"cost":0.5,"duration":2000}'`

// onTerminal pretends standard output is a terminal and says whether the accordion helper is there.
func onTerminal(t *testing.T, accordion bool) {
	t.Helper()
	oldStyled, oldAvail := stdoutStyled, accordionAvailable
	stdoutStyled = func() bool { return true }
	accordionAvailable = func() bool { return accordion }
	t.Cleanup(func() { stdoutStyled, accordionAvailable = oldStyled, oldAvail })
}

// captured runs fn with standard output and error going to pipes and returns what each received.
func captured(t *testing.T, fn func() error) (stdout, stderr string, err error) {
	t.Helper()
	r, w, perr := os.Pipe()
	require.NoError(t, perr)
	er, ew, perr := os.Pipe()
	require.NoError(t, perr)
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, ew
	read := func(f *os.File, into chan<- string) {
		data, rerr := io.ReadAll(f)
		if rerr != nil {
			data = append(data, rerr.Error()...)
		}
		into <- string(data)
	}
	outC, errC := make(chan string), make(chan string)
	go read(r, outC)
	go read(er, errC)
	err = fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	require.NoError(t, w.Close())
	require.NoError(t, ew.Close())
	return <-outC, <-errC, err
}

func TestRunReviewEngine_PrintsTheStyledReviewWithoutTheHelper(t *testing.T) {
	f := newPostFixture(t, styledEngine, 0)
	onTerminal(t, false)
	out, stderr, err := captured(t, func() error {
		return runReviewEngine(reviewContext(t, false), []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
	})
	require.NoError(t, err)
	require.NotContains(t, stderr, "memory.embedder", "the engine's warnings are held back when the run succeeds")
	args, err := os.ReadFile(f.engineArgs)
	require.NoError(t, err)
	require.Contains(t, string(args), "--json", "the review is read as JSON to be laid out")
	require.NotContains(t, out, "Verdict")
	require.Contains(t, out, "Fine.\n\na.go\n\n- suggestion: a.go:1 use x\n")
	require.Contains(t, out, "3 steps, $0.5000, 2.0s")
	require.NoFileExists(t, f.ghArgs, "showing a review never posts it")
}

func TestRunReviewEngine_ShowsAnAccordionAndPostsOnP(t *testing.T) {
	f := newPostFixture(t, styledEngine, 0)
	onTerminal(t, true)
	var shown forms.Accordion
	old := showAccordion
	showAccordion = func(_ string, a forms.Accordion) (bool, error) { shown = a; return true, nil }
	t.Cleanup(func() { showAccordion = old })

	post := &review.PostRequest{}
	post.Target("acme/w", "42")
	ctx := review.WithPost(review.WithDeadline(t.Context(), time.Now().Add(time.Minute)), post)
	out, _, err := captured(t, func() error {
		return runReviewEngine(ctx, []string{f.engine, "the prompt"}, os.Environ(), time.Now().Add(10*time.Second))
	})
	require.NoError(t, err)
	require.Equal(t, "Fine.", shown.Intro)
	require.Len(t, shown.Items, 1)
	require.Equal(t, "a.go", shown.Items[0].Title)
	require.Equal(t, "1 suggestion · 1 nitpick", shown.Items[0].Summary)
	require.Contains(t, shown.Items[0].Body, "- suggestion: a.go:1 use x")
	require.Equal(t, "p", shown.ConfirmKey, "posting is offered in the view")
	require.Contains(t, out, "Fine.\n\n  a.go  1 suggestion · 1 nitpick\n", "a recap stays in the terminal")
	require.FileExists(t, f.ghArgs, "p posts without a second question")
}

func TestReviewAccordion_SplitsAtHeadingsAndCountsFindings(t *testing.T) {
	a := reviewAccordion(sampleReview, "**Unconfirmed blocking leads.** Check:\n- `x.go:1` drops data\n", codeStyle{wrap: true, width: 80}, nil)
	require.Equal(t, "Moves tracing behind one seam.", a.Intro)
	titles := make([]string, len(a.Items))
	for i, s := range a.Items {
		titles[i] = s.Title
	}
	require.Equal(t, []string{"search/serper/main.go", "Across files", "Unconfirmed blocking leads"}, titles)
	require.Equal(t, "1 suggestion · 1 nitpick", a.Items[0].Summary)
	require.Equal(t, "1 point", a.Items[1].Summary)
	require.Contains(t, a.Items[2].Body, "x.go:1 drops data")
	only := reviewAccordion("Just prose.", "", codeStyle{}, nil)
	require.Equal(t, "Review", only.Items[0].Title, "a review without headings is one section")
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
