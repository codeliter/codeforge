package src

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/codeliter/codeforge/src/guard"
)

var errModelApplyUsage = errors.New("usage: codeforge models apply [--recommended] [--project|--global]")
var errModelApplyTerminal = errors.New("models apply requires a terminal to choose scope and confirm native settings changes; run it yourself")
var errModelApplyCancelled = errors.New("model application cancelled; native settings were not changed")
var errNativeConfigChanged = errors.New("native fallback settings changed since the preview; nothing was written, run models apply again")
var errNativeConfigApply = errors.New("cannot apply native fallback settings")

func parseModelApplyScope(args []string) (string, error) {
	scope, _, err := parseModelApplyArgs(args)
	return scope, err
}

func parseModelApplyArgs(args []string) (scope string, recommended bool, err error) {
	for _, arg := range args {
		switch arg {
		case "--recommended":
			if recommended {
				return "", false, errModelApplyUsage
			}
			recommended = true
		case "--project", "--global":
			if scope != "" {
				return "", false, errModelApplyUsage
			}
			scope = strings.TrimPrefix(arg, "--")
		default:
			return "", false, errModelApplyUsage
		}
	}
	return scope, recommended, nil
}

func defaultModelApplyScope(inProject bool) string {
	if inProject {
		return "project"
	}
	return "global"
}
func readModelApplyConfig(home, project, scope string) (Config, error) {
	cfg, err := readConfigFile(home)
	if err != nil {
		return cfg, err
	}
	if scope == "project" {
		local, err := readTrustedProjectConfig(home, project)
		if err != nil {
			return cfg, err
		}
		cfg = mergeConfig(cfg, local)
	}
	cfg = configDefaults(cfg)
	return cfg, validate(cfg)
}

func runModelApply(ctx context.Context, home, bin string, args []string) error {
	scope, recommended, err := parseModelApplyArgs(args)
	if err != nil {
		return err
	}
	if !interactive() {
		return errModelApplyTerminal
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding model application workspace: %w", err)
	}
	root, gitErr := guard.RepoRoot(ctx, wd)
	if gitErr != nil {
		root = wd
	}
	if scope == "" {
		_, projectErr := os.Stat(projectConfigPath(root))
		scope = defaultModelApplyScope(gitErr == nil || projectErr == nil)
		if sameConfigScope(home, root) {
			scope = "global"
		}
		options := []huh.Option[string]{huh.NewOption("Global defaults (projects can override)", "global")}
		if !sameConfigScope(home, root) {
			options = append(options, huh.NewOption("This project: "+root, "project"))
		}
		if err := runForm("apply model settings scope", huh.NewSelect[string]().Title("Where should these model settings apply?").Description(applyScopeDescription(recommended)).Options(options...).Value(&scope)); err != nil {
			return err
		}
	}
	if _, err := modelConfigDirectory(home, root, scope); err != nil {
		return err
	}
	cfg, err := readModelApplyConfig(home, root, scope)
	if err != nil {
		return err
	}
	workspace := root
	if scope == "global" {
		// Isolate global writes: the native API clears the current workspace override.
		workspace, err = os.MkdirTemp("", "codeforge-models-global-")
		if err != nil {
			return fmt.Errorf("creating global configuration workspace: %w", err)
		}
		defer os.RemoveAll(workspace)
	}
	var catalog []string
	if recommended {
		fmt.Fprintln(os.Stderr, "codeforge: loading the connected-provider model catalog...")
		catalog, err = connectedModelCatalogAt(ctx, bin, workspace)
		if err != nil {
			return err
		}
		cfg, err = saveRecommendedModels(home, root, scope, cfg, catalog, confirmRecommendedModels)
		if err != nil {
			return err
		}
	}
	if catalog == nil && needsModelCatalog(cfg) {
		fmt.Fprintln(os.Stderr, "codeforge: resolving saved model choices through connected providers...")
		catalog, err = connectedModelCatalogAt(ctx, bin, workspace)
		if err != nil {
			return err
		}
	}
	plan, err := resolveModelPlan(cfg, catalog)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Applying %s Codeforge settings (one-off environment overrides are excluded).\n", scope)
	printModelPlan(os.Stdout, plan)
	if len(plan.fallbackChains()) == 0 {
		fmt.Fprintln(os.Stdout, "No Codeforge fallback chains are declared. Saved role choices already apply on the next launch; nothing was written.")
		return nil
	}
	target := filepath.Join(home, ".empryo", "config.json")
	if scope == "project" {
		target = filepath.Join(root, ".empryo", "config.json")
	}
	return applyModelPlan(ctx, bin, workspace, scope, plan, func(preview string) bool {
		description := fmt.Sprintf("Native target: %q\n%s", target, preview)
		fmt.Fprintln(os.Stderr, description)
		approved := false
		if err := runForm("confirm native model settings", huh.NewConfirm().Title("Apply these fallback changes to "+scope+" Empryo settings?").Description(description).Value(&approved)); err != nil {
			return false
		}
		return approved
	})
}
func applyScopeDescription(recommended bool) string {
	if recommended {
		return "Saves recommended roles after confirmation, then applies any declared native fallback."
	}
	return "Applies saved choices, not model recommendations. Native fallback changes require confirmation."
}

// recommendedModelConfig replaces every role with a recommendation where one exists.
func recommendedModelConfig(cfg Config, catalog []string) (Config, map[string][]string, error) {
	next := cloneConfig(cfg)
	rec := recommendConfigured(catalog, next, false)
	for _, r := range roles {
		if len(rec[r.key]) > 0 {
			assignModelRole(&next, r.key, slices.Clone(rec[r.key]))
		}
	}
	if err := validate(next); err != nil {
		return cfg, rec, err
	}
	if _, err := resolveModelPlan(next, catalog); err != nil {
		return cfg, rec, err
	}
	return next, rec, nil
}

func recommendedModelsPreview(before, after Config) (string, bool) {
	var text strings.Builder
	changed := false
	fmt.Fprintln(&text, "Recommended role changes:")
	for _, r := range roles {
		old, next := current(before, r.key), current(after, r.key)
		if slices.Equal(old, next) {
			continue
		}
		changed = true
		fmt.Fprintf(&text, "  %s: %s -> %s\n", r.key, displayModels(old), displayModels(next))
	}
	return text.String(), changed
}

func displayModels(ids []string) string {
	if len(ids) == 0 {
		return "(default)"
	}
	return strings.Join(ids, ", ")
}

func confirmRecommendedModels(scope, preview string) bool {
	fmt.Fprintln(os.Stderr, preview)
	ok := false
	if err := runForm("confirm recommended models", huh.NewConfirm().Title("Save these recommended models to "+scope+" Codeforge settings?").Description(preview).Value(&ok)); err != nil {
		return false
	}
	return ok
}

func saveRecommendedModels(home, root, scope string, cfg Config, catalog []string, approve func(string, string) bool) (Config, error) {
	next, _, err := recommendedModelConfig(cfg, catalog)
	if err != nil {
		return cfg, err
	}
	preview, changed := recommendedModelsPreview(cfg, next)
	if !changed {
		fmt.Fprintln(os.Stdout, "Saved roles already match the recommendations; Codeforge settings were not changed.")
		return cfg, nil
	}
	if !approve(scope, preview) {
		return cfg, errModelApplyCancelled
	}
	if scope == "global" {
		global, err := readConfigFile(home)
		if err != nil {
			return cfg, err
		}
		for _, r := range roles {
			assignModelRole(&global, r.key, slices.Clone(current(next, r.key)))
		}
		if err := writeConfigFile(home, global); err != nil {
			return cfg, err
		}
		fmt.Fprintf(os.Stdout, "Saved recommended models to %s.\n", filepath.Join(configDir(home), "config.yaml"))
		return next, nil
	}
	if err := ensureProjectModelEditing(home, root, func() bool {
		return confirm("Replace the unapproved project model file with recommended choices? It is kept if you cancel.")
	}); err != nil {
		return cfg, err
	}
	if isSymlinked(root, filepath.Join(".codeforge", "config.yaml")) {
		return cfg, errConfigSymlink
	}
	local, err := readTrustedProjectConfig(home, root)
	if err != nil {
		return cfg, err
	}
	delta := projectModelDelta(local, cfg, next)
	// Recommendations change roles only; inherited helper routing must stay inherited.
	delta.Routing = cloneConfig(local).Routing
	dir := filepath.Dir(projectConfigPath(root))
	if err := configureModelFile(dir, delta, catalog, func(*Config, []string) error { return nil }, func(_ string, content string) error {
		return trust(home, projectConfigPath(root), content)
	}); err != nil {
		return cfg, err
	}
	fmt.Fprintf(os.Stdout, "Saved and approved recommended models in %s.\n", projectConfigPath(root))
	return next, nil
}

func sameFallbackMap(a, b map[string][]string) bool {
	return maps.EqualFunc(a, b, slices.Equal[[]string])
}

func fallbackTarget(snapshot *nativeFallbackSnapshot, scope string) map[string][]string {
	if scope == "global" {
		return snapshot.Global
	}
	return snapshot.Project
}

func plannedNativeFallbacks(plan ModelPlan, snapshot *nativeFallbackSnapshot, scope string) (map[string][]string, error) {
	desired := maps.Clone(fallbackTarget(snapshot, scope))
	if scope == "project" {
		// Native project modelFallback replaces the entire global map, not each row.
		// Retain currently effective unrelated rows when creating a project override.
		desired = maps.Clone(snapshot.Effective)
		if desired == nil {
			desired = make(map[string][]string)
		}
		maps.Copy(desired, snapshot.Project)
	}
	if desired == nil {
		desired = make(map[string][]string)
	}
	for primary, backups := range plan.fallbackChains() {
		desired[primary] = slices.Clone(backups)
	}
	for primary, backups := range plan.fallbackChains() {
		for _, further := range desired[backups[0]] {
			if further != primary {
				desired[backups[0]] = []string{}
				break
			}
		}
	}
	return desired, plan.verifyFallbacks(desired)
}

func nativeFallbackPreview(before, after map[string][]string) string {
	var keys []string
	for key := range after {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var text strings.Builder
	fmt.Fprintln(&text, "Native modelFallback changes:")
	for _, key := range keys {
		old, exists := before[key]
		if exists && slices.Equal(old, after[key]) {
			continue
		}
		previous := "(not set)"
		if exists {
			previous = fmt.Sprintf("%q", old)
		}
		fmt.Fprintf(&text, "  %q: %s -> %q\n", key, previous, after[key])
	}
	fmt.Fprintln(&text, "Unrelated entries and other settings are retained. Empty [] disables an undeclared backup hop.")
	return text.String()
}

func applyModelPlan(ctx context.Context, bin, workspace, scope string, plan ModelPlan, approve func(string) bool) error {
	if scope != "global" && scope != "project" {
		return errModelApplyUsage
	}
	if len(plan.fallbackChains()) == 0 {
		return nil
	}
	snapshot, err := readNativeFallbacks(ctx, bin, workspace)
	if err != nil {
		return err
	}
	desired, err := plannedNativeFallbacks(plan, snapshot, scope)
	if err != nil {
		return err
	}
	if sameFallbackMap(fallbackTarget(snapshot, scope), desired) {
		if err := plan.verifyFallbacks(snapshot.Effective); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Native fallback settings already match; nothing was written.")
		return nil
	}
	if !approve(nativeFallbackPreview(fallbackTarget(snapshot, scope), desired)) {
		return errModelApplyCancelled
	}
	if err := writeNativeFallbacks(ctx, bin, workspace, scope, snapshot, desired); err != nil {
		return err
	}
	if err := verifyModelPlan(ctx, bin, workspace, plan); err != nil {
		return fmt.Errorf("native settings were written but verification failed: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Applied and verified %s native fallback settings. Saved Codeforge roles were not replaced. New sessions use these settings.\n", scope)
	return nil
}

func writeNativeFallbacks(ctx context.Context, bin, workspace, scope string, before *nativeFallbackSnapshot, desired map[string][]string) error {
	attempted := false
	err := withNativeConfig(ctx, bin, workspace, func(request nativeConfigRequest, session string) error {
		current, err := readNativeFallbackSnapshot(request, session)
		if err != nil {
			return err
		}
		if !sameFallbackMap(before.Global, current.Global) || !sameFallbackMap(before.Project, current.Project) || !sameFallbackMap(before.Effective, current.Effective) {
			return errNativeConfigChanged
		}
		attempted = true
		if err := request("_empryo/config/set", map[string]any{"sessionId": session, "scope": scope, "key": "modelFallback", "value": desired}, nil); err != nil {
			return err
		}
		after, err := readNativeFallbackSnapshot(request, session)
		if err != nil {
			return err
		}
		if !sameFallbackMap(desired, fallbackTarget(after, scope)) {
			return errNativeConfigApply
		}
		other := "global"
		if scope == "global" {
			other = "project"
		}
		if !sameFallbackMap(fallbackTarget(before, other), fallbackTarget(after, other)) {
			return fmt.Errorf("%w: native API changed the other scope", errNativeConfigApply)
		}
		return nil
	})
	if err != nil && attempted {
		return fmt.Errorf("%w: native settings may have changed; run models plan to inspect them: %w", errNativeConfigApply, err)
	}
	return err
}
