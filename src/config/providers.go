package config

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"unicode"
)

var ErrModelName = errors.New("model name cannot be resolved from the connected-provider catalog")
var ErrAmbiguousModel = errors.New("model name is ambiguous; choose a primary provider or an explicit provider/model ID")
var ErrProviderPolicy = errors.New("invalid provider policy")
var ErrNativeFallback = errors.New("native fallback policy does not match Codeforge; native settings were not changed")

// ProviderPolicy is the primary provider and the ordered backups Empryo switches to when it fails or runs out of quota.
// Fallback is the original single-backup key; Fallbacks adds more. Backups() returns them in order, Fallback first.
type ProviderPolicy struct {
	Primary   string         `json:"primary,omitempty" yaml:"primary,omitempty"`
	Fallback  string         `json:"fallback,omitempty" yaml:"fallback,omitempty"`
	Fallbacks []string       `json:"fallbacks,omitempty" yaml:"fallbacks,omitempty"`
	Rules     []ProviderRule `json:"rules,omitempty" yaml:"rules,omitempty"`
}

// ProviderRule gives the models matching Match their own provider chain instead of the default one,
// for example claude-* on anthropic with openrouter as its backup. Match is a glob over the model name
// written like a model reference (Sonnet-* means claude-sonnet-*). The first matching rule wins.
type ProviderRule struct {
	Match     string   `json:"match" yaml:"match"`
	Primary   string   `json:"primary" yaml:"primary"`
	Fallbacks []string `json:"fallbacks,omitempty" yaml:"fallbacks,omitempty"`
}

// RuleKey normalises a pattern or model reference the same way, so Sonnet-* and Sonnet* both mean claude-sonnet-*.
func RuleKey(match string) string {
	key := ModelReferenceKey(match)
	for _, family := range []string{"sonnet", "opus", "haiku"} {
		if strings.HasPrefix(key, family) {
			return "claude-" + key
		}
	}
	return key
}

// ChainFor is the chain that serves a model: its rule's chain, else the default one (nil when there is none).
func (policy *ProviderPolicy) ChainFor(ref string) *ProviderPolicy {
	if rule, ok := policy.RuleFor(ref); ok {
		return rule
	}
	return policy
}

// PrimaryFor is the provider a model runs on first, or "" when no chain says.
func (policy *ProviderPolicy) PrimaryFor(ref string) string {
	if chain := policy.ChainFor(ref); chain != nil {
		return ProviderID(chain.Primary)
	}
	return ""
}

// Chain is the rule's providers as an ordinary policy, so it is validated and resolved like the default one.
func (rule ProviderRule) Chain() *ProviderPolicy {
	return NewProviderPolicy(rule.Primary, rule.Fallbacks)
}

// RuleFor returns the chain of the first rule matching the model reference.
func (policy *ProviderPolicy) RuleFor(ref string) (*ProviderPolicy, bool) {
	if policy == nil {
		return nil, false
	}
	key := RuleKey(ref)
	for _, rule := range policy.Rules {
		if ok, err := path.Match(RuleKey(rule.Match), key); err == nil && ok {
			return rule.Chain(), true
		}
	}
	return nil, false
}

// Backups is the ordered list of backup providers, with aliases normalised.
func (policy *ProviderPolicy) Backups() []string {
	if policy == nil {
		return nil
	}
	var out []string
	for _, id := range append([]string{policy.Fallback}, policy.Fallbacks...) {
		if id = ProviderID(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func (policy *ProviderPolicy) Clone() *ProviderPolicy {
	if policy == nil {
		return nil
	}
	out := *policy
	out.Fallbacks = slices.Clone(policy.Fallbacks)
	out.Rules = slices.Clone(policy.Rules)
	for i := range out.Rules {
		out.Rules[i].Fallbacks = slices.Clone(out.Rules[i].Fallbacks)
	}
	return &out
}

// NewProviderPolicy stores an ordered chain in the list form.
func NewProviderPolicy(primary string, backups []string) *ProviderPolicy {
	return &ProviderPolicy{Primary: ProviderID(primary), Fallbacks: slices.Clone(backups)}
}

type ModelRoute struct {
	Role      string
	Name      string
	Primary   string
	Fallbacks []string
	// StandIns marks backups that serve a comparable model because the provider lacks the exact one.
	StandIns map[string]bool
	Explicit bool
}

type ModelPlan struct {
	Config Config
	Routes []ModelRoute
}

func ProviderID(id string) string {
	switch strings.ToLower(id) {
	case "devpass", "llm-gateway", "llmgateway":
		return "llmgateway"
	case "copilot":
		return "copilot"
	default:
		return id
	}
}

func ValidProviderID(id string) bool {
	return id != "" && !strings.HasPrefix(id, "-") && !strings.Contains(id, "/") && ValidModelID(id+"/probe")
}

func ValidateProviderPolicy(policy *ProviderPolicy) error {
	if policy == nil {
		return nil
	}
	primary, backups := ProviderID(policy.Primary), policy.Backups()
	if primary == "" && len(backups) > 0 {
		return fmt.Errorf("%w: fallback requires primary", ErrProviderPolicy)
	}
	if primary != "" && !ValidProviderID(primary) {
		return ErrProviderPolicy
	}
	seen := map[string]bool{primary: true}
	for _, backup := range backups {
		if !ValidProviderID(backup) {
			return ErrProviderPolicy
		}
		if seen[backup] {
			return fmt.Errorf("%w: %s is listed twice; primary and fallbacks must all differ", ErrProviderPolicy, backup)
		}
		seen[backup] = true
	}
	matches := map[string]bool{}
	for _, rule := range policy.Rules {
		key := RuleKey(rule.Match)
		if key == "" || strings.TrimSpace(rule.Match) == "" {
			return fmt.Errorf("%w: a rule needs a model pattern such as claude-*", ErrProviderPolicy)
		}
		// Only * and ? are wildcards, so path.Match can never reject the pattern.
		if strings.ContainsAny(rule.Match, "[]\\/") {
			return fmt.Errorf("%w: rule pattern %q must be a model name using only * and ? as wildcards (no provider prefix, brackets or backslash)", ErrProviderPolicy, rule.Match)
		}
		if matches[key] {
			return fmt.Errorf("%w: rule %q is listed twice", ErrProviderPolicy, rule.Match)
		}
		matches[key] = true
		if ProviderID(rule.Primary) == "" {
			return fmt.Errorf("%w: rule %q needs a primary provider", ErrProviderPolicy, rule.Match)
		}
		if err := ValidateProviderPolicy(rule.Chain()); err != nil {
			return fmt.Errorf("rule %q: %w", rule.Match, err)
		}
	}
	return nil
}

func ValidModelReference(ref string) bool {
	if ValidModelID(ref) {
		return true
	}
	// A name starting with a dash is a flag that strayed into a list of models, never a model.
	if ref == "" || strings.TrimSpace(ref) == "" || strings.Contains(ref, "/") || strings.HasPrefix(ref, "-") {
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

func ModelReferenceKey(ref string) string {
	key := ModelKey(strings.Join(strings.Fields(ref), "-"))
	for _, family := range []string{"sonnet", "opus", "haiku"} {
		if strings.HasPrefix(key, family+"-") {
			return "claude-" + key
		}
	}
	return key
}

func CatalogModel(ref, provider string, catalog []string) (string, error) {
	provider = ProviderID(provider)
	var exact, matches []string
	for _, id := range catalog {
		p, name, ok := strings.Cut(id, "/")
		if !ok || provider != "" && p != provider {
			continue
		}
		if name == ref {
			exact = append(exact, id)
		}
		if ModelReferenceKey(ModelName(id)) == ModelReferenceKey(ModelName(ref)) {
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
		return "", fmt.Errorf("%w: %q via %s", ErrAmbiguousModel, ref, strings.Join(matches, ", "))
	}
	return "", fmt.Errorf("%w: %q on provider %q", ErrModelName, ref, provider)
}

// builtInDefault reports whether ref names one of Codeforge's own default models.
func builtInDefault(ref string) bool {
	key := ModelReferenceKey(ref)
	for _, id := range slices.Concat([]string{DefaultModel, DefaultExpertModel, DefaultReviewModel, DefaultLightModel, DefaultInitModel}, DefaultSecondOpinion) {
		if ModelReferenceKey(ModelName(id)) == key {
			return true
		}
	}
	return false
}

func ResolveModelReference(role, ref string, policy *ProviderPolicy, catalog []string) (ModelRoute, error) {
	route := ModelRoute{Role: role, Name: ref}
	if ValidModelID(ref) {
		// Fully qualified IDs are deliberate advanced overrides. Preserve existing
		// unavailable IDs and their native fallback behaviour, as before.
		route.Primary, route.Explicit = ref, true
		return route, nil
	}
	// A rule gives this model family its own chain; the default chain serves everything else.
	rule, ruled := policy.RuleFor(ref)
	if ruled {
		policy = rule
	}
	primary := ""
	if policy != nil {
		primary = ProviderID(policy.Primary)
	}
	var err error
	route.Primary, err = CatalogModel(ref, primary, catalog)
	if errors.Is(err, ErrModelName) && builtInDefault(ref) {
		// A default the user never chose must not break a provider that lacks it; its closest model stands in.
		if standIn, ok := EquivalentModel(ref, primary, catalog); ok {
			route.Primary, err = standIn, nil
		}
	}
	if err != nil {
		return route, fmt.Errorf("%s: %w", role, err)
	}
	// A backup serves the same model when it has it. When it doesn't, use its closest model of the same
	// line or level, so a missing model never removes the backup; only a provider with nothing comparable fails.
	// Under a rule, a stand-in stays in the model's own family: a Claude rule never falls back to a GPT model.
	standIns := catalog
	if ruled {
		fam := Family(route.Primary)
		standIns = slices.DeleteFunc(slices.Clone(catalog), func(id string) bool { return Family(id) != fam })
	}
	for _, backup := range policy.Backups() {
		id, err := CatalogModel(ref, backup, catalog)
		if errors.Is(err, ErrModelName) {
			if standIn, ok := EquivalentModel(route.Primary, backup, standIns); ok {
				id, err = standIn, nil
				if route.StandIns == nil {
					route.StandIns = map[string]bool{}
				}
				route.StandIns[id] = true
			}
		}
		if err != nil {
			return route, fmt.Errorf("%s fallback: %w (and %s has no model of the same level)", role, err, backup)
		}
		route.Fallbacks = append(route.Fallbacks, id)
	}
	return route, nil
}

func ResolvePlan(cfg Config, catalog []string) (ModelPlan, error) {
	plan := ModelPlan{Config: Clone(cfg)}
	if err := Validate(cfg); err != nil {
		return plan, err
	}
	for _, role := range Roles {
		refs := Current(cfg, role.Key)
		var ids []string
		for _, ref := range refs {
			route, err := ResolveModelReference(role.Key, ref, cfg.Providers, catalog)
			if err != nil {
				return plan, err
			}
			ids = append(ids, route.Primary)
			plan.Routes = append(plan.Routes, route)
		}
		AssignRole(&plan.Config, role.Key, ids)
	}
	// The light reviewer is no role the user picks, but a bare name needs its provider like any other: one that cannot
	// be found is dropped, so docs are reviewed by the normal reviewer rather than failing.
	if ref := cfg.Review.Light; ref != "" && !ValidModelID(ref) {
		plan.Config.Review.Light = ""
		if route, err := ResolveModelReference("review.light", ref, cfg.Providers, catalog); err == nil {
			plan.Config.Review.Light = route.Primary
			plan.Routes = append(plan.Routes, route)
		}
	}
	for job, ref := range cfg.Routing {
		if ref == "" {
			continue
		}
		route, err := ResolveModelReference("routing."+job, ref, cfg.Providers, catalog)
		if err != nil {
			return plan, err
		}
		plan.Config.Routing[job] = route.Primary
		plan.Routes = append(plan.Routes, route)
	}
	return plan, Validate(plan.Config)
}

func NeedsCatalog(cfg Config) bool {
	for _, role := range Roles {
		for _, ref := range Current(cfg, role.Key) {
			if !ValidModelID(ref) {
				return true
			}
		}
	}
	for _, ref := range cfg.Routing {
		if ref != "" && !ValidModelID(ref) {
			return true
		}
	}
	return cfg.Review.Light != "" && !ValidModelID(cfg.Review.Light)
}

func (plan ModelPlan) FallbackChains() map[string][]string {
	chains := make(map[string][]string)
	for _, route := range plan.Routes {
		if len(route.Fallbacks) > 0 {
			chains[route.Primary] = slices.Clone(route.Fallbacks)
		}
	}
	return chains
}

func (plan ModelPlan) VerifyFallbacks(native map[string][]string) error {
	chains := plan.FallbackChains()
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
		allowed := append([]string{primary}, expected...)
		for _, backup := range expected {
			for _, further := range native[backup] {
				// A backup that is itself a declared primary (another family's rule) keeps its own declared chain.
				if !slices.Contains(allowed, further) && !slices.Contains(chains[backup], further) {
					failures = append(failures, fmt.Sprintf("backup %s also routes to undeclared model %s", backup, further))
				}
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%w:\n%s\nrun codeforge models apply to review and confirm changes, or configure these chains through Empryo /router", ErrNativeFallback, strings.Join(failures, "\n"))
	}
	return nil
}
