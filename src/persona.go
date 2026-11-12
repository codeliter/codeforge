package src

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/fsutil"

	"github.com/codeliter/codeforge/assets"
)

const (
	personaHeader = "---\nname: codeforge-persona\ndescription: The user's own voice, PR format and code style, set by `codeforge init`. Overrides codeforge-voice and codeforge-code where they differ, except for the clarity baseline.\n---\n\n" + clarityBaseline + "\n\n"
	// clarityBaseline is the floor under every persona, learned, starter or per repository: the persona decides tone,
	// format and wording, never whether a text is clear.
	clarityBaseline = "Baseline for every text, whatever the persona says: clear, concise and free of ambiguity. " +
		"One meaning per sentence, no filler, no hedging, and every ask stated as an ask. " +
		"The persona below sets tone, format and wording on top of this; it never lowers it."
	// reviewPersonaSkill is the persona skill of the profiles that only review.
	reviewPersonaSkill  = "codeforge-persona-review"
	reviewPersonaHeader = "---\nname: " + reviewPersonaSkill + "\ndescription: The user's persona for reviewing: voice, review comments and code style, set by `codeforge init`. Overrides codeforge-voice and codeforge-code where they differ, except for the clarity baseline.\n---\n\n" +
		"This is `codeforge-persona` as a reviewer needs it. Wherever an instruction names `codeforge-persona`, it means this.\n\n" + clarityBaseline + "\n\n"
	emptyPersona = personaHeader + "No persona yet. Follow codeforge-voice and codeforge-code, and suggest running `codeforge init`.\n"
)

// notForReview are the persona sections about work a reviewer never does.
var notForReview = []string{"Commit messages", "Pull requests", "Replies to reviewers"}

var (
	errNoHistory      = errors.New("found none of your commits, PRs or reviews; pick a repository you have worked in, or a starter persona")
	errNoEmail        = errors.New("git user.email is not set")
	errUnknownPersona = errors.New("unknown starter persona")
	errEmptyPersona   = errors.New("empryo returned no persona")
)

type starter struct {
	Name        string
	Summary     string
	Description string
}

var starterDescriptions = map[string]string{
	"oss":       "Friendly reviews, stable APIs, and good docs.",
	"pragmatic": "Small, clear changes backed by tests.",
	"regulated": "Careful changes with risk, audit, and rollback notes.",
	"startup":   "Ship quickly with small PRs and practical follow-ups.",
}

// starters lists the embedded personas for users with no history, such as a brand-new repository.
func starters() []starter {
	entries, err := fs.ReadDir(assets.Personas, "personas")
	if err != nil {
		return nil
	}

	var out []starter
	for _, e := range entries {
		data, err := assets.Personas.ReadFile(path.Join("personas", e.Name()))
		if err != nil {
			continue
		}
		title, _, _ := strings.Cut(string(data), "\n")
		name := strings.TrimSuffix(e.Name(), ".md")
		summary := strings.TrimPrefix(title, "# Persona: ")
		description := starterDescriptions[name]
		if description == "" {
			description = summary
		}
		out = append(out, starter{
			Name:        name,
			Summary:     summary,
			Description: description,
		})
	}
	return out
}

func starterPersona(name string) ([]byte, error) {
	if !slices.ContainsFunc(starters(), func(s starter) bool { return s.Name == name }) {
		return nil, fmt.Errorf("%w %q", errUnknownPersona, name)
	}

	data, err := assets.Personas.ReadFile(path.Join("personas", name+".md"))
	if err != nil {
		return nil, fmt.Errorf("reading starter persona: %w", err)
	}
	return data, nil
}

// savePersona writes the global persona, keeping the one it replaces beside it. Readers see the old persona or the new
// one, never a part of either.
func savePersona(home string, persona []byte) (string, error) {
	if strings.TrimSpace(string(persona)) == "" {
		return "", errEmptyPersona
	}
	if err := os.MkdirAll(config.Dir(home), privateDir); err != nil {
		return "", fmt.Errorf("creating config dir: %w", err)
	}

	p := filepath.Join(config.Dir(home), "persona.md")
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("reading the persona to keep a copy of it: %w", err)
	}
	if strings.TrimSpace(string(old)) != "" && !bytes.Equal(old, persona) {
		if err := fsutil.AtomicWriteFile(filepath.Join(config.Dir(home), previousPersonaFile), old, privateFile); err != nil {
			return "", fmt.Errorf("keeping the previous persona: %w", err)
		}
		// Where that persona came from is kept with it, so a revert puts both back.
		source, err := os.ReadFile(filepath.Join(config.Dir(home), personaSourceFile))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("reading the persona's source to keep a copy of it: %w", err)
		}
		if err := fsutil.AtomicWriteFile(filepath.Join(config.Dir(home), previousSourceFile), source, privateFile); err != nil {
			return "", fmt.Errorf("keeping the previous persona's source: %w", err)
		}
	}
	if err := fsutil.AtomicWriteFile(p, persona, privateFile); err != nil {
		return "", fmt.Errorf("writing persona: %w", err)
	}
	return p, installPersonaSkill(home)
}

// installPersonaSkill installs the persona, or a placeholder so the profile's skill list always resolves.
func installPersonaSkill(home string) error {
	body := emptyPersona
	data, err := os.ReadFile(filepath.Join(config.Dir(home), "persona.md"))
	switch {
	case err == nil:
		if strings.TrimSpace(string(data)) != "" {
			body = personaHeader + string(data)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("reading persona: %w", err)
	}

	if err := writeFile(filepath.Join(home, ".empryo"), "skills/codeforge-persona/SKILL.md", []byte(body), privateFile); err != nil {
		return err
	}
	review := reviewPersonaHeader + "No persona yet. Follow codeforge-voice and codeforge-code.\n"
	if body != emptyPersona {
		review = reviewPersonaHeader + reviewPersona(string(data))
	}
	return writeFile(filepath.Join(home, ".empryo"), "skills/"+reviewPersonaSkill+"/SKILL.md", []byte(review), privateFile)
}

// reviewPersona is the persona as the reviewing profiles load it: without the sections on work a reviewer never does,
// so the rules on reviewing are not diluted by rules on commits and pull requests. The opening and the sections the
// user added themselves stay.
func reviewPersona(doc string) string {
	sections := slices.DeleteFunc(splitPersona(doc), func(s personaSection) bool { return slices.Contains(notForReview, s.title) })
	return joinPersona(sections)
}

// repoSlug turns an origin URL into owner/name, or "" when the remote is not on GitHub.
func repoSlug(remote string) string {
	for _, prefix := range []string{"git@github.com:", "https://github.com/", "ssh://git@github.com/"} {
		if rest, ok := strings.CutPrefix(remote, prefix); ok {
			return strings.TrimSuffix(rest, ".git")
		}
	}
	return ""
}
