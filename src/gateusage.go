package src

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/codeliter/codeforge/src/effort"
)

// gateUsageEnv names a file that receives one JSON line per gate run, for comparing cost between versions.
const (
	gateUsageEnv  = "CODEFORGE_GATE_USAGE"
	usageFilePerm = 0o600
)

// gateUsage tallies what the judges and reviewers of one gate run spent, as the engine reports it.
type gateUsage struct {
	mu    sync.Mutex
	calls []effort.Usage
}

func (g *gateUsage) add(u effort.Usage) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, u)
}

type usageTotals struct {
	Calls      int     `json:"Calls"`      //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	Input      int     `json:"Input"`      //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	Output     int     `json:"Output"`     //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	CacheRead  int     `json:"CacheRead"`  //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	CacheWrite int     `json:"CacheWrite"` //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	Steps      int     `json:"Steps"`      //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	Cost       float64 `json:"Cost"`       //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
	// Models counts calls per model, so a record shows which models it actually paid for.
	Models map[string]int `json:"Models"` //nolint:tagliatelle // Keys read by scripts/gate-cost.sh.
}

func (g *gateUsage) totals() usageTotals {
	g.mu.Lock()
	defer g.mu.Unlock()
	var t usageTotals
	t.Models = map[string]int{}
	for _, u := range g.calls {
		t.Calls++
		t.Input += u.Input
		t.Output += u.Output
		t.CacheRead += u.CacheRead
		t.CacheWrite += u.CacheWrite
		t.Steps += u.Steps
		t.Cost += u.Cost
		t.Models[u.Model]++
	}
	return t
}

func (t usageTotals) String() string {
	return fmt.Sprintf("%d model calls, %d steps, %d input + %d cached-read + %d output tokens, cost %.4f",
		t.Calls, t.Steps, t.Input, t.CacheRead, t.Output, t.Cost)
}

type gateRecord struct {
	At       time.Time      `json:"at"`
	Tier     string         `json:"tier"`
	Models   []string       `json:"models"`
	Files    int            `json:"files"`
	Brief    int            `json:"briefBytes"`     //nolint:tagliatelle // Read by scripts/gate-cost.sh.
	Elapsed  float64        `json:"elapsedSeconds"` //nolint:tagliatelle // Read by scripts/gate-cost.sh.
	Passed   bool           `json:"passed"`
	Blocking int            `json:"blocking"`
	FollowUp bool           `json:"followUp"` //nolint:tagliatelle // Read by scripts/gate-cost.sh.
	Totals   usageTotals    `json:"totals"`
	Calls    []effort.Usage `json:"calls"`
}

// record appends the run to the file named by CODEFORGE_GATE_USAGE. A failure only loses a measurement.
func (g *gateUsage) record(r gateRecord) {
	path := os.Getenv(gateUsageEnv)
	if path == "" {
		return
	}
	g.mu.Lock()
	r.Calls = append([]effort.Usage(nil), g.calls...)
	g.mu.Unlock()
	r.Totals = g.totals()

	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, usageFilePerm)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n')) //nolint:errcheck // a lost line only loses a measurement
}
