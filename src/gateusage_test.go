package src

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/effort"
)

func TestGateUsage_TotalsAndRecord(t *testing.T) {
	g := &gateUsage{}
	g.add(effort.Usage{Model: "a", Input: 100, Output: 10, Steps: 1, Cost: 0.01})
	g.add(effort.Usage{Model: "b", Input: 300, CacheRead: 50, Steps: 4, Cost: 0.04, Duration: time.Second})

	tot := g.totals()
	require.Equal(t, 2, tot.Calls)
	require.Equal(t, 400, tot.Input)
	require.Equal(t, 5, tot.Steps)
	require.InDelta(t, 0.05, tot.Cost, 1e-9)

	path := filepath.Join(t.TempDir(), "usage.jsonl")
	t.Setenv(gateUsageEnv, path)
	g.record(gateRecord{Tier: "small", Models: []string{"b"}, Passed: true})
	g.record(gateRecord{Tier: "risky"})

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var tiers []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r gateRecord
		require.NoError(t, json.Unmarshal(sc.Bytes(), &r))
		require.Equal(t, 400, r.Totals.Input)
		require.Len(t, r.Calls, 2)
		tiers = append(tiers, r.Tier)
	}
	require.Equal(t, []string{"small", "risky"}, tiers, "one line per run, appended")
}

func TestGateUsage_NoFileNoRecord(t *testing.T) {
	t.Setenv(gateUsageEnv, "")
	(&gateUsage{}).record(gateRecord{Tier: "small"})
}
