package src

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/charmbracelet/huh"
)

const trustFile = "trusted-personas.json"

// trustStore maps approved repository persona/model-config paths to content hashes.
// The legacy filename is retained for compatibility.
type trustStore map[string]string

func digest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func loadTrust(home string) (trustStore, error) {
	ts := trustStore{}

	data, err := os.ReadFile(filepath.Join(configDir(home), trustFile))
	if errors.Is(err, fs.ErrNotExist) {
		return ts, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading trusted personas: %w", err)
	}

	if err := json.Unmarshal(data, &ts); err != nil {
		return nil, fmt.Errorf("decoding trusted personas: %w", err)
	}
	return ts, nil
}

func saveTrust(home string, ts trustStore) error {
	data, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding trusted personas: %w", err)
	}

	if err := os.MkdirAll(configDir(home), privateDir); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(configDir(home), trustFile), append(data, '\n'), privateFile); err != nil {
		return fmt.Errorf("writing trusted personas: %w", err)
	}
	return nil
}

func trust(home, path, content string) error {
	ts, err := loadTrust(home)
	if err != nil {
		return err
	}

	ts[path] = digest(content)
	return saveTrust(home, ts)
}

// trusted asks before a new or changed repository persona reaches the system prompt,
// because a cloned repository could use it to steer the agent.
func trusted(home, path, content string) (bool, error) {
	ts, err := loadTrust(home)
	if err != nil {
		return false, err
	}
	if ts[path] == digest(content) {
		return true, nil
	}

	command := "codeforge trust"
	title := "Use this repository's persona?"
	explanation := "It becomes part of the agent's instructions in this repository."
	models := filepath.Base(path) == "config.yaml" && filepath.Base(filepath.Dir(path)) == ".codeforge"
	if models {
		command = "codeforge trust --models"
		title = "Use this repository's model configuration?"
		explanation = "It can select providers, models, reviewers and helper routing. Review this file before trusting it."
	}
	if !interactive() {
		fmt.Fprintf(os.Stderr, "codeforge: skipping %s: new or changed since you last approved it. Run `%s` to use it.\n", path, command)
		return false, nil
	}

	display := preview(content)
	if models {
		var err error
		display, err = modelConfigTrustPreview(home, content)
		if err != nil {
			return false, err
		}
	}
	ok := false
	if err := runForm("trust prompt", huh.NewConfirm().
		Title(title).
		Description(fmt.Sprintf("%s is new or changed. %s\n\n%s", path, explanation, display)).
		Affirmative("Trust it").
		Negative("Skip").
		Value(&ok)); err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return true, trust(home, path, content)
}

func preview(content string) string {
	const maxPreview = 1500
	if len(content) <= maxPreview {
		return content
	}
	return content[:maxPreview] + "\n…"
}
