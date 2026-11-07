package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// ErrUnapprovedEdit is returned when a project configuration would be edited without having been approved.
var ErrUnapprovedEdit = errors.New("project model configuration was not approved; file left unchanged")

// EnsureProjectEditing refuses to edit a project configuration that was never approved, unless replace agrees to
// replace it; the file is left as it is.
func EnsureProjectEditing(home, project string, replace func() bool) error {
	path := ProjectPath(project)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading project configuration: %w", err)
	}
	ledger, err := LoadTrust(home)
	if err != nil {
		return err
	}
	if ledger[path] == Digest(string(data)) || replace() {
		return nil
	}
	return ErrUnapprovedEdit
}

// ModelPreview renders the model settings that content would produce on top of the global configuration.
func ModelPreview(home, content string) (string, error) {
	local, err := Decode("config.yaml", []byte(content))
	if err != nil {
		return "", err
	}
	global, err := ReadFile(home)
	if err != nil {
		return "", err
	}
	effective := Defaults(Merge(global, local))
	if err := Validate(effective); err != nil {
		return "", err
	}
	encoded, err := yaml.Marshal(effective)
	if err != nil {
		return "", fmt.Errorf("encoding effective model preview: %w", err)
	}
	return "Effective model settings (before one-off environment/CLI overrides):\n" + string(encoded), nil
}

// SameScope reports whether project is the home directory itself, where the project file is the global one.
func SameScope(home, project string) bool {
	if ProjectPath(home) == ProjectPath(project) {
		return true
	}
	a, errA := os.Stat(home)
	b, errB := os.Stat(project)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// ProjectPath is where a project keeps its model configuration.
func ProjectPath(project string) string {
	if canonical, err := filepath.EvalSymlinks(project); err == nil {
		project = canonical
	}
	return filepath.Join(project, ".codeforge", "config.yaml")
}

// Approver is asked before a new or changed project configuration is used: a checkout must not nominate its own
// reviewers or change provider routing silently. It gets the file's path and a preview of the settings it would
// produce, and reports whether the user accepts it. A nil Approver means nobody is there to ask.
type Approver func(path, preview string) (bool, error)

// ReadTrustedProject reads the project configuration, if its exact bytes were approved before or ask approves them
// now. An unapproved or invalid file is skipped with a note on standard error, never an error: the global
// configuration still applies.
func ReadTrustedProject(home, project string, ask Approver) (Config, error) {
	path := ProjectPath(project)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading project configuration: %w", err)
	}
	ledger, err := LoadTrust(home)
	if err != nil {
		return Config{}, err
	}
	content := string(data)
	if ledger[path] == Digest(content) {
		return Decode(path, data)
	}
	if ask == nil {
		fmt.Fprintf(os.Stderr, "codeforge: skipping %s: new or changed since you last approved it. Run `codeforge trust --models` to use it.\n", path)
		return Config{}, nil
	}
	preview, err := ModelPreview(home, content)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codeforge: skipping invalid untrusted model configuration %s: %v\n", path, err)
		return Config{}, nil
	}
	ok, err := ask(path, preview)
	if err != nil || !ok {
		return Config{}, err
	}
	if err := Trust(home, path, content); err != nil {
		return Config{}, err
	}
	return Decode(path, data)
}

// ApproveProject records the project configuration as it is now as approved, after checking that it is valid.
func ApproveProject(home, project string) error {
	path := ProjectPath(project)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading project configuration: %w", err)
	}
	local, err := Decode(path, data)
	if err != nil {
		return err
	}
	global, err := ReadFile(home)
	if err != nil {
		return err
	}
	if err := Validate(Defaults(Merge(global, local))); err != nil {
		return err
	}
	return Trust(home, path, string(data))
}
