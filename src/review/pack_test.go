package review

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var errNotThere = errors.New("not there")

func TestParseVerdict_IgnoresALeakedToolCallClose(t *testing.T) {
	// From a real run: the answer ended inside the text of a final tool call.
	out := "Done.\n```json\n{\"verdict\":\"fail\",\"summary\":\"x\",\"blocking\":[{\"file\":\"a.go\",\"line\":1,\"what\":\"y\"}],\"should\":[]}\n```</parameter>\n</invoke>"
	v, err := ParseVerdict(out)
	require.NoError(t, err)
	require.Len(t, v.Blocking, 1)
	_, err = ParseVerdict(out + "\nmore text")
	require.ErrorIs(t, err, ErrNoVerdict, "anything else after the verdict still makes it no verdict")
}

func TestParseVerdict_RescuesAnAnswerCutOffInItsShouldList(t *testing.T) {
	// From a real gate run: the output ended mid-string in the should list.
	cut := "```json\n{\"verdict\":\"fail\",\"summary\":\"two bugs\",\"blocking\":[{\"file\":\"a.go\",\"line\":3,\"what\":\"x\"},{\"file\":\"b.go\",\"line\":9,\"what\":\"y\"}]," +
		"\"should\":[{\"file\":\"c.go\",\"line\":1,\"what\":\"ok\"},{\"file\":\"src/review/proc.go\",\"line\":17,\"what\":\"Bound replaces any SysProcAttr a Wrap command s"
	v, err := ParseVerdict(cut)
	require.NoError(t, err)
	require.Equal(t, VerdictFail, v.Verdict)
	require.Len(t, v.Blocking, 2)
	require.Len(t, v.Should, 0, "a should list that was cut is not half-read")
	require.Contains(t, v.Summary, "cut off")

	for name, out := range map[string]string{
		"cut inside blocking":     "```json\n{\"verdict\":\"fail\",\"blocking\":[{\"file\":\"a.go\",\"line\":3,\"what\":\"x\"},{\"file\":\"b.go\",\"li",
		"cut before blocking":     "```json\n{\"verdict\":\"pass\",\"summary\":\"fi",
		"no verdict field":        "```json\n{\"blocking\":[],\"should\":[{\"fi",
		"closed but broken block": "```json\n{\"verdict\":\"pass\",\"blocking\":[]\n```\nprose",
	} {
		_, err := ParseVerdict(out)
		require.ErrorIs(t, err, ErrNoVerdict, name)
	}
	clean, err := ParseVerdict("```json\n{\"verdict\":\"pass\",\"summary\":\"fine\",\"blocking\":[],\"should\":[{\"fi")
	require.NoError(t, err)
	require.Equal(t, VerdictPass, clean.Verdict, "an empty, complete blocking list is a pass")
}

func TestPackOrder_CodeBeforeTestsBeforeDocs(t *testing.T) {
	got := packOrder([]string{"TECHNICAL.md", "src/a_test.go", "docs/x.md", "src/b.go", "web/c.test.ts", "src/a.go"})
	require.Equal(t, []string{"src/b.go", "src/a.go", "src/a_test.go", "web/c.test.ts", "TECHNICAL.md", "docs/x.md"}, got)
}

func TestHunkRangesAndExcerpt(t *testing.T) {
	diff := "diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n@@ -10,2 +10,3 @@ func f() {\n x\n+y\n z\n@@ -100 +101 @@\n-a\n+b\n"
	require.Equal(t, map[string][][2]int{"big.go": {{10, 12}, {101, 101}}}, hunkRanges(diff))

	var src strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&src, "line %d\n", i)
	}
	got := excerpt(src.String(), [][2]int{{10, 12}, {101, 101}}, 3)
	require.True(t, strings.HasPrefix(got, "   7  line 7\n"), got)
	require.Contains(t, got, "  15  line 15\n   …\n  98  line 98\n")
	require.True(t, strings.HasSuffix(got, " 104  line 104\n"))
	require.Empty(t, excerpt(src.String(), nil, 3), "no hunks, no excerpt")
}

func TestPackFiles_LargeFilesGoInAsExcerptsAndDocsLast(t *testing.T) {
	big := strings.Repeat("// filler\n", 3000) // 30 KB, over maxFileBytes
	files := map[string]string{
		"TECHNICAL.md": strings.Repeat("prose\n", 2000),
		"src/big.go":   big[:200] + "func Bug() { return a || b }\n" + big[200:],
		"src/small.go": "package src\n",
		".env":         "TOKEN=x\n",
	}
	read := func(f string) ([]byte, error) {
		if s, ok := files[f]; ok {
			return []byte(s), nil
		}
		return nil, errNotThere
	}
	diff := "+++ b/src/big.go\n@@ -21,0 +21,1 @@\n+func Bug() { return a || b }\n"
	got := packFiles([]string{"TECHNICAL.md", "src/big.go", "src/small.go", ".env"}, diff, 12_000, read)
	require.Contains(t, got, "=== src/small.go ===")
	require.Contains(t, got, "=== src/big.go (excerpts around the changes")
	require.Contains(t, got, "  21  func Bug() { return a || b }")
	require.Contains(t, got, "Not included, read them if a finding needs them: TECHNICAL.md", "docs come last and drop out first")
	require.NotContains(t, got, "TOKEN")
	require.Less(t, strings.Index(got, "src/big.go"), strings.Index(got, "Not included"))
}

func TestPackFiles_EveryChangedCodeFileGetsItsChangesBeforeAnyIsWhole(t *testing.T) {
	// Three code files of about 12 KB as sent, and room for only one of them whole: each still shows its change.
	files := map[string]string{}
	var names []string
	var diff strings.Builder
	for _, n := range []string{"a.go", "b.go", "c.go"} {
		files[n] = strings.Repeat("x := 1\n", 400) + "BUG in " + n + "\n" + strings.Repeat("y := 2\n", 500)
		names = append(names, n)
		fmt.Fprintf(&diff, "+++ b/%s\n@@ -401,0 +401,1 @@\n+BUG in %s\n", n, n)
	}
	read := func(f string) ([]byte, error) { return []byte(files[f]), nil }
	got := packFiles(names, diff.String(), 20_000, read)
	for _, n := range names {
		require.Contains(t, got, " 401  BUG in "+n, "%s is in the brief where it changed", n)
	}
	require.Contains(t, got, "=== a.go ===", "the room left goes to whole files, in order")
	require.Contains(t, got, "=== c.go (excerpts")
	require.NotContains(t, got, "Not included")
}

func TestCallersAt_SearchesThePRHead(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"git grep -n -w -e Load refs/codeforge/pr-3 --": "refs/codeforge/pr-3:src/use.go:12:\tLoad(ctx)\nrefs/codeforge/pr-3:src/load.go:3:func Load() {}\n",
	}}
	got := CallersAt(t.Context(), r, "refs/codeforge/pr-3", "+func Load() {}\n", []string{"src/load.go"})
	require.Equal(t, "Load:\n  src/use.go:12:\tLoad(ctx)\n", got, "the ref prefix is stripped and changed files are left out")
}
