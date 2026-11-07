package src

import (
	"maps"
	"slices"

	"github.com/codeliter/codeforge/src/config"
)

const inheritRouting = "@inherit"

// Keep inherited roles inherited; preserve pre-existing explicit project pins.
func sameProviderPolicy(a, b *config.ProviderPolicy) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return config.ProviderID(a.Primary) == config.ProviderID(b.Primary) && slices.Equal(a.Backups(), b.Backups()) &&
		slices.EqualFunc(a.Rules, b.Rules, func(x, y config.ProviderRule) bool {
			return config.RuleKey(x.Match) == config.RuleKey(y.Match) && config.ProviderID(x.Primary) == config.ProviderID(y.Primary) &&
				slices.Equal(x.Chain().Backups(), y.Chain().Backups())
		})
}
func projectModelDelta(existing, baseline, edited config.Config) config.Config {
	out := config.Clone(existing)
	if !sameProviderPolicy(baseline.Providers, edited.Providers) {
		if edited.Providers == nil {
			out.Providers = &config.ProviderPolicy{}
		} else {
			out.Providers = edited.Providers.Clone()
		}
	}
	for _, role := range config.Roles {
		if !slices.Equal(config.Current(baseline, role.Key), config.Current(edited, role.Key)) {
			config.AssignRole(&out, role.Key, slices.Clone(config.Current(edited, role.Key)))
		}
	}
	out.Routing = maps.Clone(edited.Routing)
	return out
}
func prepareProjectModelPreview(global, local, baseline config.Config, edited *config.Config, catalog []string) (config.Config, error) {
	delta := projectModelDelta(local, baseline, *edited)
	// Removing provider defaults must retain the old provider for inherited named
	// helpers; otherwise two connected providers make those names ambiguous.
	if baseline.Providers != nil {
		for job, ref := range global.Routing {
			if _, overridden := delta.Routing[job]; overridden || ref == "" || config.ValidModelID(ref) {
				continue
			}
			// Pin only the names whose provider the edit takes away (their rule or the default order is gone).
			if baseline.Providers.PrimaryFor(ref) == "" || edited.Providers.PrimaryFor(ref) != "" {
				continue
			}
			route, err := config.ResolveModelReference("routing."+job, ref, baseline.Providers, catalog)
			if err != nil {
				return config.Config{}, err
			}
			id := route.Primary
			if edited.Routing == nil {
				edited.Routing = make(map[string]string)
			}
			edited.Routing[job] = id
		}
		delta = projectModelDelta(local, baseline, *edited)
	}
	return config.Defaults(config.Merge(global, delta)), nil
}

func routingPickerValue(routing map[string]string, job string) string {
	if value, exists := routing[job]; exists {
		return value
	}
	return inheritRouting
}

func applyRoutingChoices(cfg *config.Config, jobs, values []string) {
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
