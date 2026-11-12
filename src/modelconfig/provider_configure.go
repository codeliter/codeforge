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

const providerDone = ""

// providerLabel names a provider and says how it covers the saved models, so the choice is clear before it is picked.
func providerLabel(id string, missing []string, standIns ...string) string {
	label := id
	if id == "llmgateway" {
		label = "llmgateway (LLM Gateway / DevPass)"
	}
	if len(standIns) > 0 {
		label += " — uses " + strings.Join(standIns, ", ")
	}
	if len(missing) > 0 {
		label += " — nothing comparable to " + strings.Join(missing, ", ")
	}
	return label
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

// missingModels lists the saved model names a provider has nothing comparable for.
func missingModels(cfg config.Config, provider string, catalog []string) []string {
	_, missing := providerCoverage(cfg, provider, catalog)
	return missing
}

func coverageLabel(cfg config.Config, id string, catalog []string) string {
	standIns, missing := providerCoverage(cfg, id, catalog)
	return providerLabel(id, missing, standIns...)
}

// backupOptions offers every connected provider not already in the chain, plus Done to stop adding.
func backupOptions(cfg config.Config, connected, chain []string, catalog []string) []forms.Option {
	options := []forms.Option{{Label: "Done — no more backups", Value: providerDone}}
	for _, id := range connected {
		if !slices.Contains(chain, id) {
			options = append(options, forms.Option{Label: coverageLabel(cfg, id, catalog), Value: id})
		}
	}
	return options
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

func (formAsker) confirm(title, description string, value *bool) error {
	a, err := forms.Ask(title, forms.Field{Kind: forms.Confirm, Title: title, Description: description, Yes: *value})
	if err != nil {
		return err
	}
	*value = a.Yes
	return nil
}

// configureProviders is the interactive form for provider set: scope, primary, then backups one at a time, in order.
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
	primary := connected[0]
	if len(current) > 0 && slices.Contains(connected, current[0]) {
		primary = current[0]
	} else if slices.Contains(connected, "llmgateway") {
		primary = "llmgateway"
	}
	var primaries []forms.Option
	for _, id := range connected {
		primaries = append(primaries, forms.Option{Label: providerLabel(id, missingModels(cfg, id, catalog)), Value: id})
	}
	description := "Every model name runs here first. Connect more providers with /login in Empryo."
	if len(current) > 0 {
		description = "Current order: " + strings.Join(current, " > ") + ". " + description
	}
	if err := ask.choose("Primary provider", description, primaries, &primary); err != nil {
		return "", err
	}
	chain := []string{primary}
	for len(chain) < len(connected) {
		pick := providerDone
		// Preselect the next backup from the saved order, so re-running keeps it with a few presses of Enter.
		for _, id := range current[min(1, len(current)):] {
			if !slices.Contains(chain, id) && slices.Contains(connected, id) {
				pick = id
				break
			}
		}
		help := "Empryo switches to it, in this order, when the provider before it fails or runs out of quota. A provider without one of your models uses its closest model of the same level instead."
		if err := ask.choose(fmt.Sprintf("Backup %d", len(chain)), "Order so far: "+strings.Join(chain, " > ")+"\n"+help, backupOptions(cfg, connected, chain, catalog), &pick); err != nil {
			return "", err
		}
		if pick == providerDone {
			break
		}
		chain = append(chain, pick)
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
