package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
)

var errProviderConfigureTerminal = errors.New("provider configure needs a terminal; use codeforge provider set PRIMARY [BACKUP...] instead")
var errProviderConfigureCancelled = errors.New("provider configuration cancelled; nothing saved")

const providerDone = ""

// providerLabel names a provider and says which saved models it lacks, so a bad backup is visible before it is picked.
func providerLabel(id string, missing []string) string {
	label := id
	if id == "llmgateway" {
		label = "llmgateway (LLM Gateway / DevPass)"
	}
	if len(missing) > 0 {
		label += " — missing " + strings.Join(missing, ", ")
	}
	return label
}

// missingModels lists the saved model names a provider cannot serve; explicit provider/model IDs never move.
func missingModels(cfg Config, provider string, catalog []string) []string {
	var missing []string
	seen := map[string]bool{}
	refs := []string{}
	for _, role := range roles {
		refs = append(refs, current(cfg, role.key)...)
	}
	for _, ref := range cfg.Routing {
		refs = append(refs, ref)
	}
	for _, ref := range refs {
		if ref == "" || validModelID(ref) || seen[ref] {
			continue
		}
		seen[ref] = true
		if _, err := catalogModel(ref, provider, catalog); err != nil {
			missing = append(missing, ref)
		}
	}
	slices.Sort(missing)
	return missing
}

// backupOptions offers every connected provider not already in the chain, plus Done to stop adding.
func backupOptions(cfg Config, connected, chain []string, catalog []string) []huh.Option[string] {
	options := []huh.Option[string]{huh.NewOption("Done — no more backups", providerDone)}
	for _, id := range connected {
		if !slices.Contains(chain, id) {
			options = append(options, huh.NewOption(providerLabel(id, missingModels(cfg, id, catalog)), id))
		}
	}
	return options
}

// providerChainPreview shows the order as Empryo will try it.
func providerChainPreview(scope string, chain []string) string {
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
	return text.String()
}

// providerAsker asks one question; tests replace it to drive the menu without a terminal.
type providerAsker interface {
	choose(title, description string, options []huh.Option[string], value *string) error
	confirm(title, description string, value *bool) error
}

type formAsker struct{}

func (formAsker) choose(title, description string, options []huh.Option[string], value *string) error {
	return runForm(title, huh.NewSelect[string]().Title(title).Description(description).Options(options...).Value(value))
}

func (formAsker) confirm(title, description string, value *bool) error {
	return runForm(title, huh.NewConfirm().Title(title).Description(description).Value(value))
}

// configureProviders is the interactive form for provider set: scope, primary, then backups one at a time, in order.
func configureProviders(ctx context.Context, home, bin, root string, inGit bool, args []string, w io.Writer) error {
	ids, scope, err := providerArgs(args)
	if err != nil || len(ids) > 0 {
		return errProviderUsage
	}
	if !interactive() {
		return errProviderConfigureTerminal
	}
	fmt.Fprintln(os.Stderr, "codeforge: loading connected providers...")
	catalog, err := connectedModelCatalog(ctx, bin)
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
		return "", errNoModels
	}
	if scope == "" {
		scope = "global"
		options := []huh.Option[string]{huh.NewOption("All projects", "global")}
		if !sameConfigScope(home, root) {
			if inGit {
				scope = "project"
			}
			options = append([]huh.Option[string]{huh.NewOption("This project: "+root, "project")}, options...)
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
		current = append([]string{providerID(cfg.Providers.Primary)}, cfg.Providers.Backups()...)
	}
	primary := connected[0]
	if len(current) > 0 && slices.Contains(connected, current[0]) {
		primary = current[0]
	} else if slices.Contains(connected, "llmgateway") {
		primary = "llmgateway"
	}
	var primaries []huh.Option[string]
	for _, id := range connected {
		primaries = append(primaries, huh.NewOption(providerLabel(id, missingModels(cfg, id, catalog)), id))
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
		help := "Empryo switches to it, in this order, when the provider before it fails or runs out of quota. A provider missing a model cannot be saved."
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
	if err := checkProviderChain(home, root, scope, policy, catalog); err != nil {
		return "", err
	}
	preview := providerChainPreview(scope, chain)
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
