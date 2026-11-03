package src

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func providerTestCatalog() []string {
	return []string{"llmgateway/claude-sonnet-5-5", "llmgateway/claude-opus-5-5", "llmgateway/gpt-6-1-sol", "copilot/claude-sonnet-5.5", "copilot/claude-opus-5.5", "copilot/gpt-6.1-sol"}
}

func TestProviderModelResolution(t *testing.T) {
	policy := &ProviderPolicy{Primary: "DevPass", Fallback: "copilot"}
	for _, name := range []string{"Sonnet 5.5", "sonnet-5.5", "claude-sonnet-5.5"} {
		route, err := resolveModelReference("model", name, policy, providerTestCatalog())
		require.NoError(t, err)
		require.Equal(t, "llmgateway/claude-sonnet-5-5", route.Primary)
		require.Equal(t, "copilot/claude-sonnet-5.5", route.Fallback)
		require.False(t, route.Explicit)
	}
	route, err := resolveModelReference("expert", "custom/private-model", policy, nil)
	require.NoError(t, err)
	require.True(t, route.Explicit)
	require.Empty(t, route.Fallback)
	_, err = resolveModelReference("model", "Sonnet 5.5", nil, providerTestCatalog())
	require.ErrorIs(t, err, errAmbiguousModel)
	_, err = resolveModelReference("model", "unknown", policy, providerTestCatalog())
	require.ErrorIs(t, err, errModelName)
	_, err = resolveModelReference("model", "Opus 5.5", policy, []string{"llmgateway/claude-opus-5.5"})
	require.ErrorIs(t, err, errModelName)
}

func TestProviderPolicyValidation(t *testing.T) {
	for _, policy := range []*ProviderPolicy{{Fallback: "copilot"}, {Primary: "devpass", Fallback: "LLMGateway"}, {Primary: "a/b"}, {Primary: "--copilot"}, {Primary: "copilot", Fallback: "bad provider"}} {
		require.ErrorIs(t, validateProviderPolicy(policy), errProviderPolicy)
	}
	require.NoError(t, validateProviderPolicy(nil))
	require.NoError(t, validateProviderPolicy(&ProviderPolicy{}))
	require.NoError(t, validateProviderPolicy(&ProviderPolicy{Primary: "devpass", Fallback: "copilot"}))
	for _, ref := range []string{"--model=x", "x/y z", "$(touch file)", "", "a\nb"} {
		require.False(t, validModelReference(ref), ref)
	}
	require.True(t, validModelReference("Sonnet 5.5"))
	var cfg Config
	require.NoError(t, apply(&cfg, "judges", []string{"Haiku 5.5"}))
}

func TestProviderPlanAliasesCannotDuplicateReviewers(t *testing.T) {
	cfg := Config{Providers: &ProviderPolicy{Primary: "devpass"}, Model: "Sonnet 5.5", Review: ReviewConfig{Model: "claude-sonnet-5-5"}}
	_, err := resolveModelPlan(cfg, providerTestCatalog())
	require.ErrorIs(t, err, errReviewerIsCoder)
	cfg.Review.Model = "GPT 6.1 Sol"
	cfg.Judges = []string{"Sonnet 5.5", "claude-sonnet-5-5"}
	_, err = resolveModelPlan(cfg, providerTestCatalog())
	require.ErrorIs(t, err, errDuplicateModels)
}

func TestProviderPickerMigrationAndOptions(t *testing.T) {
	cfg := Config{Model: "copilot/claude-sonnet-5.5", Expert: "custom/private-model", Review: ReviewConfig{Model: "GPT 6.1 Sol"}, Routing: map[string]string{"editing": "copilot/claude-sonnet-5.5", "research": ""}}
	useProviderDefaults(&cfg, &ProviderPolicy{Primary: "devpass", Fallback: "copilot"}, providerTestCatalog())
	require.Equal(t, "claude-sonnet-5-5", cfg.Model)
	require.Equal(t, "custom/private-model", cfg.Expert)
	require.Equal(t, "GPT 6.1 Sol", cfg.Review.Model, "existing model-name aliases must not be needlessly pinned")
	require.Equal(t, "claude-sonnet-5-5", cfg.Routing["editing"])
	require.Empty(t, cfg.Routing["research"])
	options := providerModelOptions(providerTestCatalog(), cfg.Providers, "Sonnet 5.5", cfg.Expert, "Gemini 4 Argon")
	var values []string
	for _, option := range options {
		values = append(values, option.Value)
	}
	require.Contains(t, values, "claude-sonnet-5-5")
	require.Contains(t, values, "Sonnet 5.5")
	require.Contains(t, values, "custom/private-model")
	require.NotContains(t, values, "copilot/claude-sonnet-5.5")
	require.NotContains(t, values, "Gemini 4 Argon")
	require.Equal(t, []string{"copilot", "llmgateway"}, catalogProviders(providerTestCatalog()))
}

func TestProviderRecommendationsKeepNamesAndOverrides(t *testing.T) {
	cfg := Config{Providers: &ProviderPolicy{Primary: "devpass"}, Model: "Sonnet 5.5", Expert: "custom/private-model"}
	rec := recommendConfigured(providerTestCatalog(), cfg, true)
	require.Equal(t, []string{"Sonnet 5.5"}, rec["model"])
	require.Equal(t, []string{"custom/private-model"}, rec["expert"])
	require.Equal(t, []string{"gpt-6-1-sol"}, rec["review.model"])
	rec = recommendConfigured(providerTestCatalog(), cfg, false)
	require.Equal(t, []string{"claude-sonnet-5-5"}, rec["model"])
	require.Equal(t, []string{"claude-opus-5-5"}, rec["expert"])
	require.False(t, suggestedModel("Gemini 4 Argon"))
	require.Equal(t, "claude", family("Sonnet 5.5"))
}

func TestProviderFallbackVerification(t *testing.T) {
	route, err := resolveModelReference("model", "Sonnet 5.5", &ProviderPolicy{Primary: "devpass", Fallback: "copilot"}, providerTestCatalog())
	require.NoError(t, err)
	plan := ModelPlan{Routes: []ModelRoute{route}}
	chains := plan.fallbackChains()
	require.NoError(t, plan.verifyFallbacks(chains))
	require.ErrorIs(t, plan.verifyFallbacks(nil), errNativeFallback)
	chains[route.Primary] = append(chains[route.Primary], "custom/undeclared")
	require.ErrorIs(t, plan.verifyFallbacks(chains), errNativeFallback)
	chains = plan.fallbackChains()
	chains[route.Fallback] = []string{"custom/undeclared"}
	require.ErrorIs(t, plan.verifyFallbacks(chains), errNativeFallback)
	require.NoError(t, verifyModelPlan(context.Background(), "/missing", "/missing", ModelPlan{}), "no preference requires no native process")
}

func TestProviderModelFlagsAreNotPromptText(t *testing.T) {
	args := []string{"--system", "--model", "--model=Sonnet 5.5", "--system", "--effort"}
	resolved, routes, err := resolveModelArguments(args, &ProviderPolicy{Primary: "devpass"}, providerTestCatalog())
	require.NoError(t, err)
	require.Len(t, routes, 1)
	require.Equal(t, []string{"--system", "--model", "--model=llmgateway/claude-sonnet-5-5", "--system", "--effort"}, resolved)
	require.False(t, hasEffort(args))
	require.Empty(t, flagValue(args, "--effort"))
	require.Equal(t, []string{"--system", "--model", "--system", "--effort"}, dropFlag(args, "--model"))
	require.False(t, argumentsNeedCatalog([]string{"--system", "--model"}))
	_, _, err = resolveModelArguments([]string{"--model"}, nil, nil)
	require.ErrorIs(t, err, errModelName)
	require.True(t, slices.Equal(args, []string{"--system", "--model", "--model=Sonnet 5.5", "--system", "--effort"}))
}

func TestProviderYAMLTrustAndPolicyDelta(t *testing.T) {
	if interactive() {
		t.Skip("non-interactive trust behaviour")
	}
	home, project := t.TempDir(), t.TempDir()
	global := Config{Providers: &ProviderPolicy{Primary: "devpass"}, Model: "Sonnet 5.5", Review: ReviewConfig{Model: "GPT 6.1 Sol"}}
	require.NoError(t, writeConfigFile(home, global))
	local := Config{Providers: &ProviderPolicy{Primary: "copilot"}}
	require.NoError(t, writeConfigYAML(filepath.Join(project, ".codeforge"), local))
	cfg, err := loadConfigAt(home, project)
	require.NoError(t, err)
	require.Equal(t, "devpass", cfg.Providers.Primary)
	require.NoError(t, approveProjectConfig(home, project))
	cfg, err = loadConfigAt(home, project)
	require.NoError(t, err)
	require.Equal(t, "copilot", cfg.Providers.Primary)
	cloned := cloneConfig(cfg)
	cloned.Providers.Primary = "other"
	require.Equal(t, "copilot", cfg.Providers.Primary)
	baseline := configDefaults(global)
	edited := cloneConfig(baseline)
	edited.Providers.Primary = "llmgateway"
	delta := projectModelDelta(Config{}, baseline, edited)
	require.Nil(t, delta.Providers, "equivalent provider alias must not create a project pin")
	edited.Providers = nil
	delta = projectModelDelta(Config{}, baseline, edited)
	require.NotNil(t, delta.Providers)
	require.Empty(t, mergeConfig(global, delta).Providers.Primary)
}
func TestProviderProjectAdvancedStyleResolvesInheritedHelpers(t *testing.T) {
	global := Config{Providers: &ProviderPolicy{Primary: "devpass"}, Model: "Sonnet 5.5", Expert: "Opus 5.5", Init: "Sonnet 5.5", Judges: []string{"Opus 5.5"}, Review: ReviewConfig{Model: "GPT 6.1 Sol", SecondOpinion: []string{"Opus 5.5"}}, Routing: map[string]string{"editing": "Sonnet 5.5", "research": "GPT 6.1 Sol"}}
	baseline := cloneConfig(global)
	plan, err := resolveModelPlan(baseline, providerTestCatalog())
	require.NoError(t, err)
	edited := plan.Config
	edited.Providers = nil
	edited.Routing = map[string]string{"research": ""}
	delta := projectModelDelta(Config{}, baseline, edited)
	_, err = resolveModelPlan(mergeConfig(global, delta), providerTestCatalog())
	require.ErrorIs(t, err, errAmbiguousModel, "reproduces the unqualified inherited-helper failure")
	effective, err := prepareProjectModelPreview(global, Config{}, baseline, &edited, providerTestCatalog())
	require.NoError(t, err)
	_, err = resolveModelPlan(effective, providerTestCatalog())
	require.NoError(t, err)
	require.Equal(t, "llmgateway/claude-sonnet-5-5", edited.Routing["editing"])
	require.Empty(t, edited.Routing["research"], "explicit native resets remain resets")
	require.Equal(t, "Sonnet 5.5", global.Routing["editing"], "global configuration remains unchanged")
}
func TestProviderPlanCommandKeepsConfigFiles(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Chdir(root)
	require.NoError(t, writeConfigFile(home, Config{Model: "custom/coder", Review: ReviewConfig{Model: "custom/reviewer"}}))
	path := filepath.Join(configDir(home), "config.yaml")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, runModels(t.Context(), home, "/missing", []string{"plan"}))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.ErrorIs(t, runModels(t.Context(), home, "/missing", []string{"plan", "unexpected"}), errModelsUsage)
}

func TestProviderCatalogFiltersDisconnectedAccounts(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "empryo")
	body := `#!/bin/sh
case "$1" in
 --list-models) printf '%s\n' 'llmgateway/claude-sonnet-5.5' 'copilot/claude-sonnet-5.5' 'offline/claude-sonnet-5.5' ;;
 --list-providers) printf '%s\n' 'llmgateway  Ready' 'copilot  Ready' 'offline  Missing key' ;;
 *) exit 90 ;;
esac
`
	require.NoError(t, os.WriteFile(bin, []byte(body), 0700))
	ids, err := connectedModelCatalog(t.Context(), bin)
	require.NoError(t, err)
	require.Equal(t, []string{"llmgateway/claude-sonnet-5.5", "copilot/claude-sonnet-5.5"}, ids)
}

func TestProviderNativeReadOnlyProtocol(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for fake ACP server")
	}
	dir := t.TempDir()
	bin, log := filepath.Join(dir, "empryo"), filepath.Join(dir, "requests")
	t.Setenv("CF_RPC_LOG", log)
	body := "#!" + python + "\n" + `import json, os, sys
for line in sys.stdin:
 request = json.loads(line)
 with open(os.environ["CF_RPC_LOG"], "a") as out: out.write(request["method"]+"\n")
 method = request["method"]
 result = {}
 if method == "session/new": result = {"sessionId":"owned-query"}
 if method == "_empryo/config/get": result = {"values":{"modelFallback":{"llmgateway/claude-sonnet-5-5":["copilot/claude-sonnet-5.5"]}}}
 if method == "session/delete": assert request["params"]["sessionId"] == "owned-query"
 print(json.dumps({"jsonrpc":"2.0","id":request["id"],"result":result}), flush=True)
`
	require.NoError(t, os.WriteFile(bin, []byte(body), 0700))
	snapshot, err := readNativeFallbacks(t.Context(), bin, dir)
	require.NoError(t, err)
	require.Equal(t, snapshot.Global, snapshot.Project)
	require.Equal(t, snapshot.Global, snapshot.Effective)
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "initialize\nsession/new\n_empryo/config/get\n_empryo/config/get\n_empryo/config/get\nsession/delete\n", string(data))
}
