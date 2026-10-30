package src

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/codeliter/codeforge/src/guard"
)

const (
	sourceLearn   = "learn"
	sourceStarter = "starter"
	sourceBlank   = "blank"

	scopeGlobal = "global"
	scopeRepo   = "repo"
)

var errInitUsage = errors.New("usage: codeforge init [--repo] [--persona <name>] [repo...]")

type initAnswers struct {
	scope   string
	source  string
	repos   string
	starter string
	notes   string
}

// runInit sets the persona: interactively on a terminal, or from flags in scripts.
func runInit(ctx context.Context, home, bin string, cfg Config, args []string) error {
	ans, err := initFromArgs(args)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(filepath.Join(configDir(home), "persona.md"))
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("checking persona: %w", statErr)
	}
	if len(args) == 0 && interactive() && statErr != nil {
		if ans, err = askInit(); err != nil {
			return err
		}
	}

	if ans.scope == scopeRepo {
		return initRepo(ctx, home, ans)
	}

	// A second repository reuses the global persona; `init --persona` or naming repos replaces it on purpose.
	if statErr == nil && len(args) == 0 {
		fmt.Fprintln(os.Stderr, "codeforge: persona already set; run `codeforge init --persona NAME` or `codeforge init REPO...` to replace it.")
		reportEnforcement(ctx)
		return nil
	}

	persona, err := buildPersona(ctx, bin, cfg.Init, ans)
	if err != nil {
		return err
	}
	if ans.notes != "" {
		persona = append(persona, []byte("\n## Personal notes\n"+ans.notes+"\n")...)
	}

	p, err := savePersona(home, persona)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "codeforge: global persona written to %s. It applies in every repository.\n", p)
	reportEnforcement(ctx)
	return nil
}

// reportEnforcement tells the user where push and PR rules are now enforced.
func reportEnforcement(ctx context.Context) {
	wd, err := os.Getwd()
	if err != nil {
		return
	}
	if repo, err := guard.RepoRoot(ctx, wd); err == nil && prePushInstalled(ctx, repo) {
		fmt.Fprintf(os.Stderr, "codeforge: git pre-push hook active in %s; only commits that passed `codeforge gate` can be pushed.\n", repo)
	}
}

// initRepo seeds team conventions from a starter or a blank template, never from personal history.
func initRepo(ctx context.Context, home string, ans initAnswers) error {
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	starterName := ans.starter
	if ans.source != sourceStarter {
		starterName = ""
	}
	persona, err := repoPersonaSeed(starterName)
	if err != nil {
		return err
	}
	if ans.notes != "" {
		persona = append(persona, []byte("\n## Team notes\n"+ans.notes+"\n")...)
	}

	p, err := saveRepoPersona(home, root, persona)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "codeforge: repository persona written to %s. Edit it, then commit it to share with your team.\n", p)
	reportEnforcement(ctx)
	return nil
}

func initFromArgs(args []string) (initAnswers, error) {
	ans := initAnswers{scope: scopeGlobal, source: sourceLearn}

	var repos []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--repo":
			ans.scope = scopeRepo
		case args[i] == "--persona" && i+1 < len(args):
			ans.source, ans.starter = sourceStarter, args[i+1]
			i++
		case strings.HasPrefix(args[i], "-"):
			return ans, fmt.Errorf("%w: unknown flag %s", errInitUsage, args[i])
		default:
			repos = append(repos, args[i])
		}
	}

	if ans.scope == scopeRepo && len(repos) > 0 {
		return ans, fmt.Errorf("%w: --repo learns nothing from history; use --persona or edit the file", errInitUsage)
	}
	if ans.scope == scopeRepo && ans.source == sourceLearn {
		ans.source = sourceBlank
	}

	ans.repos = strings.Join(repos, ",")
	if ans.repos == "" {
		ans.repos = "."
	}
	return ans, nil
}

func askInit() (initAnswers, error) {
	ans := initAnswers{scope: scopeGlobal, source: sourceLearn, repos: "."}

	var options []huh.Option[string]
	for _, s := range starters() {
		options = append(options, huh.NewOption(s.Summary, s.Name))
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Which persona are you setting up?").
				Options(
					huh.NewOption("Global: how I work, in every repository", scopeGlobal),
					huh.NewOption("This repository: team conventions that override my global persona here", scopeRepo),
				).
				Value(&ans.scope),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Where should codeforge learn how you work?").
				Options(
					huh.NewOption("From my history in one or more repositories", sourceLearn),
					huh.NewOption("Start from a ready-made persona (new repo or no history)", sourceStarter),
				).
				Value(&ans.source),
		).WithHideFunc(func() bool { return ans.scope != scopeGlobal }),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Start the repository persona from").
				Options(
					huh.NewOption("A blank template I fill in", sourceBlank),
					huh.NewOption("A ready-made persona", sourceStarter),
				).
				Value(&ans.source),
		).WithHideFunc(func() bool { return ans.scope != scopeRepo }),
		huh.NewGroup(
			huh.NewInput().
				Title("Repositories to learn from").
				Description("Comma separated paths. Use another repo you have worked in when this one is new.").
				Value(&ans.repos).
				Validate(validateRepos),
		).WithHideFunc(func() bool { return ans.scope != scopeGlobal || ans.source != sourceLearn }),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Starter persona").
				Options(options...).
				Value(&ans.starter),
		).WithHideFunc(func() bool { return ans.source != sourceStarter }),
		huh.NewGroup(
			huh.NewText().
				Title("Anything codeforge should always know? (optional)").
				Description("For example: ticket format, PR template, words you avoid.").
				Value(&ans.notes),
		),
	)

	if err := form.Run(); err != nil {
		return ans, fmt.Errorf("init form: %w", err)
	}

	// The repo scope has no "learn" option, so an untouched source means the blank template.
	if ans.scope == scopeRepo && ans.source == sourceLearn {
		ans.source = sourceBlank
	}
	return ans, nil
}

func buildPersona(ctx context.Context, bin, model string, ans initAnswers) ([]byte, error) {
	if ans.source == sourceStarter {
		return starterPersona(ans.starter)
	}
	return learnPersona(ctx, bin, model, splitList(ans.repos))
}

func validateRepos(s string) error {
	for _, repo := range splitList(s) {
		if _, err := os.Stat(repo); err != nil {
			return fmt.Errorf("%s: %w", repo, err)
		}
	}
	return nil
}
