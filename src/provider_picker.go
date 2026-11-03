package src

import (
	"fmt"
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
	if cfg.Providers == nil || cfg.Providers.Primary == "" {
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
				source.Providers.Fallback = ""
			}
			plan, err := resolveModelPlan(source, catalog)
			if err != nil {
				return err
			}
			*cfg = plan.Config
		}
		cfg.Providers = nil
		return nil
	}
	ids := catalogProviders(catalog)
	if len(ids) == 0 {
		return errNoModels
	}
	primary, backup := ids[0], ""
	if slices.Contains(ids, "llmgateway") {
		primary = "llmgateway"
	} else if slices.Contains(ids, "copilot") {
		primary = "copilot"
	}
	if cfg.Providers != nil {
		if slices.Contains(ids, providerID(cfg.Providers.Primary)) {
			primary = providerID(cfg.Providers.Primary)
		}
		backup = providerID(cfg.Providers.Fallback)
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
	if err := runForm("fallback provider", huh.NewSelect[string]().Title("Fallback provider").Description("Native settings stay untouched. A selected backup must already be configured in Empryo before a task starts.").Options(fallbacks...).Value(&backup)); err != nil {
		return err
	}
	useProviderDefaults(cfg, &ProviderPolicy{Primary: primary, Fallback: backup}, catalog)
	return nil
}
func useProviderDefaults(cfg *Config, policy *ProviderPolicy, catalog []string) {
	selected := *policy
	cfg.Providers = &selected
	primary := providerID(selected.Primary)
	for _, role := range roles {
		refs := slices.Clone(current(*cfg, role.key))
		for i, ref := range refs {
			if validModelID(ref) {
				if id, err := catalogModel(modelName(ref), primary, catalog); err == nil {
					refs[i] = modelName(id)
				}
			}
		}
		assignModelRole(cfg, role.key, refs)
	}
	for job, ref := range cfg.Routing {
		if validModelID(ref) {
			if id, err := catalogModel(modelName(ref), primary, catalog); err == nil {
				cfg.Routing[job] = modelName(id)
			}
		}
	}
}

func providerModelOptions(catalog []string, policy *ProviderPolicy, saved ...string) []huh.Option[string] {
	if policy == nil || policy.Primary == "" {
		return modelOptions(catalog, saved...)
	}
	primary := providerID(policy.Primary)
	var options []huh.Option[string]
	seen := make(map[string]bool)
	for _, id := range catalog {
		p, _, _ := strings.Cut(id, "/")
		if p != primary || !suggestedModel(id) {
			continue
		}
		value := modelName(id)
		label := value
		if _, err := catalogModel(value, primary, catalog); err != nil {
			value = id
			label = id + " (explicit override; native fallback)"
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		if value != id && policy.Fallback != "" {
			if backup, err := catalogModel(value, policy.Fallback, catalog); err == nil {
				label += " (backup: " + backup + ")"
			} else {
				label += " (no matching backup)"
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
		backup := route.Fallback
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
