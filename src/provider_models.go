package src

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
)

var errModelName = errors.New("model name cannot be resolved from the connected-provider catalog")
var errAmbiguousModel = errors.New("model name is ambiguous; choose a primary provider or an explicit provider/model ID")
var errProviderPolicy = errors.New("invalid provider policy")
var errNativeFallback = errors.New("native fallback policy does not match Codeforge; native settings were not changed")

type ProviderPolicy struct {
	Primary  string `json:"primary,omitempty" yaml:"primary,omitempty"`
	Fallback string `json:"fallback,omitempty" yaml:"fallback,omitempty"`
}

type ModelRoute struct {
	Role     string
	Name     string
	Primary  string
	Fallback string
	Explicit bool
}

type ModelPlan struct {
	Config Config
	Routes []ModelRoute
}

func providerID(id string) string {
	switch strings.ToLower(id) {
	case "devpass", "llm-gateway", "llmgateway":
		return "llmgateway"
	case "copilot":
		return "copilot"
	default:
		return id
	}
}

func validProviderID(id string) bool {
	return id != "" && !strings.HasPrefix(id, "-") && !strings.Contains(id, "/") && validModelID(id+"/probe")
}

func validateProviderPolicy(policy *ProviderPolicy) error {
	if policy == nil {
		return nil
	}
	primary, backup := providerID(policy.Primary), providerID(policy.Fallback)
	if primary == "" && backup != "" {
		return fmt.Errorf("%w: fallback requires primary", errProviderPolicy)
	}
	if primary != "" && !validProviderID(primary) || backup != "" && !validProviderID(backup) {
		return errProviderPolicy
	}
	if backup != "" && primary == backup {
		return fmt.Errorf("%w: primary and fallback must differ", errProviderPolicy)
	}
	return nil
}

func validModelReference(ref string) bool {
	if validModelID(ref) {
		return true
	}
	if ref == "" || strings.TrimSpace(ref) == "" || strings.Contains(ref, "/") {
		return false
	}
	for _, c := range ref {
		allowed := unicode.IsLetter(c) || unicode.IsDigit(c) || c == ' ' || c == '-' || c == '_' || c == '.' || c == ':'
		if !allowed {
			return false
		}
	}
	return true
}

func modelReferenceKey(ref string) string {
	key := modelKey(strings.Join(strings.Fields(ref), "-"))
	for _, family := range []string{"sonnet", "opus", "haiku"} {
		if strings.HasPrefix(key, family+"-") {
			return "claude-" + key
		}
	}
	return key
}

func catalogModel(ref, provider string, catalog []string) (string, error) {
	provider = providerID(provider)
	var exact, matches []string
	for _, id := range catalog {
		p, name, ok := strings.Cut(id, "/")
		if !ok || provider != "" && p != provider {
			continue
		}
		if name == ref {
			exact = append(exact, id)
		}
		if modelReferenceKey(modelName(id)) == modelReferenceKey(modelName(ref)) {
			matches = append(matches, id)
		}
	}
	if len(exact) > 0 {
		matches = exact
	}
	sort.Strings(matches)
	matches = slices.Compact(matches)
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("%w: %q via %s", errAmbiguousModel, ref, strings.Join(matches, ", "))
	}
	return "", fmt.Errorf("%w: %q on provider %q", errModelName, ref, provider)
}

func resolveModelReference(role, ref string, policy *ProviderPolicy, catalog []string) (ModelRoute, error) {
	route := ModelRoute{Role: role, Name: ref}
	if validModelID(ref) {
		// Fully qualified IDs are deliberate advanced overrides. Preserve existing
		// unavailable IDs and their native fallback behaviour, as before.
		route.Primary, route.Explicit = ref, true
		return route, nil
	}
	primary, backup := "", ""
	if policy != nil {
		primary, backup = providerID(policy.Primary), providerID(policy.Fallback)
	}
	var err error
	route.Primary, err = catalogModel(ref, primary, catalog)
	if err != nil {
		return route, fmt.Errorf("%s: %w", role, err)
	}
	if backup != "" {
		route.Fallback, err = catalogModel(ref, backup, catalog)
		if err != nil {
			return route, fmt.Errorf("%s fallback: %w", role, err)
		}
	}
	return route, nil
}

func resolveModelPlan(cfg Config, catalog []string) (ModelPlan, error) {
	plan := ModelPlan{Config: cloneConfig(cfg)}
	if err := validate(cfg); err != nil {
		return plan, err
	}
	for _, role := range roles {
		refs := current(cfg, role.key)
		var ids []string
		for _, ref := range refs {
			route, err := resolveModelReference(role.key, ref, cfg.Providers, catalog)
			if err != nil {
				return plan, err
			}
			ids = append(ids, route.Primary)
			plan.Routes = append(plan.Routes, route)
		}
		assignModelRole(&plan.Config, role.key, ids)
	}
	for job, ref := range cfg.Routing {
		if ref == "" {
			continue
		}
		route, err := resolveModelReference("routing."+job, ref, cfg.Providers, catalog)
		if err != nil {
			return plan, err
		}
		plan.Config.Routing[job] = route.Primary
		plan.Routes = append(plan.Routes, route)
	}
	return plan, validate(plan.Config)
}

func needsModelCatalog(cfg Config) bool {
	for _, role := range roles {
		for _, ref := range current(cfg, role.key) {
			if !validModelID(ref) {
				return true
			}
		}
	}
	for _, ref := range cfg.Routing {
		if ref != "" && !validModelID(ref) {
			return true
		}
	}
	return false
}

func (plan ModelPlan) fallbackChains() map[string][]string {
	chains := make(map[string][]string)
	for _, route := range plan.Routes {
		if route.Fallback != "" {
			chains[route.Primary] = []string{route.Fallback}
		}
	}
	return chains
}

func (plan ModelPlan) verifyFallbacks(native map[string][]string) error {
	chains := plan.fallbackChains()
	keys := make([]string, 0, len(chains))
	for key := range chains {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var failures []string
	for _, primary := range keys {
		expected := chains[primary]
		if !slices.Equal(native[primary], expected) {
			failures = append(failures, fmt.Sprintf("%s needs exactly %v, found %v", primary, expected, native[primary]))
		}
		for _, further := range native[expected[0]] {
			if further != primary {
				failures = append(failures, fmt.Sprintf("backup %s also routes to undeclared model %s", expected[0], further))
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%w:\n%s\nconfigure these chains through Empryo /router or its settings tool, then retry", errNativeFallback, strings.Join(failures, "\n"))
	}
	return nil
}
