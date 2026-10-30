package src

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/codeliter/codeforge/src/effort"
)

var (
	errSecondOpinionIsReviewer = errors.New("review.second_opinion must differ from review.model")
	errReviewerIsCoder         = errors.New("review.model must differ from model, the model that writes the code")
	errSecondOpinionIsCoder    = errors.New("review.second_opinion must not include model, the model that writes the code")
)

type Config struct {
	Model       string       `json:"model,omitempty"`
	Judges      []string     `json:"judges,omitempty"`
	Review      ReviewConfig `json:"review,omitzero"`
	Init        string       `json:"init,omitempty"`
	DefaultMode string       `json:"default_mode,omitempty"`
}

type ReviewConfig struct {
	Model         string   `json:"model,omitempty"`
	SecondOpinion []string `json:"second_opinion,omitempty"`
}

const (
	defaultModel       = "copilot/claude-opus-5.5"
	defaultReviewModel = "copilot/gpt-6.1-sol"
)

var defaultSecondOpinion = []string{"copilot/kimi-k3"}

func configDir(home string) string {
	return filepath.Join(home, ".codeforge")
}

// loadConfig merges defaults, ~/.codeforge/config.json and CODEFORGE_* overrides, then validates the reviewers.
func loadConfig(home string) (Config, error) {
	cfg, err := readConfigFile(home)
	if err != nil {
		return cfg, err
	}

	override(&cfg.Model, "CODEFORGE_MODEL")
	override(&cfg.DefaultMode, "CODEFORGE_MODE")
	override(&cfg.Review.Model, "CODEFORGE_REVIEW_MODEL")
	overrideList(&cfg.Judges, "CODEFORGE_JUDGES")
	overrideList(&cfg.Review.SecondOpinion, "CODEFORGE_REVIEW_SECOND_OPINION")

	cfg.Model = cmp.Or(cfg.Model, defaultModel)
	cfg.Review.Model = cmp.Or(cfg.Review.Model, defaultReviewModel)
	if len(cfg.Judges) == 0 {
		cfg.Judges = effort.DefaultJudges
	}
	if len(cfg.Review.SecondOpinion) == 0 {
		cfg.Review.SecondOpinion = defaultSecondOpinion
	}

	return cfg, validate(cfg)
}

// validate keeps every reviewer independent of the code's author and of each other.
func validate(cfg Config) error {
	if err := validMode(cfg.DefaultMode); err != nil {
		return err
	}
	if cfg.Model != "" && cfg.Review.Model == cfg.Model {
		return fmt.Errorf("%w (%s)", errReviewerIsCoder, cfg.Model)
	}
	for _, m := range cfg.Review.SecondOpinion {
		if m == cfg.Review.Model {
			return fmt.Errorf("%w (%s)", errSecondOpinionIsReviewer, m)
		}
		if m == cfg.Model {
			return fmt.Errorf("%w (%s)", errSecondOpinionIsCoder, m)
		}
	}
	return nil
}

// readConfigFile reads only what the user wrote, so `set` and `apply` never bake defaults into the file.
func readConfigFile(home string) (Config, error) {
	var cfg Config

	data, err := os.ReadFile(filepath.Join(configDir(home), "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("decoding config: %w", err)
	}
	return cfg, nil
}

func writeConfigFile(home string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	if err := os.MkdirAll(configDir(home), privateDir); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(configDir(home), "config.json"), append(data, '\n'), privateFile); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
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
