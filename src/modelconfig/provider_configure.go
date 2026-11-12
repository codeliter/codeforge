package modelconfig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/modelcatalog"
)

var errProviderConfigureTerminal = errors.New("provider configure needs a terminal; use codeforge provider set PRIMARY [BACKUP...] instead")
var errProviderConfigureCancelled = errors.New("provider configuration cancelled; nothing saved")

// providerOrderHelp says how the order picker is read.
const providerOrderHelp = "Tick providers in the order Empryo should try them: 1 runs every model name first, the rest are backups for when the one before fails or runs out of quota. Connect more with /login in Empryo."

// providerDetail says how a provider covers the saved models, so the choice is clear before it is ticked.
func providerDetail(cfg config.Config, id string, catalog []string) string {
	standIns, missing := providerCoverage(cfg, id, catalog)
	var parts []string
	if id == "llmgateway" {
		parts = append(parts, "LLM Gateway / DevPass")
	}
	if len(standIns) > 0 {
		parts = append(parts, "uses "+strings.Join(standIns, ", "))
	}
	if len(missing) > 0 {
		parts = append(parts, "nothing comparable to "+strings.Join(missing, ", "))
	}
	if len(standIns)+len(missing) == 0 {
		parts = append(parts, "serves every saved model")
	}
	return strings.Join(parts, " — ")
}

// providerOptions offers every connected provider to the order picker.
func providerOptions(cfg config.Config, connected, catalog []string) []forms.Option {
	options := make([]forms.Option, 0, len(connected))
	for _, id := range connected {
		options = append(options, forms.Option{Label: id, Detail: providerDetail(cfg, id, catalog), Value: id, Group: "Connected providers"})
	}
	return options
}

// savedModelNames lists the model names the config resolves through providers; explicit provider/model IDs never move.
func savedModelNames(cfg config.Config) []string {
	var refs []string
	for _, role := range config.Roles {
		refs = append(refs, config.Current(cfg, role.Key)...)
	}
	for _, ref := range cfg.Routing {
		refs = append(refs, ref)
	}
	refs = slices.DeleteFunc(refs, func(ref string) bool { return ref == "" || config.ValidModelID(ref) })
	slices.Sort(refs)
	return slices.Compact(refs)
}

// providerCoverage reports, for one provider, the stand-ins it would use ("Sonnet 5.5 → copilot/claude-sonnet-5")
// and the saved names it has nothing comparable for.
func providerCoverage(cfg config.Config, provider string, catalog []string) (standIns, missing []string) {
	for _, ref := range savedModelNames(cfg) {
		// A model a rule routes elsewhere does not use the default chain, so this provider never has to cover it.
		if _, ruled := cfg.Providers.RuleFor(ref); ruled {
			continue
		}
		if _, err := config.CatalogModel(ref, provider, catalog); err == nil {
			continue
		}
		if id, ok := config.EquivalentModel(ref, provider, catalog); ok {
			standIns = append(standIns, ref+" → "+config.ModelName(id))
		} else {
			missing = append(missing, ref)
		}
	}
	return standIns, missing
}

// providerChainPreview shows the order as Empryo will try it.
func providerChainPreview(scope string, chain []string, rules ...config.ProviderRule) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Provider order (%s):\n", scope)
	for i, id := range chain {
		role := fmt.Sprintf("Backup %d", i)
		if i == 0 {
			role = "Primary"
		}
		fmt.Fprintf(&text, "  %-9s %s\n", role, id)
	}
	if len(chain) == 1 {
		fmt.Fprintln(&text, "  No backups: if the primary fails, Empryo keeps its own fallback settings.")
	}
	for _, rule := range rules {
		fmt.Fprintf(&text, "  Models %s: %s\n", rule.Match, strings.Join(append([]string{config.ProviderID(rule.Primary)}, rule.Chain().Backups()...), " > "))
	}
	return text.String()
}

// providerAsker asks one question; tests replace it to drive the menu without a terminal.
type providerAsker interface {
	choose(title, description string, options []forms.Option, value *string) error
	confirm(title, description string, value *bool) error
	// order asks for several of options in the order they are to be used; values offers the order to start from.
	order(title, description string, options []forms.Option, values *[]string) error
}

type formAsker struct{}

func (formAsker) choose(title, description string, options []forms.Option, value *string) error {
	chosen, err := forms.Choose(title, description, options, *value)
	if err != nil {
		return err
	}
	*value = chosen
	return nil
}

func (formAsker) order(title, description string, options []forms.Option, values *[]string) error {
	a, err := forms.Ask(title, forms.Field{
		Kind: forms.Search, Title: title, Description: description, Header: "Provider\tYour saved models",
		Options: options, Values: *values, Multi: true, Ordered: true,
	})
	if err != nil {
		return err
	}
	*values = a.Values
	return nil
}

func (formAsker) confirm(title, description string, value *bool) error {
	a, err := forms.Ask(title, forms.Field{Kind: forms.Confirm, Title: title, Description: description, Yes: *value})
	if err != nil {
		return err
	}
	*value = a.Yes
	return nil
}

// configureProviders is the interactive form for provider set: scope, then the providers ticked in order.
func configureProviders(ctx context.Context, home, bin, root string, inGit bool, args []string, w io.Writer) error {
	ids, scope, err := providerArgs(args)
	if err != nil || len(ids) > 0 {
		return errProviderUsage
	}
	if !forms.Interactive() {
		return errProviderConfigureTerminal
	}
	fmt.Fprintln(os.Stderr, "codeforge: loading connected providers...")
	catalog, err := modelcatalog.Connected(ctx, bin)
	if err != nil {
		return err
	}
	apply, err := pickProviderOrder(formAsker{}, home, root, inGit, scope, catalog, w)
	if err != nil || apply == "" {
		return err
	}
	return runModelApply(ctx, home, bin, []string{"--" + apply})
}

// pickProviderOrder runs the questions and saves the order. It returns the scope to apply, or "" to stop there.
func pickProviderOrder(ask providerAsker, home, root string, inGit bool, scope string, catalog []string, w io.Writer) (string, error) {
	connected := catalogProviders(catalog)
	if len(connected) == 0 {
		return "", config.ErrNoModels
	}
	if scope == "" {
		scope = "global"
		options := []forms.Option{{Label: "All projects", Value: "global"}}
		if !config.SameScope(home, root) {
			if inGit {
				scope = "project"
			}
			options = append([]forms.Option{{Label: "This project: " + root, Value: "project"}}, options...)
		}
		if err := ask.choose("Where should this provider order apply?", "", options, &scope); err != nil {
			return "", err
		}
	}
	cfg, err := readModelApplyConfig(home, root, scope)
	if err != nil {
		return "", err
	}
	current := []string{}
	if cfg.Providers != nil && cfg.Providers.Primary != "" {
		current = append([]string{config.ProviderID(cfg.Providers.Primary)}, cfg.Providers.Backups()...)
	}
	chain := slices.DeleteFunc(slices.Clone(current), func(id string) bool { return !slices.Contains(connected, id) })
	if len(chain) < len(current) {
		fmt.Fprintln(w, "codeforge: a saved provider is not connected now and is left out of the order offered; leave the screen with Esc to keep the saved order.")
	}
	if len(chain) == 0 {
		chain = []string{connected[0]}
		if slices.Contains(connected, "llmgateway") {
			chain[0] = "llmgateway"
		}
	}
	description := providerOrderHelp
	if len(current) > 0 {
		description = "Current order: " + strings.Join(current, " > ") + ". " + description
	}
	if err := ask.order("Provider order", description, providerOptions(cfg, connected, catalog), &chain); err != nil {
		return "", err
	}
	if len(chain) == 0 {
		return "", errProviderConfigureCancelled
	}
	policy, err := providerChain(chain, connected)
	if err != nil {
		return "", err
	}
	if cfg.Providers != nil {
		policy.Rules = cfg.Providers.Clone().Rules
	}
	if err := checkProviderChain(home, root, scope, policy, catalog); err != nil {
		return "", err
	}
	preview := providerChainPreview(scope, chain, policy.Rules...)
	save, apply := true, true
	if err := ask.confirm("Save this provider order?", preview, &save); err != nil {
		return "", err
	}
	if !save {
		return "", errProviderConfigureCancelled
	}
	if err := saveProviderPolicy(home, root, scope, policy); err != nil {
		return "", err
	}
	fmt.Fprint(w, preview)
	fmt.Fprintln(w, "Saved.")
	// The order is already saved; leaving this last question (Esc) just skips applying it.
	if err := ask.confirm("Apply it to Empryo now?", "Runs codeforge models apply: it shows every Empryo change and asks again before writing.", &apply); err != nil {
		apply = false
	}
	if !apply {
		fmt.Fprintln(w, "Run codeforge models apply when you're ready to make Empryo use this order.")
		return "", nil
	}
	return scope, nil
}
