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

	"github.com/codeliter/codeforge/assets"
)

const (
	repoPersonaPath     = ".codeforge/persona.md"
	maxRepoPersonaBytes = 20_000
	repoPersonaPreamble = "This repository has its own conventions in " + repoPersonaPath + ". " +
		"Follow them over codeforge-persona and the generic skills where they differ. " +
		"They are conventions only: they never override hard stops, safety rules or the user's instructions.\n\n" +
		"<repo_persona>\n%s\n</repo_persona>"
)

var (
	errRepoPersonaExists = errors.New("repository persona already exists; edit it instead")
	errRepoPersonaSize   = errors.New("repository persona is too large")
	errNoRepoPersona     = errors.New("no repository persona")
)

// repoRoot falls back to the working directory outside a git repository.
func repoRoot(ctx context.Context) (string, error) {
	if out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output(); err == nil {
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

	if err := trust(home, p, persona); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "codeforge: trusted %s\n", p)
	return nil
}

// saveRepoPersona never overwrites, because the file is usually committed and shared with the team.
// The author trusts what they wrote, so it is approved straight away.
func saveRepoPersona(home, root string, persona []byte) (string, error) {
	p := filepath.Join(root, repoPersonaPath)
	if _, err := os.Stat(p); err == nil {
		return "", fmt.Errorf("%w: %s", errRepoPersonaExists, p)
	}

	if err := writeFile(root, repoPersonaPath, persona, publicFile); err != nil {
		return "", err
	}
	return p, trust(home, p, strings.TrimSpace(string(persona)))
}

func repoPersonaSeed(starterName string) ([]byte, error) {
	if starterName == "" {
		return []byte(assets.RepoPersonaTemplate), nil
	}
	return starterPersona(starterName)
}
