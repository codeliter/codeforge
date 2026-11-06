//nolint:err113 // dynamic error messages are okay in tests
//go:build eval

package review

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Recall check for the time-boxed review: does capping steps and effort lose real findings?
//
//	CODEFORGE_EVAL_MODEL=provider/model go test -tags eval ./src/review -run TestEvalRecall -v -timeout 30m
//
// Optional: CODEFORGE_EVAL_BIN (default empryo on PATH), CODEFORGE_EVAL_RUNS (default 2 runs per case and setup).
// It calls the real model, so it costs money. Each case is a tiny repository with a base commit and a head commit that
// seeds known bugs (or none). The same briefs run twice: at the timed tier (what codeforge review uses) and at the full
// tier with a generous clock. The bar is that the timed setup keeps at least recallFloor of the full setup's recall.

const recallFloor = 0.85

type recallCase struct {
	name    string
	base    map[string]string
	head    map[string]string
	defects []Defect
}

var recallCases = []recallCase{
	{
		name:    "pagination off by one",
		base:    map[string]string{"page/page.go": "package page\n\n// Page returns page n (0-based) of items.\nfunc Page(items []int, n, size int) []int {\n\tstart := n * size\n\tif start >= len(items) {\n\t\treturn nil\n\t}\n\tend := min(start+size, len(items))\n\treturn items[start:end]\n}\n"},
		head:    map[string]string{"page/page.go": "package page\n\n// Page returns page n (0-based) of items.\nfunc Page(items []int, n, size int) []int {\n\tstart := n * size\n\tif start >= len(items) {\n\t\treturn nil\n\t}\n\tend := min(start+size, len(items)-1)\n\treturn items[start:end]\n}\n"},
		defects: []Defect{{Name: "last item dropped", File: "page/page.go", Lo: 9, Hi: 10, Keywords: []string{"last item", "len(items)-1", "off-by-one", "off by one"}}},
	},
	{
		name:    "sql built from input",
		base:    map[string]string{"store/user.go": "package store\n\nimport \"database/sql\"\n\nfunc Find(db *sql.DB, name string) *sql.Row {\n\treturn db.QueryRow(\"SELECT id FROM users WHERE name = ?\", name)\n}\n"},
		head:    map[string]string{"store/user.go": "package store\n\nimport (\n\t\"database/sql\"\n\t\"fmt\"\n)\n\nfunc Find(db *sql.DB, name string) *sql.Row {\n\treturn db.QueryRow(fmt.Sprintf(\"SELECT id FROM users WHERE name = '%s'\", name))\n}\n"},
		defects: []Defect{{Name: "sql injection", File: "store/user.go", Lo: 9, Hi: 9, Keywords: []string{"injection", "sprintf", "parameteri"}}},
	},
	{
		name:    "dedupe reorders",
		base:    map[string]string{"list/dedupe.go": "package list\n\n// Dedupe drops repeats and keeps the first occurrence in order.\nfunc Dedupe(in []string) []string {\n\tseen := map[string]bool{}\n\tvar out []string\n\tfor _, s := range in {\n\t\tif !seen[s] {\n\t\t\tseen[s] = true\n\t\t\tout = append(out, s)\n\t\t}\n\t}\n\treturn out\n}\n"},
		head:    map[string]string{"list/dedupe.go": "package list\n\n// Dedupe drops repeats and keeps the first occurrence in order.\nfunc Dedupe(in []string) []string {\n\tseen := map[string]bool{}\n\tfor _, s := range in {\n\t\tseen[s] = true\n\t}\n\tout := make([]string, 0, len(seen))\n\tfor s := range seen {\n\t\tout = append(out, s)\n\t}\n\treturn out\n}\n"},
		defects: []Defect{{Name: "order lost", File: "list/dedupe.go", Lo: 5, Hi: 13, Keywords: []string{"order", "map iteration", "random"}}},
	},
	{
		name: "clean rename",
		base: map[string]string{"list/sum.go": "package list\n\nfunc Sum(xs []int) int {\n\tt := 0\n\tfor _, x := range xs {\n\t\tt += x\n\t}\n\treturn t\n}\n"},
		head: map[string]string{"list/sum.go": "package list\n\n// Sum adds the numbers in xs.\nfunc Sum(xs []int) int {\n\ttotal := 0\n\tfor _, x := range xs {\n\t\ttotal += x\n\t}\n\treturn total\n}\n"},
	},
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "user.email=e@e", "-c", "user.name=e"}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	}
}

func buildRepo(t *testing.T, c recallCase) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeTree(t, dir, map[string]string{"go.mod": "module eval\n\ngo 1.22\n"})
	writeTree(t, dir, c.base)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	writeTree(t, dir, c.head)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "change")
	return dir
}

// dirRunner runs git in the case repository, so no test depends on the process working directory.
type dirRunner struct{ dir string }

func (d dirRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return ExecRunner{}.RunDir(ctx, d.dir, name, args...)
}

type setup struct {
	name    string
	tier    Tier
	timeout time.Duration
}

type tally struct {
	found, defects, spurious, noVerdict, runs int
	elapsed                                   time.Duration
}

func (a tally) recall() float64 {
	if a.defects == 0 {
		return 1
	}
	return float64(a.found) / float64(a.defects)
}

func TestEvalRecall(t *testing.T) {
	model := os.Getenv("CODEFORGE_EVAL_MODEL")
	if model == "" {
		t.Skip("set CODEFORGE_EVAL_MODEL=provider/model")
	}
	bin := os.Getenv("CODEFORGE_EVAL_BIN")
	if bin == "" {
		bin = "empryo"
	}
	runs := 2
	if v, err := strconv.Atoi(os.Getenv("CODEFORGE_EVAL_RUNS")); err == nil && v > 0 {
		runs = v
	}

	std := TierStandard
	std.Jury = false
	setups := []setup{
		{"timed", std.Timed(), 75 * time.Second},
		{"full", std, 5 * time.Minute},
	}
	totals := map[string]*tally{}
	for _, s := range setups {
		totals[s.name] = &tally{}
	}

	for _, c := range recallCases {
		dir := buildRepo(t, c)
		l, err := LoadLocal(t.Context(), dirRunner{dir: dir})
		require.NoError(t, err)
		for _, s := range setups {
			for run := range runs {
				brief := PromptLocal(l, s.tier, nil) + Pack(t.Context(), ExecRunner{}, dir, l, s.tier)
				ctx, cancel := context.WithTimeout(t.Context(), s.timeout)
				start := time.Now()
				out, rerr := EmpryoReviewer{Bin: bin, Model: model, Dir: dir, Tier: s.tier}.Review(ctx, brief)
				cancel()
				a := totals[s.name]
				a.runs++
				a.elapsed += time.Since(start)
				// A run without a verdict still owes its defects: it is a miss, in both setups alike.
				a.defects += len(c.defects)
				v, perr := ParseVerdict(out)
				if perr != nil {
					a.noVerdict++
					t.Logf("%-24s %-5s run %d: no verdict (%v, %v)", c.name, s.name, run, rerr, perr)
					continue
				}
				sc := ScoreBlocking(c.defects, v.Blocking)
				a.found += len(sc.Found)
				a.spurious += sc.Spurious
				t.Logf("%-24s %-5s run %d: found %v missed %v spurious %d in %s", c.name, s.name, run, sc.Found, sc.Missed, sc.Spurious, time.Since(start).Round(time.Second))
			}
		}
	}

	var report strings.Builder
	for _, s := range setups {
		a := totals[s.name]
		fmt.Fprintf(&report, "\n%-5s recall %.2f (%d/%d) spurious %d no-verdict %d/%d mean %s",
			s.name, a.recall(), a.found, a.defects, a.spurious, a.noVerdict, a.runs, (a.elapsed / time.Duration(max(a.runs, 1))).Round(time.Second))
	}
	t.Log(report.String())

	timed, full := totals["timed"], totals["full"]
	require.Less(t, full.noVerdict, full.runs, "no full run returned a verdict, so there is no baseline: check the model and its credentials")
	require.Positive(t, full.found, "the full review found nothing, so recall cannot be compared")
	require.Zero(t, timed.noVerdict, "a timed review that returns no verdict is a failed review")
	require.GreaterOrEqual(t, timed.recall(), full.recall()*recallFloor,
		"the step and effort cap must keep at least %.0f%% of the full review's recall", recallFloor*100)
}
