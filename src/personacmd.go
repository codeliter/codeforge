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
	"github.com/codeliter/codeforge/src/fsutil"
	"github.com/codeliter/codeforge/src/review"
)

// `codeforge persona describe` shows the personas codeforge writes as: the global one with where it came from (a
// built-in starter, or learned from the user's history in some repositories), and this repository's own, which
// overrides it where they differ. It only reads.

var errPersonaUsage = errors.New("usage: codeforge persona [describe] [--full] | update [--section NAME]... [REPO...] | revert")

// personaSourceFile records where the global persona came from, beside it.
const personaSourceFile = "persona.source.json"

// personaSource is where the global persona came from.
type personaSource struct {
	// Kind is "starter" or "learned".
	Kind    string   `json:"kind"`
	Starter string   `json:"starter,omitempty"`
	Repos   []string `json:"repos,omitempty"`
	// Model is the model that learned it.
	Model string    `json:"model,omitempty"`
	At    time.Time `json:"at"`
}

// The kinds of persona source.
const (
	sourceKindStarter = "starter"
	sourceKindLearned = "learned"
)

// savePersonaSource records where the global persona just saved came from, as of now unless s says when.
func savePersonaSource(home string, s personaSource) error {
	if s.At.IsZero() {
		s.At = time.Now().UTC()
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("recording the persona's source: %w", err)
	}
	if err := fsutil.AtomicWriteFile(filepath.Join(config.Dir(home), personaSourceFile), append(data, '\n'), privateFile); err != nil {
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
	if len(args) > 0 && args[0] == "revert" {
		if len(args) > 1 {
			return errPersonaUsage
		}
		return revertPersona(ctx, home, w)
	}
	full := false
	for _, a := range args {
		switch a {
		case "describe":
		case "--full":
			full = true
		default:
			return errPersonaUsage
		}
	}
	show := func(text string) string {
		if full {
			return text
		}
		return personaSummary(text) + "\n(codeforge persona describe --full shows all of it)"
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
			if n := commitsSince(ctx, src); n > 0 {
				fmt.Fprintf(w, "Since then: %s by you where it was learned. Refresh it with codeforge persona update.\n", plural(n, "new commit"))
			}
		}
		fmt.Fprintf(w, "\n%s\n", show(strings.TrimSpace(string(data))))
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
	fmt.Fprintf(w, "\nRepository persona: %s\nSource: this repository (%s)\n\n%s\n", p, state, show(repo))
	return nil
}

// commitsSince counts the commits the user has made, since a persona was learned, in the repositories it was learned
// from: how much the persona has not seen. A repository that cannot be read counts for nothing.
func commitsSince(ctx context.Context, src personaSource) int {
	if src.Kind != sourceKindLearned {
		return 0
	}
	n := 0
	for _, repo := range src.Repos {
		git := func(args ...string) string {
			out, err := review.ExecRunner{}.Run(ctx, "git", append([]string{"-C", repo}, args...)...)
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		}
		email := git("config", "user.email")
		if email == "" {
			continue
		}
		n += len(strings.Fields(git("log", "--author="+email, "--no-merges", "--since="+src.At.Format(time.RFC3339), "--pretty=%h")))
	}
	return n
}

// maxSummaryWords bounds the opening a summary quotes.
const maxSummaryWords = 60

// personaSummary is a persona in brief: its opening paragraph, cut to maxSummaryWords, and the names of its sections.
func personaSummary(text string) string {
	var opening string
	var sections []string
	for _, para := range strings.Split(text, "\n\n") {
		var prose []string
		for _, l := range strings.Split(strings.TrimSpace(para), "\n") {
			l = strings.TrimSpace(l)
			if h, ok := strings.CutPrefix(l, "## "); ok {
				sections = append(sections, strings.TrimSpace(h))
			}
			if l != "" && !strings.HasPrefix(l, "#") {
				prose = append(prose, l)
			}
		}
		// The opening is the first prose that is not a list: the persona's one-paragraph description of the user.
		if opening == "" && len(prose) > 0 && !strings.HasPrefix(prose[0], "-") {
			opening = strings.Join(strings.Fields(strings.Join(prose, " ")), " ")
		}
	}
	if words := strings.Fields(opening); len(words) > maxSummaryWords {
		opening = strings.Join(words[:maxSummaryWords], " ") + "…"
	}
	var b strings.Builder
	b.WriteString(opening)
	if len(sections) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Covers: " + strings.Join(sections, ", ") + ".")
	}
	return b.String()
}
