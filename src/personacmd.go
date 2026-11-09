package src

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/config"
)

// `codeforge persona describe` shows the personas codeforge writes as: the global one with where it came from (a
// built-in starter, or learned from the user's history in some repositories), and this repository's own, which
// overrides it where they differ. It only reads.

var errPersonaUsage = errors.New("usage: codeforge persona [describe]")

// personaSourceFile records where the global persona came from, beside it.
const personaSourceFile = "persona.source.json"

// personaSource is where the global persona came from.
type personaSource struct {
	// Kind is "starter" or "learned".
	Kind    string    `json:"kind"`
	Starter string    `json:"starter,omitempty"`
	Repos   []string  `json:"repos,omitempty"`
	At      time.Time `json:"at"`
}

// The kinds of persona source.
const (
	sourceKindStarter = "starter"
	sourceKindLearned = "learned"
)

// savePersonaSource records where the global persona just saved came from.
func savePersonaSource(home string, s personaSource) error {
	s.At = time.Now().UTC()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("recording the persona's source: %w", err)
	}
	if err := os.WriteFile(filepath.Join(config.Dir(home), personaSourceFile), append(data, '\n'), privateFile); err != nil {
		return fmt.Errorf("recording the persona's source: %w", err)
	}
	return nil
}

// globalPersonaSource says where the global persona came from: its record, or, for a persona saved before sources
// were recorded, the starter it still reads as. ok is false when that cannot be told.
func globalPersonaSource(home string, persona []byte) (personaSource, bool) {
	if data, err := os.ReadFile(filepath.Join(config.Dir(home), personaSourceFile)); err == nil {
		var s personaSource
		if json.Unmarshal(data, &s) == nil && s.Kind != "" {
			return s, true
		}
	}
	for _, st := range starters() {
		if body, err := starterPersona(st.Name); err == nil && strings.HasPrefix(strings.TrimSpace(string(persona)), strings.TrimSpace(string(body))) {
			return personaSource{Kind: sourceKindStarter, Starter: st.Name}, true
		}
	}
	return personaSource{}, false
}

// describe says where a persona came from, in one line.
func (s personaSource) describe(known bool) string {
	switch {
	case !known:
		return "written by you or learned before codeforge recorded sources"
	case s.Kind == sourceKindStarter:
		return fmt.Sprintf("the built-in %q starter", s.Starter)
	case len(s.Repos) > 0:
		return "learned from your history in " + strings.Join(s.Repos, ", ")
	}
	return "learned from your history"
}

// runPersona prints the personas in effect here and where each came from.
func runPersona(ctx context.Context, home string, args []string, w io.Writer) error {
	if len(args) > 1 || len(args) == 1 && args[0] != "describe" {
		return errPersonaUsage
	}
	global := filepath.Join(config.Dir(home), "persona.md")
	data, err := os.ReadFile(global)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(w, "Global persona: none. Set one with codeforge init --global.")
	case err != nil:
		return fmt.Errorf("reading the global persona: %w", err)
	default:
		src, known := globalPersonaSource(home, data)
		fmt.Fprintf(w, "Global persona: %s\nSource: %s\n", global, src.describe(known))
		if known && !src.At.IsZero() {
			fmt.Fprintf(w, "Set: %s\n", src.At.UTC().Format("2 Jan 2006 15:04 UTC"))
		}
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(string(data)))
	}

	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}
	repo, err := loadRepoPersona(root)
	if err != nil {
		return err
	}
	p := filepath.Join(root, repoPersonaPath)
	if repo == "" {
		fmt.Fprintf(w, "\nRepository persona: none in %s. Add one with codeforge init --repo.\n", root)
		return nil
	}
	ts, err := config.LoadTrust(home)
	if err != nil {
		return err
	}
	state := "in use: it overrides the global persona where they differ"
	if ts[p] != config.Digest(repo) {
		state = "not in use: new or changed since you approved it. Run codeforge trust to use it"
	}
	fmt.Fprintf(w, "\nRepository persona: %s\nSource: this repository (%s)\n\n%s\n", p, state, repo)
	return nil
}
