package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/codeliter/codeforge/src/fsutil"
)

var errConfigDocuments = errors.New("configuration must contain exactly one YAML document")
var ErrConfigSymlink = errors.New("configuration paths must not be symlinks; edit the target explicitly")
var ErrRoutingJob = errors.New("unsupported profile routing job; configure other background jobs in Empryo")

var RoutingJobs = []string{"research", "editing", "explore", "web-search", "cleanup", "verify", "review", "goal-review", "summaries", "marionette"}

func ValidModelID(id string) bool {
	provider, model, ok := strings.Cut(id, "/")
	if !ok || provider == "" || model == "" || strings.ContainsFunc(id, unicode.IsSpace) {
		return false
	}
	for _, c := range provider {
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !allowed {
			return false
		}
	}
	return !strings.ContainsFunc(id, unicode.IsControl)
}

// Helper jobs Codeforge routes by itself when the configuration does not name them. Reading and summarising go to
// the first task-sizing model, where a mistake is cheap to spot; checking the work goes to the reviewer, so it is not
// the coder marking its own. Editing, cleanup and the pre-pass are left to the model the session runs on.
var (
	lightJobs = []string{"research", "explore", "web-search", "summaries"}
	checkJobs = []string{"verify", "review", "goal-review"}
)

// EffectiveRouting is the model each helper job runs on: what the configuration names, and for the jobs it does not
// name, the role suited to the job. An empty entry keeps that job on native Empryo routing, so it is left out. Roles
// not yet resolved to a provider/model ID route nothing: a profile never carries a guessed ID.
func EffectiveRouting(cfg Config) map[string]string {
	routing := make(map[string]string)
	auto := func(jobs []string, model string) {
		if !ValidModelID(model) {
			return
		}
		for _, job := range jobs {
			routing[job] = model
		}
	}
	if len(cfg.Judges) > 0 {
		auto(lightJobs, cfg.Judges[0])
	}
	auto(checkJobs, cfg.Review.Model)
	for job, model := range cfg.Routing {
		if model == "" {
			delete(routing, job)
			continue
		}
		routing[job] = model
	}
	return routing
}

func validateRouting(routing map[string]string) error {
	for job, model := range routing {
		known := false
		for _, supported := range RoutingJobs {
			known = known || job == supported
		}
		if !known {
			return fmt.Errorf("%w: %s", ErrRoutingJob, job)
		}
		if model != "" && !ValidModelReference(model) {
			return fmt.Errorf("%w for routing.%s: %q", ErrNotProviderModel, job, model)
		}
	}
	return nil
}

func readConfigDirectory(dir string, legacy bool) (Config, error) {
	paths := []string{filepath.Join(dir, "config.yaml")}
	if legacy {
		paths = append(paths, filepath.Join(dir, "config.json"))
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			if _, e := os.Lstat(path); errors.Is(e, fs.ErrNotExist) {
				continue
			}
		}
		if err != nil {
			return Config{}, fmt.Errorf("reading %s: %w", path, err)
		}
		return Decode(path, data)
	}
	return Config{}, nil
}
func Decode(path string, data []byte) (Config, error) {
	var cfg Config
	var err error
	if filepath.Ext(path) == ".json" {
		err = json.Unmarshal(data, &cfg)
	} else {
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		err = decoder.Decode(&cfg)
		if err == nil {
			var extra any
			if next := decoder.Decode(&extra); !errors.Is(next, io.EOF) {
				err = errConfigDocuments
			}
		}
	}
	if err != nil {
		return cfg, fmt.Errorf("decoding %s: %w", path, err)
	}
	return cfg, nil
}

func Merge(base, local Config) Config {
	if local.Providers != nil {
		base.Providers = local.Providers.Clone()
	}
	if local.Model != "" {
		base.Model = local.Model
	}
	if local.Expert != "" {
		base.Expert = local.Expert
	}
	if local.Init != "" {
		base.Init = local.Init
	}
	if local.DefaultMode != "" {
		base.DefaultMode = local.DefaultMode
	}
	if local.Judges != nil {
		base.Judges = local.Judges
	}
	if local.Review.Model != "" {
		base.Review.Model = local.Review.Model
	}
	if local.Review.Light != "" {
		base.Review.Light = local.Review.Light
	}
	if local.Review.SecondOpinion != nil {
		base.Review.SecondOpinion = local.Review.SecondOpinion
	}
	// A project adds sensitive paths to the global ones and never removes any: they can only add scrutiny.
	for _, p := range local.Review.SensitivePaths {
		if !slices.Contains(base.Review.SensitivePaths, p) {
			base.Review.SensitivePaths = append(slices.Clone(base.Review.SensitivePaths), p)
		}
	}
	base.Routing = maps.Clone(base.Routing)
	if local.Routing != nil && base.Routing == nil {
		base.Routing = make(map[string]string)
	}
	// An empty entry is kept: it says the job stays on native Empryo routing, which no entry at all does not say.
	for job, model := range local.Routing {
		base.Routing[job] = model
	}
	return base
}

func WriteYAML(dir string, cfg Config) error { _, err := SaveYAML(dir, cfg); return err }
func SaveYAML(dir string, cfg Config) (string, error) {
	if err := Validate(cfg); err != nil {
		return "", err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encoding configuration: %w", err)
	}
	if err := os.MkdirAll(dir, fsutil.PrivateDir); err != nil {
		return "", fmt.Errorf("creating configuration directory: %w", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %s", ErrConfigSymlink, path)
	}
	if err := fsutil.AtomicWriteFile(path, data, fsutil.PrivateFile); err != nil {
		return "", err
	}
	return string(data), nil
}
