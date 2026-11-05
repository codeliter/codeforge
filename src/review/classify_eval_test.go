//nolint:err113 // dynamic error messages are okay in tests
//go:build eval

package review

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/effort"
)

// Run with: go test -tags eval ./src/review -run TestEvalClassify -v
// Judges default to effort.DefaultJudges; set CODEFORGE_JUDGES=provider/model,provider/model to use others,
// for example CODEFORGE_JUDGES=toapis/claude-haiku-5-5,toapis/gpt-6-luna-mini.
// It calls the real judge models, so it costs a few cents. The bar is zero under-classified risky changes
// (a trap) and 85% overall.

func hunk(file string, removed, added []string) string {
	var s strings.Builder
	s.WriteString("diff --git a/" + file + " b/" + file + "\n--- a/" + file + "\n+++ b/" + file + "\n@@ -10,6 +10,6 @@\n")
	for _, l := range removed {
		s.WriteString("-" + l + "\n")
	}
	for _, l := range added {
		s.WriteString("+" + l + "\n")
	}
	return s.String()
}

func repeatLines(n int, format func(i int) string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = format(i)
	}
	return out
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('a'+i%26)) + strings.Repeat("x", i%7))
}

var classifyEvals = []struct {
	name  string
	files []string
	diff  string
	want  Tier
	trap  bool
}{
	{
		"typo inside an auth file", []string{"internal/auth/handler.go"},
		hunk("internal/auth/handler.go",
			[]string{`	return errors.New("recieve failed: invalid credentials")`},
			[]string{`	return errors.New("receive failed: invalid credentials")`}),
		TierSmall, true,
	},
	{
		"flipped authorization check", []string{"internal/api/orders.go"},
		hunk("internal/api/orders.go",
			[]string{`	if order.OwnerID != user.ID && !user.IsAdmin {`, `		return ErrForbidden`, `	}`},
			[]string{`	if order.OwnerID != user.ID || !user.IsAdmin {`, `		return ErrForbidden`, `	}`}),
		TierRisky, true,
	},
	{
		"mutex added around shared map", []string{"internal/cache/cache.go"},
		hunk("internal/cache/cache.go",
			[]string{`func (c *Cache) Get(k string) (string, bool) {`, `	v, ok := c.m[k]`, `	return v, ok`, `}`, `func (c *Cache) Set(k, v string) {`, `	c.m[k] = v`, `}`},
			[]string{`func (c *Cache) Get(k string) (string, bool) {`, `	c.mu.RLock()`, `	v, ok := c.m[k]`, `	c.mu.RUnlock()`, `	return v, ok`, `}`, `func (c *Cache) Set(k, v string) {`, `	c.mu.Lock()`, `	if c.m == nil {`, `		c.m = map[string]string{}`, `	}`, `	c.m[k] = v`, `	c.mu.Unlock()`, `}`}),
		TierRisky, true,
	},
	{
		"comment only", []string{"internal/cache/cache.go"},
		hunk("internal/cache/cache.go",
			[]string{`// Get returns the value`},
			[]string{`// Get returns the value for k and whether it was present.`}),
		TierSmall, true,
	},
	{
		"one word in a log line in a payments file", []string{"internal/payment/charge.go"},
		hunk("internal/payment/charge.go",
			[]string{`	log.Printf("charging card for order %s", id)`},
			[]string{`	log.Printf("charging the card for order %s", id)`}),
		TierSmall, false,
	},
	{
		"large mechanical rename", []string{"internal/store/store.go", "internal/store/query.go"},
		hunk("internal/store/store.go",
			repeatLines(150, func(i int) string { return "	res" + itoa(i) + " := s.fetchRecord(ctx, id)" }),
			repeatLines(150, func(i int) string { return "	res" + itoa(i) + " := s.fetchItem(ctx, id)" })),
		TierSmall, true,
	},
	{
		"delete path now removes data", []string{"internal/jobs/cleanup.go"},
		hunk("internal/jobs/cleanup.go",
			[]string{`	for _, f := range stale {`, `		log.Printf("would delete %s", f)`, `	}`},
			[]string{`	for _, f := range stale {`, `		if err := os.RemoveAll(f); err != nil {`, `			return fmt.Errorf("removing %s: %w", f, err)`, `		}`, `	}`}),
		TierRisky, true,
	},
	{
		"new field exactly like an existing one", []string{"internal/user/user.go", "internal/user/dto.go"},
		hunk("internal/user/user.go",
			[]string{`	FirstName string ` + "`json:\"firstName\"`"},
			[]string{`	FirstName string ` + "`json:\"firstName\"`", `	MiddleName string ` + "`json:\"middleName\"`"}) +
			hunk("internal/user/dto.go",
				[]string{`		FirstName: u.FirstName,`},
				[]string{`		FirstName: u.FirstName,`, `		MiddleName: u.MiddleName,`}),
		TierSmall, false,
	},
	{
		"new retry logic with backoff", []string{"internal/client/retry.go"},
		hunk("internal/client/retry.go",
			[]string{`	return do()`},
			[]string{`	var err error`, `	for attempt := 0; attempt < maxAttempts; attempt++ {`, `		if err = do(); err == nil {`, `			return nil`, `		}`, `		if !retryable(err) {`, `			return err`, `		}`, `		sleep := base << attempt`, `		sleep += time.Duration(rand.Int63n(int64(sleep) / 2))`, `		select {`, `		case <-time.After(sleep):`, `		case <-ctx.Done():`, `			return ctx.Err()`, `		}`, `	}`, `	return err`}),
		TierStandard, false,
	},
	{
		"sql migration drops a column", []string{"db/migrations/0042_drop_legacy.sql"},
		hunk("db/migrations/0042_drop_legacy.sql", nil, []string{`ALTER TABLE users DROP COLUMN legacy_id;`}),
		TierRisky, true,
	},
	{
		"test only", []string{"internal/auth/token_test.go"},
		hunk("internal/auth/token_test.go", nil, repeatLines(40, func(i int) string { return "	require.Equal(t, " + itoa(i) + ", " + itoa(i) + ")" })),
		TierSmall, false,
	},
	{
		"docs only", []string{"README.md"},
		hunk("README.md", []string{`Run it.`}, []string{`Run it with --verbose for more output.`}),
		TierDocs, false,
	},
	{
		"secret rotation in config loader", []string{"internal/config/secrets.go"},
		hunk("internal/config/secrets.go",
			[]string{`	key := os.Getenv("SIGNING_KEY")`},
			[]string{`	key := os.Getenv("SIGNING_KEY")`, `	if key == "" {`, `		key = defaultDevKey`, `	}`}),
		TierRisky, true,
	},
	{
		"new exported helper with callers elsewhere", []string{"internal/format/bytes.go"},
		hunk("internal/format/bytes.go",
			[]string{`func Bytes(n int64) string {`},
			[]string{`func Bytes(n int64) string {`, `	if n < 0 {`, `		return "-" + Bytes(-n)`, `	}`}),
		TierStandard, false,
	},
}

func TestEvalClassify(t *testing.T) {
	bin, err := exec.LookPath("empryo")
	require.NoError(t, err)
	judgeModels := effort.DefaultJudges
	if v := os.Getenv("CODEFORGE_JUDGES"); v != "" {
		judgeModels = nil
		for m := range strings.SplitSeq(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				judgeModels = append(judgeModels, m)
			}
		}
	}
	var judges []Judge
	for _, m := range judgeModels {
		judges = append(judges, effort.EmpryoJudge{Bin: bin, Model: m})
	}

	order := map[string]int{TierDocs.Name: 0, TierSmall.Name: 1, TierStandard.Name: 2, TierRisky.Name: 3}
	exact, under := 0, 0
	var spent time.Duration
	for _, e := range classifyEvals {
		start := time.Now()
		got, why := Classify(t.Context(), judges, e.diff, e.files)
		spent += time.Since(start)
		if strings.Contains(why, "no judge answered") {
			t.Fatalf("the judges did not answer for %q, so this run measures nothing: %s\nCheck that `empryo` is authenticated and the models in DefaultJudges are available.", e.name, why)
		}

		switch {
		case got.Name == e.want.Name:
			exact++
		case order[got.Name] < order[e.want.Name]:
			under++
			t.Errorf("UNDER-CLASSIFIED %q: want %s got %s (%s)", e.name, e.want.Name, got.Name, why)
		default:
			t.Logf("over  %q: want %s got %s (%s)", e.name, e.want.Name, got.Name, why)
		}
	}

	t.Logf("exact %d/%d, under-classified %d, judge time %s total (%s per case)",
		exact, len(classifyEvals), under, spent.Round(time.Second), (spent / time.Duration(len(classifyEvals))).Round(time.Second))
	require.Zero(t, under, "an under-classified risky change is the dangerous error")
	require.GreaterOrEqual(t, exact*100/len(classifyEvals), 85)
}
