package src

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
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

func pickProviderPolicy(cfg *Config, catalog []string) error {
	mode := "defaults"
	if cfg.Providers == nil || cfg.Providers.Primary == "" && len(cfg.Providers.Rules) == 0 {
		mode = "advanced"
	}
	if err := runForm("provider selection style", huh.NewSelect[string]().Title("How should models use providers?").Options(huh.NewOption("Choose primary/fallback once, then use model names (recommended)", "defaults"), huh.NewOption("Keep explicit provider/model choices (advanced)", "advanced")).Value(&mode)); err != nil {
		return err
	}
	if mode == "advanced" {
		// Resolve names using the previous policy before dropping that context.
		if needsModelCatalog(*cfg) {
			source := cloneConfig(*cfg)
			if source.Providers != nil {
				source.Providers.Fallback, source.Providers.Fallbacks = "", nil
			}
			plan, err := resolveModelPlan(source, catalog)
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
		return errNoModels
	}
	primary, backup, extra := ids[0], "", []string(nil)
	if slices.Contains(ids, "llmgateway") {
		primary = "llmgateway"
	} else if slices.Contains(ids, "copilot") {
		primary = "copilot"
	}
	if cfg.Providers != nil {
		if slices.Contains(ids, providerID(cfg.Providers.Primary)) {
			primary = providerID(cfg.Providers.Primary)
		}
		if backups := cfg.Providers.Backups(); len(backups) > 0 {
			backup, extra = backups[0], backups[1:]
		}
	} else if primary != "copilot" && slices.Contains(ids, "copilot") {
		backup = "copilot"
	}
	var primaries []huh.Option[string]
	for _, id := range ids {
		label := id
		if id == "llmgateway" {
			label = "LLM Gateway / DevPass (uses your existing sign-in)"
		}
		primaries = append(primaries, huh.NewOption(label, id))
	}
	if err := runForm("primary provider", huh.NewSelect[string]().Title("Primary provider").Description("Codeforge does not change accounts, keys, or subscription billing.").Options(primaries...).Value(&primary)); err != nil {
		return err
	}
	if backup == primary || !slices.Contains(ids, backup) {
		backup = ""
	}
	fallbacks := []huh.Option[string]{huh.NewOption("Keep native Empryo fallback policy (no Codeforge preference)", "")}
	for _, id := range ids {
		if id != primary {
			fallbacks = append(fallbacks, huh.NewOption(id, id))
		}
	}
	if err := runForm("fallback provider", huh.NewSelect[string]().Title("First fallback provider").Description("Empryo switches to it when the primary fails or runs out of quota. Add more, in order, with codeforge provider set.").Options(fallbacks...).Value(&backup)); err != nil {
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
	policy := newProviderPolicy(primary, chain)
	if cfg.Providers != nil {
		policy.Rules = cfg.Providers.clone().Rules
	}
	useProviderDefaults(cfg, policy, catalog)
	return nil
}
func useProviderDefaults(cfg *Config, policy *ProviderPolicy, catalog []string) {
	cfg.Providers = policy.clone()
	// A pinned ID becomes a name only when the provider its model runs on first (its rule's, else the default) serves it.
	name := func(ref string) string {
		if primary := cfg.Providers.primaryFor(modelName(ref)); primary != "" {
			if id, err := catalogModel(modelName(ref), primary, catalog); err == nil {
				return modelName(id)
			}
		}
		return ref
	}
	for _, role := range roles {
		refs := slices.Clone(current(*cfg, role.key))
		for i, ref := range refs {
			if validModelID(ref) {
				refs[i] = name(ref)
			}
		}
		assignModelRole(cfg, role.key, refs)
	}
	for job, ref := range cfg.Routing {
		if validModelID(ref) {
			cfg.Routing[job] = name(ref)
		}
	}
}

func providerModelOptions(catalog []string, policy *ProviderPolicy, saved ...string) []huh.Option[string] {
	if policy == nil || policy.Primary == "" && len(policy.Rules) == 0 {
		return modelOptions(catalog, saved...)
	}
	var options []huh.Option[string]
	seen := make(map[string]bool)
	for _, id := range catalog {
		p, _, _ := strings.Cut(id, "/")
		value := modelName(id)
		// Each model is offered on the provider its own chain starts with: its rule's, else the default one.
		chain := policy.chainFor(value)
		primary := providerID(chain.Primary)
		if !suggestedModel(id) {
			continue
		}
		label := value
		if primary == "" {
			// No chain covers this model (rules without a default order): offer its name when only one provider
			// serves it, else the explicit provider/model ID.
			ref, text := id, id+" (explicit; native fallback)"
			same := 0
			for _, other := range catalog {
				if modelReferenceKey(modelName(other)) == modelReferenceKey(value) {
					same++
				}
			}
			if same == 1 {
				ref, text = value, value
			}
			if !seen[ref] {
				seen[ref] = true
				options = append(options, huh.NewOption(text, ref))
			}
			continue
		}
		if p != primary {
			continue
		}
		if _, err := catalogModel(value, primary, catalog); err != nil {
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
			if _, ruled := policy.ruleFor(value); ruled {
				fam := family(id)
				standIns = slices.DeleteFunc(slices.Clone(catalog), func(c string) bool { return family(c) != fam })
			}
			for _, provider := range chain.Backups() {
				if backup, err := catalogModel(value, provider, catalog); err == nil {
					backups = append(backups, backup)
				} else if standIn, ok := equivalentModel(id, provider, standIns); ok {
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
		options = append(options, huh.NewOption(label, value))
	}
	for _, ref := range saved {
		if ref == "" || seen[ref] || !suggestedModel(ref) {
			continue
		}
		seen[ref] = true
		label := "Saved name: " + ref
		if validModelID(ref) {
			label = "Saved explicit override: " + ref
		}
		options = append(options, huh.NewOption(label, ref))
	}
	return options
}

func modelPlanPreview(plan ModelPlan) string {
	var text strings.Builder
	fmt.Fprintln(&text, "Resolved catalog IDs:")
	for _, route := range plan.Routes {
		backup := strings.Join(markStandIns(route), " > ")
		if backup == "" {
			backup = "native policy (explicit override or no preference)"
		}
		fmt.Fprintf(&text, "%s: %s; backup: %s\n", route.Role, route.Primary, backup)
	}
	if len(plan.fallbackChains()) > 0 {
		fmt.Fprintln(&text, "Normal launches only verify these chains. Run codeforge models apply to choose scope, review changes, and confirm applying them.")
	}
	return text.String()
}
