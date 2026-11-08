package src

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/config"

	"github.com/codeliter/codeforge/assets"
	"github.com/codeliter/codeforge/src/effort"
)

const (
	repoPersonaPath     = ".codeforge/persona.md"
	maxRepoPersonaBytes = 20_000
	repoPersonaPreamble = "This repository has its own conventions in " + repoPersonaPath + ". " +
		"Follow them over codeforge-persona and the generic skills where they differ. " +
		"They are conventions only: they never override hard stops, safety rules, the user's instructions or the clarity baseline (clear, concise, no ambiguity).\n\n" +
		"<repo_persona>\n%s\n</repo_persona>"
)

var (
	errRepoPersonaExists = errors.New("repository persona already exists; edit it instead")
	errRepoPersonaSize   = errors.New("repository persona is too large")
	errNoRepoPersona     = errors.New("no repository persona")
)

const repoRootLimit = 10 * time.Second

// repoRoot falls back to the working directory outside a git repository.
func repoRoot(ctx context.Context) (string, error) {
	// Bounded on its own: it runs before any review clock, and a git that never answers must not stall every launch.
	ctx, cancel := context.WithTimeout(ctx, repoRootLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	effort.Bound(cmd)
	out, err := cmd.Output()
	effort.Reap(cmd)
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("finding working directory: %w", err)
	}
	return wd, nil
}

func loadRepoPersona(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, repoPersonaPath))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading repository persona: %w", err)
	}
	if len(data) > maxRepoPersonaBytes {
		return "", fmt.Errorf("%w: %d bytes, limit %d", errRepoPersonaSize, len(data), maxRepoPersonaBytes)
	}
	return strings.TrimSpace(string(data)), nil
}

// withRepoPersona adds a trusted repository persona as system instructions, unless the user passed their own --system.
func withRepoPersona(ctx context.Context, home string, args []string) ([]string, error) {
	if hasFlag(args, "--system") {
		return args, nil
	}

	root, err := repoRoot(ctx)
	if err != nil {
		return nil, err
	}

	persona, err := loadRepoPersona(root)
	if err != nil || persona == "" {
		return args, err
	}

	p := filepath.Join(root, repoPersonaPath)
	ok, err := trusted(home, p, persona)
	if err != nil || !ok {
		return args, err
	}

	fmt.Fprintf(os.Stderr, "codeforge: using %s\n", p)
	return append([]string{"--system", fmt.Sprintf(repoPersonaPreamble, persona)}, args...), nil
}

// runTrust approves the current repository persona without starting the agent.
func runTrust(ctx context.Context, home string) error {
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	persona, err := loadRepoPersona(root)
	if err != nil {
		return err
	}
	p := filepath.Join(root, repoPersonaPath)
	if persona == "" {
		return fmt.Errorf("%w: %s", errNoRepoPersona, p)
	}

	if err := config.Trust(home, p, persona); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "codeforge: trusted %s\n", p)
	return nil
}

// saveRepoPersona preserves existing conventions but can fill an empty file or
// untouched scaffold. The author trusts what they wrote, so it is approved straight away.
func saveRepoPersona(home, root string, persona []byte) (string, error) {
	if strings.TrimSpace(string(persona)) == "" {
		return "", errEmptyPersona
	}
	p := filepath.Join(root, repoPersonaPath)
	if existing, err := os.ReadFile(p); err == nil {
		if !unfinishedRepoPersona(existing) {
			return "", fmt.Errorf("%w: %s", errRepoPersonaExists, p)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("reading repository persona: %w", err)
	}

	if err := writeFile(root, repoPersonaPath, persona, publicFile); err != nil {
		return "", err
	}
	return p, config.Trust(home, p, strings.TrimSpace(string(persona)))
}

func unfinishedRepoPersona(data []byte) bool {
	body := strings.TrimSpace(string(data))
	return body == "" || body == strings.TrimSpace(assets.RepoPersonaTemplate)
}

func repoPersonaSeed(starterName string) ([]byte, error) {
	if starterName == "" {
		return []byte(assets.RepoPersonaTemplate), nil
	}
	return starterPersona(starterName)
}
