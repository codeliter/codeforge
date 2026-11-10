package config

import (
	"slices"
	"strconv"
	"strings"
)

// Model levels group models that can stand in for one another when a backup provider lacks the exact one.
const (
	levelFrontier = "frontier"
	levelEveryday = "everyday"
	levelFast     = "fast"
)

// levelWords maps a word in a model name to its level. Names with none of these words have no known level,
// so they are only replaced by another version of the same model line.
var levelWords = map[string]string{
	"opus": levelFrontier, "fable": levelFrontier, "mythos": levelFrontier, "sol": levelFrontier, "pro": levelFrontier, "ultra": levelFrontier, "kimi": levelFrontier,
	"sonnet": levelEveryday,
	"haiku":  levelFast, "luna": levelFast, "flash": levelFast, "nano": levelFast, "mini": levelFast, "lite": levelFast,
}

// modelShape splits a model name into its line (the words, e.g. "gpt sol") and its version numbers (e.g. 6 1).
type modelShape struct {
	line    string
	vendor  string
	level   string
	version []int
}

func shapeOf(name string) modelShape {
	var words []string
	var shape modelShape
	for _, token := range strings.Split(ModelReferenceKey(ModelName(name)), "-") {
		if n, err := strconv.Atoi(token); err == nil {
			shape.version = append(shape.version, n)
			continue
		}
		if token == "" {
			continue
		}
		words = append(words, token)
		if level, ok := levelWords[token]; ok && shape.level == "" {
			shape.level = level
		}
	}
	shape.line = strings.Join(words, " ")
	if len(words) > 0 {
		shape.vendor = words[0]
	}
	return shape
}

// compareVersions orders version numbers; a missing part counts as zero, so 5 equals 5.0.
func compareVersions(a, b []int) int {
	for i := range max(len(a), len(b)) {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

// equivalentModel finds the closest stand-in for want on provider when the exact model is not served there.
// It prefers, in order: another version of the same model line (the nearest older one, else the nearest newer),
// then a model of the same level from the same vendor, then from any vendor, newest first.
// It reports false when nothing of the same line or level exists, so a provider is only refused when it truly
// has nothing comparable.
func EquivalentModel(want, provider string, catalog []string) (string, bool) {
	target := shapeOf(want)
	provider = ProviderID(provider)
	var sameLine, sameLevel []string
	for _, id := range catalog {
		p, _, ok := strings.Cut(id, "/")
		if !ok || p != provider || !SuggestedModel(id) {
			continue
		}
		shape := shapeOf(id)
		switch {
		case shape.line == target.line && target.line != "":
			sameLine = append(sameLine, id)
		case target.level != "" && shape.level == target.level:
			sameLevel = append(sameLevel, id)
		}
	}
	if len(sameLine) > 0 {
		slices.SortStableFunc(sameLine, func(a, b string) int {
			da, db := compareVersions(shapeOf(a).version, target.version), compareVersions(shapeOf(b).version, target.version)
			// Older-or-equal versions first (closest first), then newer ones (closest first).
			if (da <= 0) != (db <= 0) {
				if da <= 0 {
					return -1
				}
				return 1
			}
			if da <= 0 {
				return compareVersions(shapeOf(b).version, shapeOf(a).version)
			}
			return compareVersions(shapeOf(a).version, shapeOf(b).version)
		})
		return sameLine[0], true
	}
	if len(sameLevel) > 0 {
		slices.SortStableFunc(sameLevel, func(a, b string) int {
			sa, sb := shapeOf(a), shapeOf(b)
			if (sa.vendor == target.vendor) != (sb.vendor == target.vendor) {
				if sa.vendor == target.vendor {
					return -1
				}
				return 1
			}
			if c := compareVersions(sb.version, sa.version); c != 0 {
				return c
			}
			return strings.Compare(a, b)
		})
		return sameLevel[0], true
	}
	return "", false
}

// SuggestedModel excludes models the project must never recommend or offer.
func SuggestedModel(id string) bool {
	return !strings.HasPrefix(ModelReferenceKey(ModelName(id)), "gemini-4-argon")
}
