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
	defer levelsMu.Unlock()
	if lv, ok := levelsCache[model]; ok {
		return lv
	}

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "--headless", "--no-profile", "--no-genome", "--model", model, "--effort", "codeforge-probe", "x")
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err == nil {
		levelsCache[model] = nil
		return nil
	}

	var levels []string
	if m := validLevels.FindStringSubmatch(out.String()); m != nil {
		for lvl := range strings.SplitSeq(m[1], ",") {
			levels = append(levels, strings.TrimSpace(lvl))
		}
	}
	levelsCache[model] = levels
	return levels
}
