package src

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/codeliter/codeforge/src/config"

	"go.yaml.in/yaml/v3"

	"github.com/codeliter/codeforge/assets"
)

var errProfileRootSymlink = errors.New("workspace .empryo must not be a symlink")

func profileModelScalar(model string) (string, error) {
	encoded, err := yaml.Marshal(model)
	if err != nil {
		return "", fmt.Errorf("encoding profile model: %w", err)
	}
	return strings.TrimSuffix(string(encoded), "\n"), nil
}
func renderAgent(body []byte, cfg config.Config) ([]byte, error) {
	expert := cfg.Expert
	if expert == "" {
		expert = config.DefaultExpertModel
	}
	coder, err := profileModelScalar(cfg.Model)
	if err != nil {
		return nil, err
	}
	reviewer, err := profileModelScalar(cfg.Review.Model)
	if err != nil {
		return nil, err
	}
	expert, err = profileModelScalar(expert)
	if err != nil {
		return nil, err
	}
	text := strings.NewReplacer("{{expert}}", expert, "{{coder}}", coder, "{{reviewer}}", reviewer).Replace(string(body))
	routing := make(map[string]string)
	for job, model := range cfg.Routing {
		if model != "" {
			routing[job] = model
		}
	}
	if len(routing) == 0 {
		return []byte(text), nil
	}
	encoded, err := yaml.Marshal(map[string]any{"routing": routing})
	if err != nil {
		return nil, fmt.Errorf("encoding profile routing: %w", err)
	}
	return []byte(strings.Replace(text, "---\n", "---\n"+string(encoded), 1)), nil
}

// Project-owned profiles prevent simultaneous projects from overwriting one
// another's role/routing choices in the global Codeforge profiles.
func syncProjectAgents(workspace string, cfg config.Config) error {
	if info, err := os.Lstat(filepath.Join(workspace, ".empryo")); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errProfileRootSymlink
	}
	err := fs.WalkDir(assets.Profile, "agents", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := assets.Profile.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading profile %s: %w", path, err)
		}
		body, err = renderAgent(body, cfg)
		if err != nil {
			return err
		}
		root := filepath.Join(workspace, ".empryo")
		if existing, err := os.ReadFile(filepath.Join(root, path)); err == nil && bytes.Equal(existing, body) && !isSymlinked(root, path) {
			return nil
		}
		return writeFile(root, path, body, publicFile)
	})
	if err != nil {
		return fmt.Errorf("syncing project profiles: %w", err)
	}
	return nil
}
