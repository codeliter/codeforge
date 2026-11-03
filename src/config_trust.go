package src

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

var errUnapprovedModelEdit = errors.New("project model configuration was not approved; file left unchanged")

func ensureProjectModelEditing(home, project string, replace func() bool) error {
	path := projectConfigPath(project)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading project configuration: %w", err)
	}
	ledger, err := loadTrust(home)
	if err != nil {
		return err
	}
	if ledger[path] == digest(string(data)) || replace() {
		return nil
	}
	return errUnapprovedModelEdit
}

func modelConfigTrustPreview(home, content string) (string, error) {
	local, err := decodeConfig("config.yaml", []byte(content))
	if err != nil {
		return "", err
	}
	global, err := readConfigFile(home)
	if err != nil {
		return "", err
	}
	effective := configDefaults(mergeConfig(global, local))
	if err := validate(effective); err != nil {
		return "", err
	}
	encoded, err := yaml.Marshal(effective)
	if err != nil {
		return "", fmt.Errorf("encoding effective model preview: %w", err)
	}
	return "Effective model settings (before one-off environment/CLI overrides):\n" + string(encoded), nil
}
func sameConfigScope(home, project string) bool {
	if projectConfigPath(home) == projectConfigPath(project) {
		return true
	}
	a, errA := os.Stat(home)
	b, errB := os.Stat(project)
	return errA == nil && errB == nil && os.SameFile(a, b)
}
func projectConfigPath(project string) string {
	if canonical, err := filepath.EvalSymlinks(project); err == nil {
		project = canonical
	}
	return filepath.Join(project, ".codeforge", "config.yaml")
}

// Check trust before parsing, and parse the exact bytes whose hash was checked.
// A checkout cannot nominate its own reviewers or change provider routing silently.
func readTrustedProjectConfig(home, project string) (Config, error) {
	path := projectConfigPath(project)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading project configuration: %w", err)
	}
	ledger, err := loadTrust(home)
	if err != nil {
		return Config{}, err
	}
	if ledger[path] != digest(string(data)) && interactive() {
		candidate, parseErr := decodeConfig(path, data)
		if parseErr == nil {
			global, readErr := readConfigFile(home)
			if readErr != nil {
				return Config{}, readErr
			}
			parseErr = validate(configDefaults(mergeConfig(global, candidate)))
		}
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "codeforge: skipping invalid untrusted model configuration %s: %v\n", path, parseErr)
			return Config{}, nil
		}
	}
	ok, err := trusted(home, path, string(data))
	if err != nil || !ok {
		return Config{}, err
	}
	return decodeConfig(path, data)
}

func approveProjectConfig(home, project string) error {
	path := projectConfigPath(project)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading project configuration: %w", err)
	}
	local, err := decodeConfig(path, data)
	if err != nil {
		return err
	}
	global, err := readConfigFile(home)
	if err != nil {
		return err
	}
	if err := validate(configDefaults(mergeConfig(global, local))); err != nil {
		return err
	}
	return trust(home, path, string(data))
}

func runTrustModels(home, project string) error {
	if err := approveProjectConfig(home, project); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "codeforge: trusted model configuration %s\n", projectConfigPath(project))
	return nil
}
