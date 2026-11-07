package src

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"

	"github.com/codeliter/codeforge/src/guard"
)

var errModelApplyUsage = errors.New("usage: codeforge models apply [--recommended] [--project|--global]")
var errModelApplyTerminal = errors.New("models apply requires a terminal to choose scope and confirm native settings changes; run it yourself")
var errModelApplyCancelled = errors.New("model application cancelled; native settings were not changed")
var errNativeConfigChanged = errors.New("native model settings changed since the preview; nothing was written, run models apply again")
var errNativeConfigApply = errors.New("cannot apply native model settings")

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
func readModelApplyConfig(home, project, scope string) (config.Config, error) {
	cfg, _, err := readModelApplyLayers(home, project, scope)
	return cfg, err
}

// readModelApplyLayers is readModelApplyConfig that also returns the project file's own settings
// (empty for global scope), read once, so callers never trigger a second trust prompt for the same file.
func readModelApplyLayers(home, project, scope string) (merged, local config.Config, err error) {
	merged, err = config.ReadFile(home)
	if err != nil {
		return merged, config.Config{}, err
	}
	if scope == "project" {
		if local, err = config.ReadTrustedProject(home, project, modelTrustApprover()); err != nil {
			return merged, local, err
		}
		merged = config.Merge(merged, local)
	}
	merged = config.Defaults(merged)
	return merged, local, config.Validate(merged)
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
		_, projectErr := os.Stat(config.ProjectPath(root))
		scope = defaultModelApplyScope(gitErr == nil || projectErr == nil)
		if config.SameScope(home, root) {
			scope = "global"
		}
		options := []forms.Option{{Label: "Global defaults (projects can override)", Value: "global"}}
		if !config.SameScope(home, root) {
			options = append(options, forms.Option{Label: "This project: " + root, Value: "project"})
		}
		if scope, err = forms.Choose("Where should these model settings apply?", applyScopeDescription(recommended), options, scope); err != nil {
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
	if catalog == nil && config.NeedsCatalog(cfg) {
		fmt.Fprintln(os.Stderr, "codeforge: resolving saved model choices through connected providers...")
		catalog, err = connectedModelCatalogAt(ctx, bin, workspace)
		if err != nil {
			return err
		}
	}
	plan, err := config.ResolvePlan(cfg, catalog)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Applying %s Codeforge settings (one-off environment overrides are excluded).\n", scope)
	printModelPlan(os.Stdout, plan)
	target := filepath.Join(home, ".empryo", "config.json")
	if scope == "project" {
		target = filepath.Join(root, ".empryo", "config.json")
	}
	confirmNative := func(title string) func(string) bool {
		return func(preview string) bool {
			description := fmt.Sprintf("Native target: %q\n%s", target, preview)
			fmt.Fprintln(os.Stderr, description)
			approved, err := forms.YesNo(title, description)
			return err == nil && approved
		}
	}
	if err := applyModelPlan(ctx, bin, workspace, scope, plan, confirmNative("Apply these changes to "+scope+" Empryo settings?")); err != nil {
		return err
	}
	if scope == "global" && !config.SameScope(home, root) {
		if warning := globalDefaultModelShadowed(root, plan); warning != "" {
			fmt.Fprintln(os.Stderr, "codeforge: "+warning)
		}
	}
	return nil
}
func applyScopeDescription(recommended bool) string {
	if recommended {
		return "Saves recommended roles after confirmation, then applies any declared native fallback and default model."
	}
	return "Applies saved choices, not model recommendations. Native fallback and default model changes require confirmation."
}

// recommendedModelConfig replaces every role with a recommendation where one exists.
func recommendedModelConfig(cfg config.Config, catalog []string) (config.Config, map[string][]string, error) {
	next := config.Clone(cfg)
	rec := recommendConfigured(catalog, next, false)
	for _, r := range config.Roles {
		if len(rec[r.Key]) > 0 {
			config.AssignRole(&next, r.Key, slices.Clone(rec[r.Key]))
		}
	}
	if err := config.Validate(next); err != nil {
		return cfg, rec, err
	}
	if _, err := config.ResolvePlan(next, catalog); err != nil {
		return cfg, rec, err
	}
	return next, rec, nil
}

func recommendedModelsPreview(before, after config.Config) (string, bool) {
	var text strings.Builder
	changed := false
	fmt.Fprintln(&text, "Recommended role changes:")
	for _, r := range config.Roles {
		old, next := config.Current(before, r.Key), config.Current(after, r.Key)
		if slices.Equal(old, next) {
			continue
		}
		changed = true
		fmt.Fprintf(&text, "  %s: %s -> %s\n", r.Key, displayModels(old), displayModels(next))
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
	ok, err := forms.YesNo("Save these recommended models to "+scope+" Codeforge settings?", preview)
	return err == nil && ok
}

func saveRecommendedModels(home, root, scope string, cfg config.Config, catalog []string, approve func(string, string) bool) (config.Config, error) {
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
		global, err := config.ReadFile(home)
		if err != nil {
			return cfg, err
		}
		for _, r := range config.Roles {
			config.AssignRole(&global, r.Key, slices.Clone(config.Current(next, r.Key)))
		}
		if err := config.WriteFile(home, global); err != nil {
			return cfg, err
		}
		fmt.Fprintf(os.Stdout, "Saved recommended models to %s.\n", filepath.Join(config.Dir(home), "config.yaml"))
		return next, nil
	}
	if err := config.EnsureProjectEditing(home, root, func() bool {
		return confirm("Replace the unapproved project model file with recommended choices? It is kept if you cancel.")
	}); err != nil {
		return cfg, err
	}
	if isSymlinked(root, filepath.Join(".codeforge", "config.yaml")) {
		return cfg, config.ErrConfigSymlink
	}
	local, err := config.ReadTrustedProject(home, root, modelTrustApprover())
	if err != nil {
		return cfg, err
	}
	delta := projectModelDelta(local, cfg, next)
	// Recommendations change roles only; inherited helper routing must stay inherited.
	delta.Routing = config.Clone(local).Routing
	dir := filepath.Dir(config.ProjectPath(root))
	if err := configureModelFile(dir, delta, catalog, func(*config.Config, []string) error { return nil }, func(_ string, content string) error {
		return config.Trust(home, config.ProjectPath(root), content)
	}); err != nil {
		return cfg, err
	}
	fmt.Fprintf(os.Stdout, "Saved and approved recommended models in %s.\n", config.ProjectPath(root))
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

func plannedNativeFallbacks(plan config.ModelPlan, snapshot *nativeFallbackSnapshot, scope string) (map[string][]string, error) {
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
	for primary, backups := range plan.FallbackChains() {
		desired[primary] = slices.Clone(backups)
	}
	for primary, backups := range plan.FallbackChains() {
		allowed := append([]string{primary}, backups...)
		for _, backup := range backups {
			for _, further := range desired[backup] {
				if !slices.Contains(allowed, further) {
					desired[backup] = []string{}
					break
				}
			}
		}
	}
	return desired, plan.VerifyFallbacks(desired)
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

// nativeModelSnapshot is what models apply may change in one Empryo scope.
type nativeModelSnapshot struct {
	Fallbacks    *nativeFallbackSnapshot
	DefaultModel string
}

// nativeModelChange is the approved write; a nil field leaves that setting alone.
type nativeModelChange struct {
	Fallbacks    map[string][]string
	DefaultModel string
}

func (change nativeModelChange) empty() bool {
	return change.Fallbacks == nil && change.DefaultModel == ""
}

func readNativeModelSnapshot(request nativeConfigRequest, session, scope string) (*nativeModelSnapshot, error) {
	fallbacks, err := readNativeFallbackSnapshot(request, session)
	if err != nil {
		return nil, err
	}
	model, err := readNativeDefaultModel(request, session, scope)
	if err != nil {
		return nil, err
	}
	return &nativeModelSnapshot{Fallbacks: fallbacks, DefaultModel: model}, nil
}

// applyModelPlan writes the plan's fallback chains and coder model to one Empryo scope.
// It reads once, asks once for every change, then writes and verifies in a single session.
func applyModelPlan(ctx context.Context, bin, workspace, scope string, plan config.ModelPlan, approve func(string) bool) error {
	if scope != "global" && scope != "project" {
		return errModelApplyUsage
	}
	model := plannedDefaultModel(plan)
	if len(plan.FallbackChains()) == 0 && model == "" {
		return nil
	}
	var before *nativeModelSnapshot
	if err := withNativeConfig(ctx, bin, workspace, func(request nativeConfigRequest, session string) error {
		var err error
		before, err = readNativeModelSnapshot(request, session, scope)
		return err
	}); err != nil {
		return err
	}
	var change nativeModelChange
	var preview strings.Builder
	if len(plan.FallbackChains()) > 0 {
		desired, err := plannedNativeFallbacks(plan, before.Fallbacks, scope)
		if err != nil {
			return err
		}
		if sameFallbackMap(fallbackTarget(before.Fallbacks, scope), desired) {
			if err := plan.VerifyFallbacks(before.Fallbacks.Effective); err != nil {
				return err
			}
		} else {
			change.Fallbacks = desired
			preview.WriteString(nativeFallbackPreview(fallbackTarget(before.Fallbacks, scope), desired))
		}
	}
	if model != "" && model != before.DefaultModel {
		change.DefaultModel = model
		preview.WriteString(nativeDefaultModelPreview(before.DefaultModel, model))
	}
	if change.empty() {
		fmt.Fprintln(os.Stdout, "Native model settings already match; nothing was written.")
		return nil
	}
	if !approve(preview.String()) {
		return errModelApplyCancelled
	}
	if err := writeNativeModelChange(ctx, bin, workspace, scope, plan, before, change); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Applied and verified %s native model settings. Saved Codeforge roles were not replaced. New sessions use these settings.\n", scope)
	return nil
}

// plannedDefaultModel is the resolved coder model the Empryo TUI should preselect.
func plannedDefaultModel(plan config.ModelPlan) string {
	for _, route := range plan.Routes {
		if route.Role == "model" && route.Primary != "" {
			return route.Primary
		}
	}
	return plan.Config.Model
}

func readNativeDefaultModel(request nativeConfigRequest, session, scope string) (string, error) {
	var read struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err := request("_empryo/config/get", map[string]any{"sessionId": session, "scope": scope, "keys": []string{"defaultModel"}}, &read); err != nil {
		return "", err
	}
	var current string
	if data, ok := read.Values["defaultModel"]; ok && string(data) != "null" {
		if err := json.Unmarshal(data, &current); err != nil {
			return "", fmt.Errorf("%w: malformed %s defaultModel: %w", errNativeConfigRead, scope, err)
		}
	}
	return current, nil
}

func nativeDefaultModelPreview(before, after string) string {
	previous := "(not set)"
	if before != "" {
		previous = fmt.Sprintf("%q", before)
	}
	return fmt.Sprintf("Native defaultModel change (the model Empryo selects on start):\n  %s -> %q\n", previous, after)
}

// projectDefaultModel reads a project's own Empryo defaultModel, which wins over the global one.
func projectDefaultModel(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".empryo", "config.json"))
	if err != nil {
		return ""
	}
	var native struct {
		DefaultModel string `json:"defaultModel"` //nolint:tagliatelle // Empryo config file format.
	}
	if json.Unmarshal(data, &native) != nil {
		return ""
	}
	return native.DefaultModel
}

// globalDefaultModelShadowed explains when a project setting will keep the TUI on another model.
func globalDefaultModelShadowed(root string, plan config.ModelPlan) string {
	model, project := plannedDefaultModel(plan), projectDefaultModel(root)
	if model == "" || project == "" || project == model {
		return ""
	}
	return fmt.Sprintf("This project's Empryo settings select %q, which overrides the global default %q here. Run codeforge models apply --project to change it.", project, model)
}

func writeNativeModelChange(ctx context.Context, bin, workspace, scope string, plan config.ModelPlan, before *nativeModelSnapshot, change nativeModelChange) error {
	attempted := false
	err := withNativeConfig(ctx, bin, workspace, func(request nativeConfigRequest, session string) error {
		current, err := readNativeModelSnapshot(request, session, scope)
		if err != nil {
			return err
		}
		was, now := before.Fallbacks, current.Fallbacks
		if !sameFallbackMap(was.Global, now.Global) || !sameFallbackMap(was.Project, now.Project) || !sameFallbackMap(was.Effective, now.Effective) || before.DefaultModel != current.DefaultModel {
			return errNativeConfigChanged
		}
		attempted = true
		if change.Fallbacks != nil {
			if err := request("_empryo/config/set", map[string]any{"sessionId": session, "scope": scope, "key": "modelFallback", "value": change.Fallbacks}, nil); err != nil {
				return err
			}
		}
		if change.DefaultModel != "" {
			if err := request("_empryo/config/set", map[string]any{"sessionId": session, "scope": scope, "key": "defaultModel", "value": change.DefaultModel}, nil); err != nil {
				return err
			}
		}
		after, err := readNativeModelSnapshot(request, session, scope)
		if err != nil {
			return err
		}
		if change.DefaultModel != "" && after.DefaultModel != change.DefaultModel {
			return fmt.Errorf("%w: defaultModel is %q, want %q", errNativeConfigApply, after.DefaultModel, change.DefaultModel)
		}
		if change.Fallbacks == nil {
			return nil
		}
		if !sameFallbackMap(change.Fallbacks, fallbackTarget(after.Fallbacks, scope)) {
			return errNativeConfigApply
		}
		other := "global"
		if scope == "global" {
			other = "project"
		}
		if !sameFallbackMap(fallbackTarget(was, other), fallbackTarget(after.Fallbacks, other)) {
			return fmt.Errorf("%w: native API changed the other scope", errNativeConfigApply)
		}
		return errors.Join(plan.VerifyFallbacks(after.Fallbacks.declaredFallbacks()), plan.VerifyFallbacks(after.Fallbacks.Effective))
	})
	if err != nil && attempted {
		return fmt.Errorf("%w: native settings may have changed; run models plan to inspect them: %w", errNativeConfigApply, err)
	}
	return err
}
