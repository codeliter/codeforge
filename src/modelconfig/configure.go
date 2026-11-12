package modelconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"

	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/modelcatalog"

	"github.com/codeliter/codeforge/src/fsutil"
)

var errModelConfigure = errors.New("usage: codeforge models configure [--project|--global]")
var errModelConfigureTerminal = errors.New("model configuration requires a terminal; edit config.yaml or use codeforge models set")
var errModelConfigureCancelled = errors.New("model configuration cancelled; nothing saved")

func modelConfigurationScope(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) != 1 || args[0] != "--project" && args[0] != "--global" {
		return "", errModelConfigure
	}
	return args[0][2:], nil
}

func modelConfigDirectory(home, project, scope string) (string, error) {
	if scope == "global" {
		return config.Dir(home), nil
	}
	if config.SameScope(home, project) {
		return "", fmt.Errorf("%w: this project path is your global configuration; use --global", errModelConfigure)
	}
	return filepath.Dir(config.ProjectPath(project)), nil
}
func runModelConfigure(ctx context.Context, home, bin string, args []string) error {
	scope, err := modelConfigurationScope(args)
	if err != nil {
		return err
	}
	if !forms.Interactive() {
		return errModelConfigureTerminal
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding configuration workspace: %w", err)
	}
	root, gitErr := guard.RepoRoot(ctx, wd)
	if gitErr != nil {
		root = wd
	}
	if scope == "" {
		scope = "global"
		if gitErr == nil {
			scope = "project"
		}
		if scope, err = forms.Choose("Where should these models apply?", "", []forms.Option{{Label: "This project: " + root, Value: "project"}, {Label: "All projects", Value: "global"}}, scope); err != nil {
			return err
		}
	}
	dir, err := modelConfigDirectory(home, root, scope)
	if err != nil {
		return err
	}
	cfg, err := config.ReadFile(home)
	if err != nil {
		return err
	}
	global := config.Clone(cfg)
	var local config.Config
	if scope == "project" {
		local, err = config.ReadTrustedProject(home, root, TrustApprover())
		if err != nil {
			return err
		}
		if err := config.EnsureProjectEditing(home, root, func() bool {
			return forms.Agreed("Replace the unapproved project model file with new choices? It is kept if you cancel.")
		}); err != nil {
			return err
		}
		cfg = config.Merge(cfg, local)
	}
	fmt.Fprintln(os.Stderr, "codeforge: loading the connected-provider model catalog...")
	catalog, err := modelcatalog.Connected(ctx, bin)
	if err != nil {
		return err
	}
	if len(catalog) == 0 {
		return config.ErrNoModels
	}
	baseline := config.Defaults(config.Clone(cfg))
	cfg = config.Clone(baseline)
	editor := editModelChoices
	var approvers []func(string, string) error
	if scope == "project" {
		cfg.Routing = config.Clone(local).Routing
		editor = func(edited *config.Config, catalog []string) error {
			if err := editModelChoicesPreview(edited, catalog, func(choices *config.Config) (config.Config, error) {
				return prepareProjectModelPreview(global, local, baseline, choices, catalog)
			}); err != nil {
				return err
			}
			if fsutil.Symlinked(root, filepath.Join(".codeforge", "config.yaml")) {
				return config.ErrConfigSymlink
			}
			*edited = projectModelDelta(local, baseline, *edited)
			return nil
		}
		approvers = append(approvers, func(_ string, content string) error { return config.Trust(home, config.ProjectPath(root), content) })
	}
	if err := configureModelFile(dir, cfg, catalog, editor, approvers...); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Saved %s. New Codeforge runs use these models; Empryo's own configuration was not changed.\n", filepath.Join(dir, "config.yaml"))
	return nil
}

// The editor works on a copy. Cancellation or invalid choices never replace the file.
func configureModelFile(dir string, cfg config.Config, catalog []string, edit func(*config.Config, []string) error, approve ...func(string, string) error) error {
	cfg = config.Clone(cfg)
	if err := edit(&cfg, catalog); err != nil {
		return err
	}
	content, err := config.SaveYAML(dir, cfg)
	if err != nil {
		return err
	}
	for _, record := range approve {
		if err := record(filepath.Join(dir, "config.yaml"), content); err != nil {
			return err
		}
	}
	return nil
}

func modelOptions(catalog []string, saved ...string) []forms.Option {
	ids := slices.Clone(catalog)
	sort.Strings(ids)
	ids = slices.Compact(ids)
	var options []forms.Option
	for _, id := range ids {
		if !config.SuggestedModel(id) {
			continue
		}
		options = append(options, forms.Option{Label: id, Value: id})
	}
	for _, id := range saved {
		if id != "" && config.SuggestedModel(id) && config.ValidModelID(id) && !slices.Contains(ids, id) {
			options = append(options, forms.Option{Label: id + " (saved/default; not currently listed)", Value: id})
			ids = append(ids, id)
		}
	}
	return options
}

func seedModelChoices(cfg *config.Config, catalog []string) {
	for _, role := range config.Roles {
		ids := slices.DeleteFunc(slices.Clone(config.Current(*cfg, role.Key)), func(id string) bool { return !config.SuggestedModel(id) })
		config.AssignRole(cfg, role.Key, ids)
	}
	if cfg.Model == "" {
		cfg.Model = catalog[0]
		if selected := recommendConfigured(catalog, *cfg, false)["model"]; len(selected) > 0 {
			cfg.Model = selected[0]
		}
	}
	for key, models := range recommendConfigured(catalog, *cfg, true) {
		if len(config.Current(*cfg, key)) == 0 && len(models) > 0 {
			config.AssignRole(cfg, key, models)
		}
	}
	if cfg.Expert == "" {
		cfg.Expert = config.DefaultExpertModel
	}
	if cfg.Init == "" {
		cfg.Init = cfg.Model
	}
	if len(cfg.Judges) == 0 {
		cfg.Judges = []string{catalog[0]}
	}
	for _, id := range catalog {
		if cfg.Review.Model == "" && id != cfg.Model {
			cfg.Review.Model = id
			continue
		}
		if len(cfg.Review.SecondOpinion) == 0 && id != cfg.Model && id != cfg.Review.Model {
			cfg.Review.SecondOpinion = []string{id}
		}
	}
	if cfg.Review.Model == "" {
		cfg.Review.Model = config.DefaultReviewModel
		if cfg.Review.Model == cfg.Model {
			cfg.Review.Model = config.DefaultModel
		}
	}
	if len(cfg.Review.SecondOpinion) == 0 {
		for _, id := range append(slices.Clone(config.DefaultSecondOpinion), config.DefaultModel, config.DefaultReviewModel) {
			if id != cfg.Model && id != cfg.Review.Model {
				cfg.Review.SecondOpinion = []string{id}
				break
			}
		}
	}
}

func editModelChoices(cfg *config.Config, catalog []string) error {
	return editModelChoicesPreview(cfg, catalog, nil)
}
func editModelChoicesPreview(cfg *config.Config, catalog []string, prepare func(*config.Config) (config.Config, error)) error {
	if err := pickProviderPolicy(cfg, catalog); err != nil {
		return err
	}
	seedModelChoices(cfg, catalog)
	noModels := config.ErrNoModels.Error()
	answers, err := forms.Run(forms.Page("configure models",
		forms.Field{Key: "model", Kind: forms.Select, Title: "Coding model", Description: "Writes code in work, chat and ticket mode.", Options: providerModelOptions(catalog, cfg.Providers, cfg.Model), Value: cfg.Model},
		forms.Field{Key: "expert", Kind: forms.Select, Title: "Expert delegate (on demand)", Description: "Use Opus for difficult delegated work, not routine calls.", Options: providerModelOptions(catalog, cfg.Providers, cfg.Expert), Value: cfg.Expert},
		forms.Field{Key: "judges", Kind: forms.MultiSelect, Title: "Task sizing models", Description: "Fast helpers that choose how much thinking a task needs.", Options: providerModelOptions(catalog, cfg.Providers, cfg.Judges...), Values: cfg.Judges, Required: noModels},
		forms.Field{Key: "review", Kind: forms.Select, Title: "Review model", Description: "Checks changes with a different model from the coder.", Options: providerModelOptions(catalog, cfg.Providers, cfg.Review.Model), Value: cfg.Review.Model,
			Differs: []forms.Differ{{Key: "model", Message: config.ErrReviewerIsCoder.Error()}}},
		forms.Field{Key: "second", Kind: forms.MultiSelect, Title: "Extra reviewers", Description: "Second opinions for larger or riskier changes.", Options: providerModelOptions(catalog, cfg.Providers, cfg.Review.SecondOpinion...), Values: cfg.Review.SecondOpinion, Required: noModels,
			Differs: []forms.Differ{{Key: "model", Message: config.ErrSecondOpinionIsCoder.Error()}, {Key: "review", Message: config.ErrSecondOpinionIsReviewer.Error()}}},
		forms.Field{Key: "init", Kind: forms.Select, Title: "Persona model", Description: "Learns your conventions during init.", Options: providerModelOptions(catalog, cfg.Providers, cfg.Init), Value: cfg.Init},
		forms.Field{Key: "helpers", Kind: forms.Confirm, Title: "Also configure Empryo helper routing?", Description: "Only Codeforge profiles change; other Empryo sessions keep their routing."},
	))
	if err != nil {
		return err
	}
	cfg.Model, cfg.Expert, cfg.Judges = answers.String("model"), answers.String("expert"), answers.Strings("judges")
	cfg.Review.Model, cfg.Review.SecondOpinion, cfg.Init = answers.String("review"), answers.Strings("second"), answers.String("init")
	if answers.Bool("helpers") {
		values := make([]string, len(config.RoutingJobs))
		var routingFields []forms.Field
		for i, job := range config.RoutingJobs {
			values[i] = routingPickerValue(cfg.Routing, job)
			if !config.SuggestedModel(values[i]) {
				values[i] = inheritRouting
			}
			options := append([]forms.Option{{Label: "Inherit global Codeforge / existing routing", Value: inheritRouting}, {Label: "Use native Empryo routing (reset)", Value: ""}}, providerModelOptions(catalog, cfg.Providers, values[i])...)
			routingFields = append(routingFields, forms.Field{Key: job, Kind: forms.Select, Title: "Helper: " + job, Options: options, Value: values[i]})
		}
		routing, err := forms.Run(forms.Page("configure helper models", routingFields...))
		if err != nil {
			return err
		}
		for i, job := range config.RoutingJobs {
			values[i] = routing.String(job)
		}
		applyRoutingChoices(cfg, config.RoutingJobs, values)
	}
	if err := config.Validate(*cfg); err != nil {
		return err
	}
	preview := *cfg
	if prepare != nil {
		prepared, err := prepare(cfg)
		if err != nil {
			return err
		}
		preview = prepared
	}
	plan, err := config.ResolvePlan(preview, catalog)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, modelPlanPreview(plan))
	save, err := forms.YesNo("Save these model choices?", "")
	if err != nil {
		return err
	}
	if !save {
		return errModelConfigureCancelled
	}
	return nil
}
