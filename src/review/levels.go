package review

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
)

var (
	ladder      = []string{"off", "none", "minimal", "low", "medium", "high", "xhigh", "max"}
	validLevels = regexp.MustCompile(`Valid: ([a-z, ]+)`)

	levelsMu    sync.Mutex
	levelsCache = map[string][]string{}
)

// fitEffort picks the highest level the model supports at or below want, so a tier never escalates past its budget.
// A model with nothing at or below want gets its lowest supported level.
func fitEffort(want string, supported []string) string {
	if len(supported) == 0 || slices.Contains(supported, want) {
		return want
	}

	limit := slices.Index(ladder, want)
	best := ""
	for _, lvl := range ladder[:limit+1] {
		if slices.Contains(supported, lvl) && lvl != "off" && lvl != "none" {
			best = lvl
		}
	}
	if best != "" {
		return best
	}

	for _, lvl := range ladder {
		if slices.Contains(supported, lvl) && lvl != "off" && lvl != "none" {
			return lvl
		}
	}
	return want
}

// supportedEfforts asks Empryo once per model and process; Empryo lists valid levels when it rejects one.
func supportedEfforts(ctx context.Context, bin, model string) []string {
	levelsMu.Lock()
	lv, ok := levelsCache[model]
	levelsMu.Unlock()
	if ok {
		return lv
	}

	// The probe runs without the lock so jurors for different models probe in parallel, not one after another.
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "--headless", "--no-profile", "--no-genome", "--model", model, "--effort", "codeforge-probe", "x")
	Bound(cmd)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	Reap(cmd)
	if err == nil {
		levelsMu.Lock()
		levelsCache[model] = nil
		levelsMu.Unlock()
		return nil
	}

	var levels []string
	if m := validLevels.FindStringSubmatch(out.String()); m != nil {
		for lvl := range strings.SplitSeq(m[1], ",") {
			levels = append(levels, strings.TrimSpace(lvl))
		}
	}
	levelsMu.Lock()
	levelsCache[model] = levels
	levelsMu.Unlock()
	return levels
}

// EffortFor lowers want to the nearest level model supports, asking Empryo once per model, so a time-boxed default never
// makes the engine reject a reviewer that lacks it. An unanswered probe leaves want unchanged.
func EffortFor(ctx context.Context, bin, model, want string) string {
	return fitEffort(want, supportedEfforts(ctx, bin, model))
}
