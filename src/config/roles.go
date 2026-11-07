package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrNotProviderModel = errors.New("model is not provider/model")
var ErrOneModel = errors.New("role takes exactly one model")
var ErrNoModels = errors.New("no models given")
var ErrUnknownRole = errors.New("unknown role")

// Role describes one model slot. Candidates are model names without the provider, best first,
// so the same list works whether the user reaches a model through copilot, anthropic or openai.
type Role struct {
	Key        string
	Count      int
	Why        string
	Candidates []string
}

var Roles = []Role{
	{"model", 1, "writes the code; strongest coding model you have", []string{"claude-sonnet-5.5", "claude-opus-5.5", "gpt-6.1-sol", "claude-opus-5", "gpt-6-sol"}},
	{"expert", 1, "on-demand delegate for difficult implementation, not routine work", []string{"claude-opus-5.5", "claude-opus-5", "gpt-6.1-sol"}},
	{"judges", 2, "score effort before launch; cheapest fast models, two families", []string{"claude-haiku-5.5", "gpt-6-luna", "gemini-3.8-flash", "gpt-5.6-luna", "mai-code-1.1-flash", "gpt-5.4-nano"}},
	{"review.model", 1, "writes the review; strongest model from a different family than the coder", []string{"gpt-6.1-sol", "claude-opus-5.5", "gpt-5.5", "claude-opus-5", "gemini-3.8-flash"}},
	{"review.second_opinion", 2, "independent reviewers; frontier models from other families", []string{"kimi-k3", "gpt-6.1-sol", "gpt-5.5", "claude-opus-5.5"}},
	{"init", 1, "distils your persona once; capable everyday writer", []string{"claude-sonnet-5.5", "claude-opus-5.5", "gpt-6.1-sol"}},
}

func Family(model string) string {
	name := ModelReferenceKey(ModelName(model))
	if i := strings.IndexAny(name, "-."); i > 0 {
		return name[:i]
	}
	return name
}

func ModelName(model string) string {
	return model[strings.LastIndex(model, "/")+1:]
}
func ModelKey(name string) string {
	return strings.ToLower(strings.NewReplacer(".", "-", "_", "-").Replace(name))
}

func Current(cfg Config, key string) []string {
	switch key {
	case "expert":
		return nonEmpty(cfg.Expert)
	case "model":
		return nonEmpty(cfg.Model)
	case "judges":
		return cfg.Judges
	case "review.model":
		return nonEmpty(cfg.Review.Model)
	case "review.second_opinion":
		return cfg.Review.SecondOpinion
	case "init":
		return nonEmpty(cfg.Init)
	}
	return nil
}

func Apply(cfg *Config, key string, models []string) error {
	r, ok := FindRole(key)
	if !ok {
		return fmt.Errorf("%w %q; roles: %s", ErrUnknownRole, key, RoleKeys())
	}
	if len(models) == 0 {
		return fmt.Errorf("%w: %s", ErrNoModels, key)
	}
	if r.Count == 1 && len(models) != 1 {
		return fmt.Errorf("%w: %s", ErrOneModel, key)
	}
	for _, model := range models {
		if !ValidModelReference(model) {
			return fmt.Errorf("%w: %q", ErrNotProviderModel, model)
		}
	}
	AssignRole(cfg, key, models)
	return Validate(*cfg)
}
func AssignRole(cfg *Config, key string, models []string) {
	value := ""
	if len(models) > 0 {
		value = models[0]
	}
	switch key {
	case "expert":
		cfg.Expert = value
	case "model":
		cfg.Model = value
	case "judges":
		cfg.Judges = models
	case "review.model":
		cfg.Review.Model = value
	case "review.second_opinion":
		cfg.Review.SecondOpinion = models
	case "init":
		cfg.Init = value
	}
}

func FindRole(key string) (Role, bool) {
	i := slices.IndexFunc(Roles, func(r Role) bool { return r.Key == key })
	if i < 0 {
		return Role{}, false
	}
	return Roles[i], true
}

func RoleKeys() string {
	keys := make([]string, len(Roles))
	for i, r := range Roles {
		keys[i] = r.Key
	}
	return strings.Join(keys, ", ")
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
