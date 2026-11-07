package src

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/codeliter/codeforge/src/effort"
	"github.com/codeliter/codeforge/src/review"
)

var (
	errSecondOpinionIsReviewer = errors.New("review.second_opinion must differ from review.model")
	errReviewerIsCoder         = errors.New("review.model must differ from model, the model that writes the code")
	errLightIsCoder            = errors.New("review.light_model must differ from model, the model that writes the code")
	errSecondOpinionIsCoder    = errors.New("review.second_opinion must not include model, the model that writes the code")
	errDuplicateModels         = errors.New("model IDs must be unique within each role")
	errSensitivePath           = errors.New("review.sensitive_paths holds an unusable pattern")
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
	defaultModel       = "copilot/claude-sonnet-5.5"
	defaultExpertModel = "copilot/claude-opus-5.5"
	defaultReviewModel = "copilot/gpt-6.1-sol"
	defaultLightModel  = "copilot/deepseek-v4.1-flash"
)

var defaultSecondOpinion = []string{"copilot/kimi-k3"}

func configDir(home string) string {
	return filepath.Join(home, ".codeforge")
}

// loadConfig retains the global-only contract for setup and existing callers.
func loadConfig(home string) (Config, error) {
	return loadConfigAt(home, "")
}

// loadConfigAt applies project overrides before environment overrides and defaults.
func loadConfigAt(home, project string) (Config, error) {
	cfg, err := readConfigFile(home)
	if err != nil {
		return cfg, err
	}
	if project != "" && !sameConfigScope(home, project) {
		local, err := readTrustedProjectConfig(home, project)
		if err != nil {
			return cfg, err
		}
		cfg = mergeConfig(cfg, local)
	}
	cfg = configEnvironment(cfg)
	cfg = configDefaults(cfg)
	return cfg, validate(cfg)
}
func loadBaseConfig(home string) (Config, error) {
	cfg, err := readConfigFile(home)
	if err != nil {
		return cfg, err
	}
	cfg = configDefaults(cfg)
	return cfg, nil
}

func configEnvironment(cfg Config) Config {
	override(&cfg.Model, "CODEFORGE_MODEL")
	override(&cfg.Init, "CODEFORGE_INIT")
	override(&cfg.Expert, "CODEFORGE_EXPERT")
	override(&cfg.DefaultMode, "CODEFORGE_MODE")
	override(&cfg.Review.Model, "CODEFORGE_REVIEW_MODEL")
	override(&cfg.Review.Light, "CODEFORGE_REVIEW_LIGHT_MODEL")
	overrideList(&cfg.Judges, "CODEFORGE_JUDGES")
	overrideList(&cfg.Review.SecondOpinion, "CODEFORGE_REVIEW_SECOND_OPINION")
	return cfg
}

func configDefaults(cfg Config) Config {
	choice := func(id string) string {
		if cfg.Providers != nil && cfg.Providers.Primary != "" {
			return modelName(id)
		}
		return id
	}
	cfg.Model = cmp.Or(cfg.Model, choice(defaultModel))
	cfg.Init = cmp.Or(cfg.Init, cfg.Model)
	cfg.Expert = cmp.Or(cfg.Expert, choice(defaultExpertModel))
	cfg.Review.Model = cmp.Or(cfg.Review.Model, choice(defaultReviewModel))
	cfg.Review.Light = cmp.Or(cfg.Review.Light, choice(defaultLightModel))
	if len(cfg.Judges) == 0 {
		cfg.Judges = nil
		for _, id := range effort.DefaultJudges {
			cfg.Judges = append(cfg.Judges, choice(id))
		}
	}
	if len(cfg.Review.SecondOpinion) == 0 {
		cfg.Review.SecondOpinion = nil
		for _, id := range defaultSecondOpinion {
			cfg.Review.SecondOpinion = append(cfg.Review.SecondOpinion, choice(id))
		}
	}
	return cfg
}

func validate(cfg Config) error {
	if err := validateProviderPolicy(cfg.Providers); err != nil {
		return err
	}
	if err := validMode(cfg.DefaultMode); err != nil {
		return err
	}
	if cfg.Model != "" && cfg.Review.Model == cfg.Model {
		return fmt.Errorf("%w (%s)", errReviewerIsCoder, cfg.Model)
	}
	if cfg.Model != "" && cfg.Review.Light == cfg.Model {
		return fmt.Errorf("%w (%s)", errLightIsCoder, cfg.Model)
	}
	for _, p := range cfg.Review.SensitivePaths {
		if why := review.ValidSensitivePattern(p); why != "" {
			return fmt.Errorf("%w: %q %s", errSensitivePath, p, why)
		}
	}
	for _, m := range cfg.Review.SecondOpinion {
		if m == cfg.Review.Model {
			return fmt.Errorf("%w (%s)", errSecondOpinionIsReviewer, m)
		}
		if m == cfg.Model {
			return fmt.Errorf("%w (%s)", errSecondOpinionIsCoder, m)
		}
	}
	for _, role := range roles {
		seen := make(map[string]bool)
		for _, model := range current(cfg, role.key) {
			if seen[model] {
				return fmt.Errorf("%w: %s", errDuplicateModels, role.key)
			}
			seen[model] = true
			if !validModelReference(model) {
				return fmt.Errorf("%w for %s: %q", errNotProviderModel, role.key, model)
			}
		}
	}
	return validateRouting(cfg.Routing)
}

func readConfigFile(home string) (Config, error) {
	return readConfigDirectory(configDir(home), true)
}

func writeConfigFile(home string, cfg Config) error {
	return writeConfigYAML(configDir(home), cfg)
}

func override(dst *string, env string) {
	if v := os.Getenv(env); v != "" {
		*dst = v
	}
}

func overrideList(dst *[]string, env string) {
	if v := os.Getenv(env); v != "" {
		*dst = splitList(v)
	}
}

func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
