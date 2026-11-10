package config

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"maps"
	"slices"

	"github.com/codeliter/codeforge/src/effort"
	"github.com/codeliter/codeforge/src/review"
)

var (
	ErrSecondOpinionIsReviewer = errors.New("review.second_opinion must differ from review.model")
	ErrReviewerIsCoder         = errors.New("review.model must differ from model, the model that writes the code")
	ErrLightIsCoder            = errors.New("review.light_model must differ from model, the model that writes the code")
	ErrSecondOpinionIsCoder    = errors.New("review.second_opinion must not include model, the model that writes the code")
	ErrDuplicateModels         = errors.New("model IDs must be unique within each role")
	ErrSensitivePath           = errors.New("review.sensitive_paths holds an unusable pattern")
)

type Config struct {
	Model       string            `json:"model,omitempty" yaml:"model,omitempty"`
	Expert      string            `json:"expert,omitempty" yaml:"expert,omitempty"`
	Judges      []string          `json:"judges,omitempty" yaml:"judges,omitempty"`
	Review      ReviewConfig      `json:"review,omitzero" yaml:"review,omitempty"`
	Init        string            `json:"init,omitempty" yaml:"init,omitempty"`
	DefaultMode string            `json:"default_mode,omitempty" yaml:"default_mode,omitempty"` //nolint:tagliatelle // Preserve the established configuration keys.
	Routing     map[string]string `json:"routing,omitempty" yaml:"routing,omitempty"`
	Providers   *ProviderPolicy   `json:"providers,omitempty" yaml:"providers,omitempty"`
}

type ReviewConfig struct {
	Model         string   `json:"model,omitempty" yaml:"model,omitempty"`
	SecondOpinion []string `json:"second_opinion,omitempty" yaml:"second_opinion,omitempty"` //nolint:tagliatelle // Preserve the established configuration keys.
	// Light reviews docs-only changes; code changes always use Model. Empty means the normal reviewer.
	Light string `json:"light_model,omitempty" yaml:"light_model,omitempty"` //nolint:tagliatelle // Preserve the established configuration keys.
	// SensitivePaths are paths the repository's owners always want reviewed at the risky tier, whatever the judges say:
	// "auth/" (any directory of that name), "src/guard/*.go" (a path) or "*.sql" (a file name). They only add scrutiny.
	SensitivePaths []string `json:"sensitive_paths,omitempty" yaml:"sensitive_paths,omitempty"` //nolint:tagliatelle // Preserve the established configuration keys.
}

const (
	DefaultModel       = "copilot/claude-opus-5.5"
	DefaultExpertModel = "copilot/claude-fable-5.2"
	DefaultReviewModel = "copilot/gpt-6.1-sol"
	DefaultLightModel  = "copilot/deepseek-v4.1-flash"
	// DefaultInitModel writes the persona; drafting needs no more than Sonnet.
	DefaultInitModel = "copilot/claude-sonnet-5.5"
)

var DefaultSecondOpinion = []string{"copilot/kimi-k3"}

func Dir(home string) string {
	return filepath.Join(home, ".codeforge")
}

// LoadGlobal retains the global-only contract for setup and existing callers.
func LoadGlobal(home string) (Config, error) {
	return Load(home, "", nil)
}

// Load applies project overrides before environment overrides and defaults.
func Load(home, project string, ask Approver) (Config, error) {
	cfg, err := ReadFile(home)
	if err != nil {
		return cfg, err
	}
	if project != "" && !SameScope(home, project) {
		local, err := ReadTrustedProject(home, project, ask)
		if err != nil {
			return cfg, err
		}
		cfg = Merge(cfg, local)
	}
	cfg = Environment(cfg)
	cfg = Defaults(cfg)
	return cfg, Validate(cfg)
}
func LoadBase(home string) (Config, error) {
	cfg, err := ReadFile(home)
	if err != nil {
		return cfg, err
	}
	cfg = Defaults(cfg)
	return cfg, nil
}

func Environment(cfg Config) Config {
	OverrideEnv(&cfg.Model, "CODEFORGE_MODEL")
	OverrideEnv(&cfg.Init, "CODEFORGE_INIT")
	OverrideEnv(&cfg.Expert, "CODEFORGE_EXPERT")
	OverrideEnv(&cfg.DefaultMode, "CODEFORGE_MODE")
	OverrideEnv(&cfg.Review.Model, "CODEFORGE_REVIEW_MODEL")
	OverrideEnv(&cfg.Review.Light, "CODEFORGE_REVIEW_LIGHT_MODEL")
	overrideList(&cfg.Judges, "CODEFORGE_JUDGES")
	overrideList(&cfg.Review.SecondOpinion, "CODEFORGE_REVIEW_SECOND_OPINION")
	return cfg
}

func Defaults(cfg Config) Config {
	choice := func(id string) string {
		if cfg.Providers != nil && cfg.Providers.Primary != "" {
			return ModelName(id)
		}
		return id
	}
	if cfg.Init == "" && cfg.Model == "" {
		cfg.Init = choice(DefaultInitModel)
	}
	cfg.Model = cmp.Or(cfg.Model, choice(DefaultModel))
	cfg.Init = cmp.Or(cfg.Init, cfg.Model)
	cfg.Expert = cmp.Or(cfg.Expert, choice(DefaultExpertModel))
	cfg.Review.Model = cmp.Or(cfg.Review.Model, choice(DefaultReviewModel))
	cfg.Review.Light = cmp.Or(cfg.Review.Light, choice(DefaultLightModel))
	if len(cfg.Judges) == 0 {
		cfg.Judges = nil
		for _, id := range effort.DefaultJudges {
			cfg.Judges = append(cfg.Judges, choice(id))
		}
	}
	if len(cfg.Review.SecondOpinion) == 0 {
		cfg.Review.SecondOpinion = nil
		for _, id := range DefaultSecondOpinion {
			cfg.Review.SecondOpinion = append(cfg.Review.SecondOpinion, choice(id))
		}
	}
	return cfg
}

func Validate(cfg Config) error {
	if err := ValidateProviderPolicy(cfg.Providers); err != nil {
		return err
	}
	if err := ValidMode(cfg.DefaultMode); err != nil {
		return err
	}
	if cfg.Model != "" && cfg.Review.Model == cfg.Model {
		return fmt.Errorf("%w (%s)", ErrReviewerIsCoder, cfg.Model)
	}
	if cfg.Model != "" && cfg.Review.Light == cfg.Model {
		return fmt.Errorf("%w (%s)", ErrLightIsCoder, cfg.Model)
	}
	for _, p := range cfg.Review.SensitivePaths {
		if why := review.ValidSensitivePattern(p); why != "" {
			return fmt.Errorf("%w: %q %s", ErrSensitivePath, p, why)
		}
	}
	for _, m := range cfg.Review.SecondOpinion {
		if m == cfg.Review.Model {
			return fmt.Errorf("%w (%s)", ErrSecondOpinionIsReviewer, m)
		}
		if m == cfg.Model {
			return fmt.Errorf("%w (%s)", ErrSecondOpinionIsCoder, m)
		}
	}
	for _, role := range Roles {
		seen := make(map[string]bool)
		for _, model := range Current(cfg, role.Key) {
			if seen[model] {
				return fmt.Errorf("%w: %s", ErrDuplicateModels, role.Key)
			}
			seen[model] = true
			if !ValidModelReference(model) {
				return fmt.Errorf("%w for %s: %q", ErrNotProviderModel, role.Key, model)
			}
		}
	}
	return validateRouting(cfg.Routing)
}

func ReadFile(home string) (Config, error) {
	return readConfigDirectory(Dir(home), true)
}

func WriteFile(home string, cfg Config) error {
	return WriteYAML(Dir(home), cfg)
}

func OverrideEnv(dst *string, env string) {
	if v := os.Getenv(env); v != "" {
		*dst = v
	}
}

func overrideList(dst *[]string, env string) {
	if v := os.Getenv(env); v != "" {
		*dst = SplitList(v)
	}
}

func SplitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Clone copies cfg deeply, so that editing the copy leaves cfg alone.
func Clone(cfg Config) Config {
	cfg.Providers = cfg.Providers.Clone()
	cfg.Judges = slices.Clone(cfg.Judges)
	cfg.Review.SecondOpinion = slices.Clone(cfg.Review.SecondOpinion)
	cfg.Review.SensitivePaths = slices.Clone(cfg.Review.SensitivePaths)
	cfg.Routing = maps.Clone(cfg.Routing)
	return cfg
}
