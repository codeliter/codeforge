package src

import (
	"maps"
	"slices"
)

const inheritRouting = "@inherit"

func cloneConfig(cfg Config) Config {
	cfg.Providers = cfg.Providers.clone()
	cfg.Judges = slices.Clone(cfg.Judges)
	cfg.Review.SecondOpinion = slices.Clone(cfg.Review.SecondOpinion)
	cfg.Review.SensitivePaths = slices.Clone(cfg.Review.SensitivePaths)
	cfg.Routing = maps.Clone(cfg.Routing)
	return cfg
}

// Keep inherited roles inherited; preserve pre-existing explicit project pins.
func sameProviderPolicy(a, b *ProviderPolicy) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return providerID(a.Primary) == providerID(b.Primary) && slices.Equal(a.Backups(), b.Backups()) &&
		slices.EqualFunc(a.Rules, b.Rules, func(x, y ProviderRule) bool {
			return ruleKey(x.Match) == ruleKey(y.Match) && providerID(x.Primary) == providerID(y.Primary) &&
				slices.Equal(x.chain().Backups(), y.chain().Backups())
		})
}
func projectModelDelta(existing, baseline, edited Config) Config {
	out := cloneConfig(existing)
	if !sameProviderPolicy(baseline.Providers, edited.Providers) {
		if edited.Providers == nil {
			out.Providers = &ProviderPolicy{}
		} else {
			out.Providers = edited.Providers.clone()
		}
	}
	for _, role := range roles {
		if !slices.Equal(current(baseline, role.key), current(edited, role.key)) {
			assignModelRole(&out, role.key, slices.Clone(current(edited, role.key)))
		}
	}
	out.Routing = maps.Clone(edited.Routing)
	return out
}
func prepareProjectModelPreview(global, local, baseline Config, edited *Config, catalog []string) (Config, error) {
	delta := projectModelDelta(local, baseline, *edited)
	// Removing provider defaults must retain the old provider for inherited named
	// helpers; otherwise two connected providers make those names ambiguous.
	if baseline.Providers != nil {
		for job, ref := range global.Routing {
			if _, overridden := delta.Routing[job]; overridden || ref == "" || validModelID(ref) {
				continue
			}
			// Pin only the names whose provider the edit takes away (their rule or the default order is gone).
			if baseline.Providers.primaryFor(ref) == "" || edited.Providers.primaryFor(ref) != "" {
				continue
			}
			route, err := resolveModelReference("routing."+job, ref, baseline.Providers, catalog)
			if err != nil {
				return Config{}, err
			}
			id := route.Primary
			if edited.Routing == nil {
				edited.Routing = make(map[string]string)
			}
			edited.Routing[job] = id
		}
		delta = projectModelDelta(local, baseline, *edited)
	}
	return configDefaults(mergeConfig(global, delta)), nil
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
