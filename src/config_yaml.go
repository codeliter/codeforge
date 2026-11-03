package src

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
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

var errConfigDocuments = errors.New("configuration must contain exactly one YAML document")
var errConfigSymlink = errors.New("configuration paths must not be symlinks; edit the target explicitly")
var errRoutingJob = errors.New("unsupported profile routing job; configure other background jobs in Empryo")

var routingJobs = []string{"research", "editing", "explore", "web-search", "cleanup", "verify", "review", "goal-review", "summaries", "marionette"}

func validModelID(id string) bool {
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

func validateRouting(routing map[string]string) error {
	for job, model := range routing {
		known := false
		for _, supported := range routingJobs {
			known = known || job == supported
		}
		if !known {
			return fmt.Errorf("%w: %s", errRoutingJob, job)
		}
		if model != "" && !validModelReference(model) {
			return fmt.Errorf("%w for routing.%s: %q", errNotProviderModel, job, model)
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
		return decodeConfig(path, data)
	}
	return Config{}, nil
}
func decodeConfig(path string, data []byte) (Config, error) {
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

func mergeConfig(base, local Config) Config {
	if local.Providers != nil {
		policy := *local.Providers
		base.Providers = &policy
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
	if local.Review.SecondOpinion != nil {
		base.Review.SecondOpinion = local.Review.SecondOpinion
	}
	base.Routing = maps.Clone(base.Routing)
	if local.Routing != nil && base.Routing == nil {
		base.Routing = make(map[string]string)
	}
	for job, model := range local.Routing {
		if model == "" {
			delete(base.Routing, job)
		} else {
			base.Routing[job] = model
		}
	}
	return base
}

func writeConfigYAML(dir string, cfg Config) error { _, err := saveConfigYAML(dir, cfg); return err }
func saveConfigYAML(dir string, cfg Config) (string, error) {
	if err := validate(cfg); err != nil {
		return "", err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encoding configuration: %w", err)
	}
	if err := os.MkdirAll(dir, privateDir); err != nil {
		return "", fmt.Errorf("creating configuration directory: %w", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %s", errConfigSymlink, path)
	}
	if err := atomicWriteFile(path, data, privateFile); err != nil {
		return "", err
	}
	return string(data), nil
}
