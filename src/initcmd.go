package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/codeliter/codeforge/src/config"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/guard"
)

const (
	sourceLearn   = "learn"
	sourceStarter = "starter"
	sourceBlank   = "blank"

	scopeAuto   = "auto"
	scopeGlobal = "global"
	scopeRepo   = "repo"
)

var errInitUsage = errors.New("usage: codeforge init [--repo|--global] [--persona [name]] [repo...]")

type initAnswers struct {
	scope   string
	source  string
	repos   string
	starter string
	notes   string
}

// runInit sets the persona: interactively on a terminal, or from flags in scripts.
func runInit(ctx context.Context, home, bin string, cfg config.Config, args []string) error {
	ans, err := initFromArgs(args)
	if err != nil {
		return err
	}
	if ans.source == sourceStarter {
		ans.starter, err = resolveStarter(ans.starter, pickStarter)
		if err != nil {
			return err
		}
	}
	if ans.scope == scopeAuto {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("finding current directory: %w", err)
		}
		ans.scope = scopeGlobal
		if _, err := guard.RepoRoot(ctx, wd); err == nil {
			ans.scope = scopeRepo
		}
	}
	if ans.scope == scopeGlobal {
		existing, err := readPersona(home)
		if err != nil {
			return fmt.Errorf("checking persona: %w", err)
		}
		if len(args) == 0 && existing != nil {
			fmt.Fprintln(os.Stderr, "codeforge: outside a Git repository; keeping your global persona. Use `codeforge init --global` to replace it.")
			return nil
		}
		if len(args) == 0 && interactive() && existing == nil {
			if ans, err = askInit(); err != nil {
				return err
			}
		}
	}

	if ans.scope == scopeRepo {
		return initRepo(ctx, home, bin, cfg.Init, ans)
	}

	if ans.source == sourceLearn {
		ans.repos = strings.Join(learnFrom(home, config.SplitList(ans.repos)), ",")
	}
	before, err := personaSnapshot(home)
	if err != nil {
		return err
	}
	persona, err := buildPersona(ctx, home, bin, cfg.Init, ans)
	if err != nil {
		return err
	}
	if ans.notes != "" {
		persona = append(persona, []byte("\n## Personal notes\n"+ans.notes+"\n")...)
	}
	fitted, err := reconcilePersona(home, before, persona)
	if err != nil {
		return err
	}
	if fitted.keep {
		fmt.Fprintln(os.Stderr, "codeforge: your persona is unchanged.")
		return nil
	}

	p, err := commitPersona(ctx, home, fitted, func() error {
		if ans.source == sourceStarter {
			// Sections learned earlier that the starter did not replace still come from where they were learned, so
			// that record stands and a later update revisits those repositories.
			if old, known := globalPersonaSource(home, nil); known && old.Kind == sourceKindLearned && !fitted.fresh {
				return nil
			}
			return savePersonaSource(home, personaSource{Kind: sourceKindStarter, Starter: ans.starter})
		}
		return recordLearned(home, absolutePaths(config.SplitList(ans.repos)), cfg.Init, fitted.fresh)
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "codeforge: global persona written to %s. It applies in every repository.\n", p)
	return nil
}

// initRepo learns this repository's conventions without changing the global persona.
func initRepo(ctx context.Context, home, bin, model string, ans initAnswers) error {
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}
	if existing, err := loadRepoPersona(root); err != nil {
		return err
	} else if existing != "" && !unfinishedRepoPersona([]byte(existing)) {
		fmt.Fprintf(os.Stderr, "codeforge: repository persona already exists at %s; left unchanged.\n", sharedPersonaPath(root))
		return nil
	}
	if existing, err := loadPrivateRepoPersona(ctx, home, root); err != nil {
		return err
	} else if existing != "" && !unfinishedRepoPersona([]byte(existing)) {
		fmt.Fprintf(os.Stderr, "codeforge: you already have a persona for this repository at %s; left unchanged.\n", privatePersonaWhere(ctx, home, root))
		return nil
	}
	fmt.Fprintf(os.Stderr, "codeforge: setting up the persona for %s; your global persona is unchanged.\n", root)
	var persona []byte
	if ans.source == sourceBlank {
		persona, err = repoPersonaSeed("")
	} else {
		persona, err = buildPersona(ctx, home, bin, model, ans)
	}
	if err != nil {
		return err
	}
	if ans.notes != "" {
		persona = append(persona, []byte("\n## Team notes\n"+ans.notes+"\n")...)
	}

	p, err := saveRepoPersona(ctx, home, root, persona)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "codeforge: persona for this repository written to %s. It is yours alone and nothing was added to the repository; codeforge persona share puts it there for your team.\n", p)
	return nil
}

func initFromArgs(args []string) (initAnswers, error) {
	ans := initAnswers{scope: scopeAuto, source: sourceLearn}

	var repos []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--repo" || args[i] == "--global":
			scope := scopeRepo
			if args[i] == "--global" {
				scope = scopeGlobal
			}
			if ans.scope != scopeAuto && ans.scope != scope {
				return ans, fmt.Errorf("%w: --repo and --global conflict", errInitUsage)
			}
			ans.scope = scope
		case args[i] == "--persona":
			ans.source, ans.starter = sourceStarter, ""
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				ans.starter = args[i+1]
				i++
			}
		case strings.HasPrefix(args[i], "-"):
			return ans, fmt.Errorf("%w: unknown flag %s", errInitUsage, args[i])
		default:
			repos = append(repos, args[i])
		}
	}

	if ans.scope == scopeRepo && len(repos) > 0 {
		return ans, fmt.Errorf("%w: --repo uses the current repository; use --global REPO... to learn from other repositories", errInitUsage)
	}
	if ans.scope == scopeAuto && len(repos) > 0 {
		ans.scope = scopeGlobal
	}

	ans.repos = strings.Join(repos, ",")
	if ans.repos == "" {
		ans.repos = "."
	}
	return ans, nil
}

func askInit() (initAnswers, error) {
	ans := initAnswers{scope: scopeGlobal, source: sourceLearn, repos: "."}
	// Two pages ask for the source, one per scope, so each offers only what makes sense there; both answer "source".
	answers, err := forms.Run(forms.Form{Name: "init form", Groups: []forms.Group{
		{Fields: []forms.Field{{Key: "scope", Kind: forms.Select, Title: "Which persona are you setting up?", Value: ans.scope, Options: []forms.Option{
			{Label: "Global: how I work, in every repository", Value: scopeGlobal},
			{Label: "This repository: team conventions that override my global persona here", Value: scopeRepo},
		}}}},
		{When: &forms.When{Key: "scope", Is: []string{scopeGlobal}}, Fields: []forms.Field{{Key: "source", Kind: forms.Select, Title: "Where should codeforge learn how you work?", Value: sourceLearn, Options: []forms.Option{
			{Label: "From my history in one or more repositories", Value: sourceLearn},
			{Label: "Start from a ready-made persona (new repo or no history)", Value: sourceStarter},
		}}}},
		{When: &forms.When{Key: "scope", Is: []string{scopeRepo}}, Fields: []forms.Field{{Key: "source", Kind: forms.Select, Title: "Start the repository persona from", Options: []forms.Option{
			{Label: "A blank template I fill in", Value: sourceBlank},
			{Label: "A ready-made persona", Value: sourceStarter},
		}}}},
		{When: &forms.When{Key: "scope", Is: []string{scopeGlobal}, And: &forms.When{Key: "source", Is: []string{sourceLearn}}}, Fields: []forms.Field{{Key: "repos", Kind: forms.Input, Title: "Repositories to learn from", Value: ans.repos, Check: forms.CheckPaths,
			Description: "Comma separated paths. Use another repo you have worked in when this one is new."}}},
		{When: &forms.When{Key: "source", Is: []string{sourceStarter}}, Fields: []forms.Field{{Key: "starter", Kind: forms.Select, Title: "Starter persona", Options: starterOptions(starters())}}},
		{Fields: []forms.Field{{Key: "notes", Kind: forms.Text, Title: "Anything codeforge should always know? (optional)",
			Description: "For example: ticket format, PR template, words you avoid."}}},
	}})
	if err != nil {
		return ans, err
	}
	ans.scope, ans.source = answers.String("scope"), answers.String("source")
	ans.repos, ans.starter, ans.notes = answers.String("repos"), answers.String("starter"), answers.String("notes")
	// The repo scope has no "learn" option, so an untouched source means the blank template.
	if ans.scope == scopeRepo && ans.source == sourceLearn {
		ans.source = sourceBlank
	}
	return ans, nil
}

func buildPersona(ctx context.Context, home, bin, model string, ans initAnswers) ([]byte, error) {
	if ans.source == sourceStarter {
		return starterPersona(ans.starter)
	}
	return learnPersona(ctx, home, bin, model, config.SplitList(ans.repos))
}
