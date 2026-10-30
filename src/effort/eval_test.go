//nolint:err113 // dynamic error messages are okay in tests
//go:build eval

package effort

import (
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Run with: go test -tags eval ./src/effort -run TestEval -v
var evals = []struct {
	task string
	want Level
	trap bool
}{
	{`Fix the typo "recieve" in the login error string in internal/auth/handler.go`, Low, true},
	{`Change replicas from 3 to 5 in deploy/prod/api-server.yaml`, High, true},
	{`JIRA-4821: rename the "Submit" button label to "Send"`, Low, true},
	{`Make it faster`, High, true},
	{`Write the PR title and description for the currently staged diff`, Low, true},
	{`Add a "middleName" field to User exactly like the existing "firstName" field (struct, DTO, migration)`, Medium, true},
	{`Fix flaky TestWorkerShutdown — it intermittently deadlocks under go test -race`, High, true},
	{`Rename local variable "d" to "duration" in internal/timer/tick.go`, Low, false},
	{`Update README.md to document the existing --verbose flag`, Low, false},
	{`Add a unit test for the existing FormatBytes helper covering KB/MB/GB`, Low, false},
	{`Fix the off-by-one in paginate() that returns 11 items per page instead of 10`, Low, false},
	{`Extract the CSV-parsing block duplicated in cmd/import, cmd/export, cmd/report into one shared helper`, Medium, false},
	{`Bump the jwt library one minor version to patch a CVE and update the two call sites`, Medium, false},
	{`Move all HTTP handlers from package web to package api and fix every import`, Medium, false},
	{`Add a --dry-run flag to the cleanup command that deletes session files older than 30 days`, Medium, false},
	{`Rewrite the payment retry logic to use exponential backoff with jitter across all three payment providers`, High, false},
	{`Design and implement a plugin system so third parties can register custom exporters`, High, false},
	{`PROJ-99: migrate the entire data layer from gorm to sqlc`, High, false},
	{`Investigate why memory spikes in production every night around 2am and fix it`, High, false},
	{`Optimize the JSON serialization hot path; it dominates our pprof CPU profiles`, High, false},
}

func TestEval(t *testing.T) {
	bin, err := exec.LookPath("empryo")
	require.NoError(t, err)
	var judges []Judge
	for _, m := range DefaultJudges {
		judges = append(judges, EmpryoJudge{Bin: bin, Model: m})
	}
	c, err := New(Options{Judges: judges, Timeout: time.Minute})
	require.NoError(t, err)

	hits := 0
	for _, e := range evals {
		got, err := c.Classify(t.Context(), e.task, Signals{})
		require.NoError(t, err, e.task)
		if got.Level == e.want {
			hits++
			continue
		}
		t.Logf("MISS want=%s got=%s merged=%+v votes=%+v task=%q", e.want, got.Level, got.Scores, got.Votes, e.task)
		if e.trap {
			t.Errorf("trap misclassified: %q", e.task)
		}
	}

	t.Logf("accuracy %d/%d", hits, len(evals))
	require.GreaterOrEqual(t, hits*100/len(evals), 85)
}
