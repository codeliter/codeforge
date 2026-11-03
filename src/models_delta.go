package src

import (
	"maps"
	"slices"
)

const inheritRouting = "@inherit"

func cloneConfig(cfg Config) Config {
	cfg.Judges = slices.Clone(cfg.Judges)
	cfg.Review.SecondOpinion = slices.Clone(cfg.Review.SecondOpinion)
	cfg.Routing = maps.Clone(cfg.Routing)
	return cfg
}

// Keep inherited roles inherited; preserve pre-existing explicit project pins.
func projectModelDelta(existing, baseline, edited Config) Config {
	out := cloneConfig(existing)
	for _, role := range roles {
		if !slices.Equal(current(baseline, role.key), current(edited, role.key)) {
			assignModelRole(&out, role.key, slices.Clone(current(edited, role.key)))
		}
	}
	out.Routing = maps.Clone(edited.Routing)
	return out
}

func routingPickerValue(routing map[string]string, job string) string {
	if value, exists := routing[job]; exists {
		return value
	}
	return inheritRouting
}

func applyRoutingChoices(cfg *Config, jobs, values []string) {
	for i, job := range jobs {
		if values[i] == inheritRouting {
			delete(cfg.Routing, job)
			continue
		}
		if cfg.Routing == nil {
			cfg.Routing = make(map[string]string)
		}
		cfg.Routing[job] = values[i]
	}
}
