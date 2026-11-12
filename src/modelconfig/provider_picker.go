package modelconfig

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"
)

func catalogProviders(catalog []string) []string {
	var ids []string
	for _, model := range catalog {
		id, _, ok := strings.Cut(model, "/")
		if ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return slices.Compact(ids)
}

func pickProviderPolicy(cfg *config.Config, catalog []string) error {
	mode := "defaults"
	if cfg.Providers == nil || cfg.Providers.Primary == "" && len(cfg.Providers.Rules) == 0 {
		mode = "advanced"
	}
	mode, err := forms.Choose("How should models use providers?", "", []forms.Option{
		{Label: "Choose primary/fallback once, then use model names (recommended)", Value: "defaults"},
		{Label: "Keep explicit provider/model choices (advanced)", Value: "advanced"},
	}, mode)
	if err != nil {
		return err
	}
	if mode == "advanced" {
		// Resolve names using the previous policy before dropping that context.
		if config.NeedsCatalog(*cfg) {
			source := config.Clone(*cfg)
			if source.Providers != nil {
				source.Providers.Fallback, source.Providers.Fallbacks = "", nil
			}
			plan, err := config.ResolvePlan(source, catalog)
			if err != nil {
				return err
			}
			*cfg = plan.Config
		}
		if cfg.Providers != nil && len(cfg.Providers.Rules) > 0 {
			fmt.Fprintln(os.Stderr, "codeforge: explicit provider/model choices do not use provider rules; the saved rules are removed.")
		}
		cfg.Providers = nil
		return nil
	}
	ids := catalogProviders(catalog)
	if len(ids) == 0 {
		return config.ErrNoModels
	}
	primary, backup, extra := ids[0], "", []string(nil)
	if slices.Contains(ids, "llmgateway") {
		primary = "llmgateway"
	} else if slices.Contains(ids, "copilot") {
		primary = "copilot"
	}
	if cfg.Providers != nil {
		if slices.Contains(ids, config.ProviderID(cfg.Providers.Primary)) {
			primary = config.ProviderID(cfg.Providers.Primary)
		}
		if backups := cfg.Providers.Backups(); len(backups) > 0 {
			backup, extra = backups[0], backups[1:]
		}
	} else if primary != "copilot" && slices.Contains(ids, "copilot") {
		backup = "copilot"
	}
	var primaries []forms.Option
	for _, id := range ids {
		label := id
		if id == "llmgateway" {
			label = "LLM Gateway / DevPass (uses your existing sign-in)"
		}
		primaries = append(primaries, forms.Option{Label: label, Value: id})
	}
	if primary, err = forms.Choose("Primary provider", "Codeforge does not change accounts, keys, or subscription billing.", primaries, primary); err != nil {
		return err
	}
	if backup == primary || !slices.Contains(ids, backup) {
		backup = ""
	}
	fallbacks := []forms.Option{{Label: "Keep native Empryo fallback policy (no Codeforge preference)", Value: ""}}
	for _, id := range ids {
		if id != primary {
			fallbacks = append(fallbacks, forms.Option{Label: id, Value: id})
		}
	}
	if backup, err = forms.Choose("First fallback provider", "Empryo switches to it when the primary fails or runs out of quota. Add more, in order, with codeforge provider set.", fallbacks, backup); err != nil {
		return err
	}
	var chain []string
	if backup != "" {
		chain = append(chain, backup)
		// Keep further backups chosen with codeforge provider set, unless they are now primary or disconnected.
		for _, id := range extra {
			if id != primary && id != backup && slices.Contains(ids, id) && !slices.Contains(chain, id) {
				chain = append(chain, id)
			}
		}
	}
	policy := config.NewProviderPolicy(primary, chain)
	if cfg.Providers != nil {
		policy.Rules = cfg.Providers.Clone().Rules
	}
	useProviderDefaults(cfg, policy, catalog)
	return nil
}
func useProviderDefaults(cfg *config.Config, policy *config.ProviderPolicy, catalog []string) {
	cfg.Providers = policy.Clone()
	// A pinned ID becomes a name only when the provider its model runs on first (its rule's, else the default) serves it.
	name := func(ref string) string {
		if primary := cfg.Providers.PrimaryFor(config.ModelName(ref)); primary != "" {
			if id, err := config.CatalogModel(config.ModelName(ref), primary, catalog); err == nil {
				return config.ModelName(id)
			}
		}
		return ref
	}
	for _, role := range config.Roles {
		refs := slices.Clone(config.Current(*cfg, role.Key))
		for i, ref := range refs {
			if config.ValidModelID(ref) {
				refs[i] = name(ref)
			}
		}
		config.AssignRole(cfg, role.Key, refs)
	}
	for job, ref := range cfg.Routing {
		if config.ValidModelID(ref) {
			cfg.Routing[job] = name(ref)
		}
	}
}

func providerModelOptions(catalog []string, policy *config.ProviderPolicy, saved ...string) []forms.Option {
	if policy == nil || policy.Primary == "" && len(policy.Rules) == 0 {
		return modelOptions(catalog, saved...)
	}
	var options []forms.Option
	seen := make(map[string]bool)
	for _, id := range catalog {
		p, _, _ := strings.Cut(id, "/")
		value := config.ModelName(id)
		// Each model is offered on the provider its own chain starts with: its rule's, else the default one.
		chain := policy.ChainFor(value)
		primary := config.ProviderID(chain.Primary)
		if !config.SuggestedModel(id) {
			continue
		}
		label := value
		if primary == "" {
			// No chain covers this model (rules without a default order): offer its name when only one provider
			// serves it, else the explicit provider/model ID.
			ref, text := id, id+" (explicit; native fallback)"
			same := 0
			for _, other := range catalog {
				if config.ModelReferenceKey(config.ModelName(other)) == config.ModelReferenceKey(value) {
					same++
				}
			}
			if same == 1 {
				ref, text = value, value
			}
			if !seen[ref] {
				seen[ref] = true
				options = append(options, forms.Option{Label: text, Value: ref})
			}
			continue
		}
		if p != primary {
			continue
		}
		if _, err := config.CatalogModel(value, primary, catalog); err != nil {
			value = id
			label = id + " (explicit override; native fallback)"
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		if value != id && len(chain.Backups()) > 0 {
			var backups []string
			missing := false
			// Under a rule a stand-in stays in the model's own family, as resolveModelReference does.
			standIns := catalog
			if _, ruled := policy.RuleFor(value); ruled {
				fam := config.Family(id)
				standIns = slices.DeleteFunc(slices.Clone(catalog), func(c string) bool { return config.Family(c) != fam })
			}
			for _, provider := range chain.Backups() {
				if backup, err := config.CatalogModel(value, provider, catalog); err == nil {
					backups = append(backups, backup)
				} else if standIn, ok := config.EquivalentModel(id, provider, standIns); ok {
					backups = append(backups, standIn+" (closest)")
				} else {
					missing = true
				}
			}
			if missing {
				label += " (nothing comparable on a backup provider)"
			} else {
				label += " (backups: " + strings.Join(backups, " > ") + ")"
			}
		}
		options = append(options, forms.Option{Label: label, Value: value})
	}
	for _, ref := range saved {
		if ref == "" || seen[ref] || !config.SuggestedModel(ref) {
			continue
		}
		seen[ref] = true
		label := "Saved name: " + ref
		if config.ValidModelID(ref) {
			label = "Saved explicit override: " + ref
		}
		options = append(options, forms.Option{Label: label, Value: ref})
	}
	return options
}

func modelPlanPreview(plan config.ModelPlan) string {
	var text strings.Builder
	fmt.Fprintln(&text, "Resolved catalog IDs:")
	for _, route := range plan.Routes {
		backup := strings.Join(markStandIns(route), " > ")
		if backup == "" {
			backup = "native policy (explicit override or no preference)"
		}
		fmt.Fprintf(&text, "%s: %s; backup: %s\n", route.Role, route.Primary, backup)
	}
	if len(plan.FallbackChains()) > 0 {
		fmt.Fprintln(&text, "Normal launches only verify these chains. Run codeforge models apply to choose scope, review changes, and confirm applying them.")
	}
	return text.String()
}
