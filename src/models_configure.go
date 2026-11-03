package src

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/charmbracelet/huh"

	"github.com/codeliter/codeforge/src/guard"
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
		return configDir(home), nil
	}
	if sameConfigScope(home, project) {
		return "", fmt.Errorf("%w: this project path is your global configuration; use --global", errModelConfigure)
	}
	return filepath.Dir(projectConfigPath(project)), nil
}
func runModelConfigure(ctx context.Context, home, bin string, args []string) error {
	scope, err := modelConfigurationScope(args)
	if err != nil {
		return err
	}
	if !interactive() {
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
		if err := runForm("model configuration scope", huh.NewSelect[string]().Title("Where should these models apply?").Options(huh.NewOption("This project: "+root, "project"), huh.NewOption("All projects", "global")).Value(&scope)); err != nil {
			return err
		}
	}
	dir, err := modelConfigDirectory(home, root, scope)
	if err != nil {
		return err
	}
	cfg, err := readConfigFile(home)
	if err != nil {
		return err
	}
	var local Config
	if scope == "project" {
		local, err = readTrustedProjectConfig(home, root)
		if err != nil {
			return err
		}
		if err := ensureProjectModelEditing(home, root, func() bool {
			return confirm("Replace the unapproved project model file with new choices? It is kept if you cancel.")
		}); err != nil {
			return err
		}
		cfg = mergeConfig(cfg, local)
	}
	fmt.Fprintln(os.Stderr, "codeforge: loading the connected-provider model catalog...")
	catalog, err := availableModels(ctx, bin)
	if err != nil {
		return err
	}
	if len(catalog) == 0 {
		return errNoModels
	}
	baseline := configDefaults(cloneConfig(cfg))
	cfg = cloneConfig(baseline)
	editor := editModelChoices
	var approvers []func(string, string) error
	if scope == "project" {
		cfg.Routing = cloneConfig(local).Routing
		editor = func(edited *Config, catalog []string) error {
			if err := editModelChoices(edited, catalog); err != nil {
				return err
			}
			if isSymlinked(root, filepath.Join(".codeforge", "config.yaml")) {
				return errConfigSymlink
			}
			*edited = projectModelDelta(local, baseline, *edited)
			return nil
		}
		approvers = append(approvers, func(_ string, content string) error { return trust(home, projectConfigPath(root), content) })
	}
	if err := configureModelFile(dir, cfg, catalog, editor, approvers...); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Saved %s. New Codeforge runs use these models; Empryo's own configuration was not changed.\n", filepath.Join(dir, "config.yaml"))
	return nil
}

// The editor works on a copy. Cancellation or invalid choices never replace the file.
func configureModelFile(dir string, cfg Config, catalog []string, edit func(*Config, []string) error, approve ...func(string, string) error) error {
	cfg = cloneConfig(cfg)
	if err := edit(&cfg, catalog); err != nil {
		return err
	}
	content, err := saveConfigYAML(dir, cfg)
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

func modelOptions(catalog []string, saved ...string) []huh.Option[string] {
	ids := slices.Clone(catalog)
	sort.Strings(ids)
	ids = slices.Compact(ids)
	var options []huh.Option[string]
	for _, id := range ids {
		if !suggestedModel(id) {
			continue
		}
		options = append(options, huh.NewOption(id, id))
	}
	for _, id := range saved {
		if id != "" && suggestedModel(id) && validModelID(id) && !slices.Contains(ids, id) {
			options = append(options, huh.NewOption(id+" (saved/default; not currently listed)", id))
			ids = append(ids, id)
		}
	}
	return options
}

func seedModelChoices(cfg *Config, catalog []string) {
	for _, role := range roles {
		ids := slices.DeleteFunc(slices.Clone(current(*cfg, role.key)), func(id string) bool { return !suggestedModel(id) })
		assignModelRole(cfg, role.key, ids)
	}
	if cfg.Model == "" {
		cfg.Model = catalog[0]
		if selected := recommend(catalog)["model"]; len(selected) > 0 {
			cfg.Model = selected[0]
		}
	}
	for key, models := range recommendKeeping(catalog, *cfg) {
		if len(current(*cfg, key)) == 0 && len(models) > 0 {
			assignModelRole(cfg, key, models)
		}
	}
	if cfg.Expert == "" {
		cfg.Expert = defaultExpertModel
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
		cfg.Review.Model = defaultReviewModel
		if cfg.Review.Model == cfg.Model {
			cfg.Review.Model = defaultModel
		}
	}
	if len(cfg.Review.SecondOpinion) == 0 {
		for _, id := range append(slices.Clone(defaultSecondOpinion), defaultModel, defaultReviewModel) {
			if id != cfg.Model && id != cfg.Review.Model {
				cfg.Review.SecondOpinion = []string{id}
				break
			}
		}
	}
}

func editModelChoices(cfg *Config, catalog []string) error {
	seedModelChoices(cfg, catalog)
	helpers, save := false, false
	fields := []huh.Field{
		huh.NewSelect[string]().Title("Coding model").Description("Writes code in work, chat and ticket mode.").Options(modelOptions(catalog, cfg.Model)...).Value(&cfg.Model),
		huh.NewSelect[string]().Title("Expert delegate (on demand)").Description("Use Opus for difficult delegated work, not routine calls.").Options(modelOptions(catalog, cfg.Expert)...).Value(&cfg.Expert),
		huh.NewMultiSelect[string]().Title("Task sizing models").Description("Fast helpers that choose how much thinking a task needs.").Options(modelOptions(catalog, cfg.Judges...)...).Value(&cfg.Judges).Validate(func(ids []string) error {
			if len(ids) == 0 {
				return errNoModels
			}
			return nil
		}),
		huh.NewSelect[string]().Title("Review model").Description("Checks changes with a different model from the coder.").Options(modelOptions(catalog, cfg.Review.Model)...).Value(&cfg.Review.Model).Validate(func(id string) error {
			if id == cfg.Model {
				return errReviewerIsCoder
			}
			return nil
		}),
		huh.NewMultiSelect[string]().Title("Extra reviewers").Description("Second opinions for larger or riskier changes.").Options(modelOptions(catalog, cfg.Review.SecondOpinion...)...).Value(&cfg.Review.SecondOpinion).Validate(func(ids []string) error {
			if len(ids) == 0 {
				return errNoModels
			}
			for _, id := range ids {
				if id == cfg.Model {
					return errSecondOpinionIsCoder
				}
				if id == cfg.Review.Model {
					return errSecondOpinionIsReviewer
				}
			}
			return nil
		}),
		huh.NewSelect[string]().Title("Persona model").Description("Learns your conventions during init.").Options(modelOptions(catalog, cfg.Init)...).Value(&cfg.Init),
		huh.NewConfirm().Title("Also configure Empryo helper routing?").Description("Only Codeforge profiles change; other Empryo sessions keep their routing.").Value(&helpers),
	}
	if err := runForm("configure models", fields...); err != nil {
		return err
	}
	if helpers {
		values := make([]string, len(routingJobs))
		var routingFields []huh.Field
		for i, job := range routingJobs {
			values[i] = routingPickerValue(cfg.Routing, job)
			if !suggestedModel(values[i]) {
				values[i] = inheritRouting
			}
			options := append([]huh.Option[string]{huh.NewOption("Inherit global Codeforge / existing routing", inheritRouting), huh.NewOption("Use native Empryo routing (reset)", "")}, modelOptions(catalog, values[i])...)
			routingFields = append(routingFields, huh.NewSelect[string]().Title("Helper: "+job).Options(options...).Value(&values[i]))
		}
		if err := runForm("configure helper models", routingFields...); err != nil {
			return err
		}
		applyRoutingChoices(cfg, routingJobs, values)
	}
	if err := validate(*cfg); err != nil {
		return err
	}
	if err := runForm("save models", huh.NewConfirm().Title("Save these model choices?").Value(&save)); err != nil {
		return err
	}
	if !save {
		return errModelConfigureCancelled
	}
	return nil
}
