//go:build eval

package src

// Checking the jurors' leads: the reviewer is given two blockers another reviewer reported, one real and one the code
// rules out, and must keep the first and rule out the second. Before checkLeads every lead was posted as written, so
// the false one always reached the PR.
//
//	CODEFORGE_EVAL_MODEL=provider/model go test -tags eval ./src -run TestEvalCheckLeads -v -timeout 30m
//
// Optional: CODEFORGE_EVAL_BIN, CODEFORGE_EVAL_RUNS (default 4, run in parallel).
//
// Measured, 4 runs each: GPT 6.1 Sol and Opus 5.5 both kept the real lead 4/4 and ruled out the false one 4/4, each
// with the reason from the code (Clamp swaps the bounds first).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/effort"
	"github.com/codeliter/codeforge/src/review"
)

// checkLeadsSource has one real defect (Window drops the last element: the loop stops one short) and one function a
// juror misread (Clamp looks inverted at a glance, but the guard swaps lo and hi first).
const checkLeadsSource = `package calc

// Window returns the sums of every run of n consecutive values.
func Window(xs []int, n int) []int {
	var out []int
	for i := 0; i+n < len(xs); i++ {
		s := 0
		for _, x := range xs[i : i+n] {
			s += x
		}
		out = append(out, s)
	}
	return out
}

// Clamp limits v to the range between lo and hi, in either order.
func Clamp(v, lo, hi int) int {
	if lo > hi {
		lo, hi = hi, lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
`

const (
	realLead  = "calc.go:6"
	falseLead = "calc.go:18"
)

func TestEvalCheckLeads(t *testing.T) {
	model := os.Getenv("CODEFORGE_EVAL_MODEL")
	if model == "" {
		t.Skip("set CODEFORGE_EVAL_MODEL=provider/model")
	}
	bin := os.Getenv("CODEFORGE_EVAL_BIN")
	if bin == "" {
		bin = "empryo"
	}
	runs := 4
	if v, err := strconv.Atoi(os.Getenv("CODEFORGE_EVAL_RUNS")); err == nil && v > 0 {
		runs = v
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "calc.go"), []byte(checkLeadsSource), 0o600))
	leads := review.LeadsHeader +
		"- `" + realLead + "` The loop condition `i+n < len(xs)` skips the last window, so `Window([1,2,3], 3)` returns nothing. Use `i+n <= len(xs)`.\n" +
		"- `" + falseLead + "` Clamp returns the wrong bound when lo > hi, because the comparisons assume lo <= hi. Check the order first.\n"
	turn := func(ctx context.Context, prompt string) (string, error) {
		args := []string{"--headless", "--json", "--profile", "codeforge-review", "--mode", "plan", "--model", model,
			"--effort", "medium", "--cwd", dir, "--max-steps", "8", "--marionette-mode", "none",
			"You reviewed the change that adds calc.go in this directory.\n\n" + prompt}
		out, err := exec.CommandContext(ctx, bin, args...).Output()
		var res effort.EmpryoResult
		if jerr := json.Unmarshal(out, &res); jerr != nil {
			return "", fmt.Errorf("run: %w (%w)", err, jerr)
		}
		if res.Error != "" && res.Output == "" {
			return "", fmt.Errorf("run: %s", res.Error) //nolint:err113 // the engine's own message
		}
		return res.Output, nil
	}
	var mu sync.Mutex
	var failed []error
	checked := func(ctx context.Context, prompt string) (string, error) {
		reply, err := turn(ctx, prompt)
		if err != nil {
			mu.Lock()
			failed = append(failed, err)
			mu.Unlock()
		}
		return reply, err
	}
	got := make([]string, runs)
	var wg sync.WaitGroup
	for i := range runs {
		wg.Go(func() { got[i] = checkLeads(t.Context(), leads, checked) })
	}
	wg.Wait()
	require.Empty(t, failed, "every check must run; a failed turn posts the leads unchecked")
	kept, ruledOut := 0, 0
	for i, g := range got {
		t.Logf("run %d:\n%s", i+1, g)
		if strings.Contains(g, "`"+realLead+"`") {
			kept++
		}
		if !strings.Contains(g, "`"+falseLead+"`") {
			ruledOut++
		}
	}
	t.Logf("real lead kept %d/%d, false lead ruled out %d/%d", kept, runs, ruledOut, runs)
	require.Equal(t, runs, kept, "a real blocker must never be ruled out")
	require.Equal(t, runs, ruledOut, "a lead the code disproves must be left off")
}
