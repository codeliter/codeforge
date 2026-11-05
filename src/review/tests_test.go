package review

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errExit = errors.New("exit status 1")

type fakeTests struct {
	out   string
	err   error
	calls []string
}

func (f *fakeTests) RunDir(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, dir+" "+name+" "+strings.Join(args, " "))
	return []byte(f.out), f.err
}

func TestTestPackages(t *testing.T) {
	require.Equal(t, []string{"internal/a", "cmd/x"}, TestPackages([]string{"internal/a/a.go", "internal/a/b.go", "cmd/x/main.go", "README.md"}))
	require.Empty(t, TestPackages([]string{"README.md", ".env"}), "docs and secrets cost no test run")
	require.Equal(t, []string{"."}, TestPackages([]string{"main.go"}))
}

func TestVerify_PassesWhenTestsPass(t *testing.T) {
	r := &fakeTests{out: "ok  \texample.com/shop/internal/a\t0.1s"}
	require.Empty(t, Verify(t.Context(), r, "/repo", []string{"internal/a/a.go"}))
	require.Equal(t, []string{"/repo go test -count=1 ./internal/a"}, r.calls)
}

func TestVerify_BlocksOnAFailingPackage(t *testing.T) {
	r := &fakeTests{
		out: "--- FAIL: TestWrap (0.00s)\n    wrap_test.go:12: lost the last line\nFAIL\texample.com/shop/internal/textutil\t0.2s\n",
		err: errExit,
	}
	got := Verify(t.Context(), r, "/repo", []string{"internal/textutil/wrap.go", "internal/report/report.go"})
	require.Len(t, got, 1)
	require.Equal(t, "internal/textutil/wrap.go", got[0].File, "the finding points at a changed file of the failing package")
	require.Contains(t, got[0].What, "tests fail for example.com/shop/internal/textutil")
	require.Contains(t, got[0].What, "--- FAIL: TestWrap")
}

func TestVerify_BlocksWhenTheTestsCannotRun(t *testing.T) {
	r := &fakeTests{out: "# example.com/shop/internal/a\nbuild failed: undefined: Foo", err: errExit}
	got := Verify(t.Context(), r, "/repo", []string{"internal/a/a.go"})
	require.Len(t, got, 1)
	require.Contains(t, got[0].What, "could not be run")
	require.Contains(t, got[0].What, "build failed")
}

func TestVerify_RealGoTestCatchesTheDroppedLine(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	write("go.mod", "module example.com/shop\n\ngo 1.22\n")
	write("internal/textutil/wrap.go", `package textutil

import "strings"

func Wrap(s string, width int) []string {
	var lines []string
	var line string
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) <= width:
			line += " " + w
		default:
			lines = append(lines, line)
			line = w
		}
	}
	return lines
}
`)
	write("internal/textutil/wrap_test.go", `package textutil

import (
	"reflect"
	"testing"
)

func TestWrap(t *testing.T) {
	if got := Wrap("red apples", 20); !reflect.DeepEqual(got, []string{"red apples"}) {
		t.Errorf("got %q", got)
	}
}
`)

	got := Verify(t.Context(), ExecRunner{}, dir, []string{"internal/textutil/wrap.go"})
	require.Len(t, got, 1, "the dropped final line fails the shipped test, so the gate must block")
	require.Equal(t, "internal/textutil/wrap.go", got[0].File)
	require.Contains(t, got[0].What, "tests fail for example.com/shop/internal/textutil")
	require.True(t, grounded(dir, got[0]), "the finding points at a file that exists, so the jury keeps it")

	write("internal/textutil/wrap.go", `package textutil

import "strings"

func Wrap(s string, width int) []string {
	var lines []string
	var line string
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) <= width:
			line += " " + w
		default:
			lines = append(lines, line)
			line = w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
`)
	require.Empty(t, Verify(t.Context(), ExecRunner{}, dir, []string{"internal/textutil/wrap.go"}), "the fixed code passes")
}

func TestCacheDelete(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	now := time.Now()
	require.NoError(t, c.Put("k", Verdict{Verdict: VerdictPass}, now))
	_, ok := c.Get("k", now)
	require.True(t, ok)
	c.Delete("k")
	_, ok = c.Get("k", now)
	require.False(t, ok)
	c.Delete("missing") // a missing entry is not an error
}

func TestVerify_SkipsNonGoChanges(t *testing.T) {
	r := &fakeTests{}
	require.Empty(t, Verify(t.Context(), r, "/repo", []string{"README.md"}))
	require.Empty(t, r.calls, "no test run for a docs change")
}
