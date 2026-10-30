package src

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/codeliter/codeforge/assets"
	"github.com/codeliter/codeforge/src/review"
)

const (
	maxCorpusBytes = 300_000
	maxDiffBytes   = 120_000
	maxStderrBytes = 300
	personaHeader  = "---\nname: codeforge-persona\ndescription: The user's own voice, PR format and code style, set by `codeforge init`. Overrides codeforge-voice and codeforge-code where they differ.\n---\n\n"
	emptyPersona   = personaHeader + "No persona yet. Follow codeforge-voice and codeforge-code, and suggest running `codeforge init`.\n"
)

var (
	errNoHistory      = errors.New("found none of your commits, PRs or reviews; pick a repository you have worked in, or a starter persona")
	errNoEmail        = errors.New("git user.email is not set")
	errUnknownPersona = errors.New("unknown starter persona")
	errEmptyPersona   = errors.New("empryo returned no persona")
)

type starter struct {
	Name    string
	Summary string
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
		out = append(out, starter{
			Name:    strings.TrimSuffix(e.Name(), ".md"),
			Summary: strings.TrimPrefix(title, "# Persona: "),
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

// learnPersona mines the user's own history in each repo and asks Empryo to distil it into a persona.
func learnPersona(ctx context.Context, bin, model string, repos []string) ([]byte, error) {
	var corpus bytes.Buffer
	for _, repo := range repos {
		fmt.Fprintf(os.Stderr, "codeforge: reading your history in %s\n", repo)
		if err := collect(ctx, review.ExecRunner{}, repo, &corpus); err != nil {
			return nil, fmt.Errorf("reading %s: %w", repo, err)
		}
	}
	if corpus.Len() == 0 {
		return nil, errNoHistory
	}

	fmt.Fprintln(os.Stderr, "codeforge: learning your style, this takes a minute or two")
	return distil(ctx, bin, model, truncate(corpus.String(), maxCorpusBytes))
}

func distil(ctx context.Context, bin, model, corpus string) ([]byte, error) {
	args := []string{"--headless", "--json", "--no-profile", "--no-genome", "--effort", "high", "--max-steps", "1"}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "--system", assets.InitPrompt, "<history>\n"+corpus+"\n</history>")

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running empryo: %w: %s", err, truncate(stderr.String(), maxStderrBytes))
	}

	var res struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("decoding empryo result: %w", err)
	}
	if res.Error != "" || strings.TrimSpace(res.Output) == "" {
		return nil, fmt.Errorf("%w: %s", errEmptyPersona, res.Error)
	}
	return []byte(strings.TrimSpace(res.Output) + "\n"), nil
}

func savePersona(home string, persona []byte) (string, error) {
	if err := os.MkdirAll(configDir(home), privateDir); err != nil {
		return "", fmt.Errorf("creating config dir: %w", err)
	}

	p := filepath.Join(configDir(home), "persona.md")
	if err := os.WriteFile(p, persona, privateFile); err != nil {
		return "", fmt.Errorf("writing persona: %w", err)
	}
	return p, installPersonaSkill(home)
}

// installPersonaSkill installs the persona, or a placeholder so the profile's skill list always resolves.
func installPersonaSkill(home string) error {
	body := emptyPersona
	data, err := os.ReadFile(filepath.Join(configDir(home), "persona.md"))
	switch {
	case err == nil:
		body = personaHeader + string(data)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("reading persona: %w", err)
	}

	return writeFile(filepath.Join(home, ".empryo"), "skills/codeforge-persona/SKILL.md", []byte(body), privateFile)
}

func collect(ctx context.Context, r review.Runner, repo string, w *bytes.Buffer) error {
	git := func(args ...string) string {
		out, err := r.Run(ctx, "git", append([]string{"-C", repo}, args...)...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	gh := func(args ...string) string {
		out, err := r.Run(ctx, "gh", args...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}

	email := git("config", "user.email")
	if email == "" {
		return errNoEmail
	}

	name := filepath.Base(git("rev-parse", "--show-toplevel"))
	slug := repoSlug(git("remote", "get-url", "origin"))

	section := func(title, body string) {
		if body != "" {
			fmt.Fprintf(w, "\n## %s: %s\n%s\n", name, title, body)
		}
	}

	section("commit subjects", git("log", "--author="+email, "--no-merges", "-n", "300", "--pretty=%s"))
	section("code you wrote (recent diffs)", truncate(git("log", "--author="+email, "--no-merges", "-n", "25", "-p", "--unified=2", "--pretty=### %s"), maxDiffBytes))
	if slug == "" {
		return nil
	}

	section("your pull requests", gh("pr", "list", "--repo", slug, "--author", "@me", "--state", "all", "--limit", "30", "--json", "title,body"))

	me := gh("api", "user", "--jq", ".login")
	nums := gh("api", "-X", "GET", "search/issues", "-f", "q=is:pr reviewed-by:@me repo:"+slug, "-f", "per_page=10", "--jq", ".items[].number")
	for _, n := range strings.Fields(nums) {
		section("your review comments on PR "+n, gh("api", "repos/"+slug+"/pulls/"+n+"/comments",
			"--jq", `.[] | select(.user.login=="`+me+`") | "- " + .body`))
	}
	return nil
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
