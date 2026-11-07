package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/codeliter/codeforge/src/config"

	"github.com/codeliter/codeforge/src/guard"
)

var errProviderUsage = errors.New("usage: codeforge provider [show] | provider set [PRIMARY [BACKUP...]] [--for MODEL-GLOB PROVIDER[,PROVIDER...]]... [--no-rules] [--project|--global] | provider clear [--project|--global]")

// providerArgs splits provider IDs from the optional scope flag.
func providerArgs(args []string) (ids []string, scope string, err error) {
	for _, arg := range args {
		switch arg {
		case "--project", "--global":
			if scope != "" {
				return nil, "", errProviderUsage
			}
			scope = strings.TrimPrefix(arg, "--")
		default:
			if strings.HasPrefix(arg, "-") {
				return nil, "", errProviderUsage
			}
			ids = append(ids, arg)
		}
	}
	return ids, scope, nil
}

// ruleArg is one --for MODEL-GLOB PROVIDER[,PROVIDER...] pair: the models matching the glob use their own chain.
type ruleArg struct {
	match string
	ids   []string
}

// providerRuleArgs pulls the repeatable --for pairs out of the arguments and returns the rest.
func providerRuleArgs(args []string) (rest []string, rules []ruleArg, err error) {
	for i := 0; i < len(args); i++ {
		var match, list string
		switch {
		case args[i] == "--for":
			if i+2 >= len(args) {
				return nil, nil, errProviderUsage
			}
			match, list = args[i+1], args[i+2]
			i += 2
		case strings.HasPrefix(args[i], "--for="):
			if i+1 >= len(args) {
				return nil, nil, errProviderUsage
			}
			match, list = strings.TrimPrefix(args[i], "--for="), args[i+1]
			i++
		default:
			rest = append(rest, args[i])
			continue
		}
		var ids []string
		for _, id := range strings.Split(list, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
		if strings.HasPrefix(match, "-") || strings.HasPrefix(list, "-") || len(ids) == 0 {
			return nil, nil, errProviderUsage
		}
		rules = append(rules, ruleArg{match: match, ids: ids})
	}
	return rest, rules, nil
}

// runProvider shows or changes which provider Codeforge uses first and which backups Empryo switches to, in order.
func runProvider(ctx context.Context, home, bin string, args []string, w io.Writer) error {
	sub := "show"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}
	root, gitErr := guard.RepoRoot(ctx, wd)
	if gitErr != nil {
		root = wd
	}
	switch sub {
	case "show", "list":
		if len(args) > 0 {
			return errProviderUsage
		}
		return showProviders(ctx, home, bin, root, w)
	case "set", "clear":
		rest, rules, err := providerRuleArgs(args)
		if err != nil {
			return err
		}
		noRules := slices.Contains(rest, "--no-rules")
		rest = slices.DeleteFunc(slices.Clone(rest), func(arg string) bool { return arg == "--no-rules" })
		ids, scope, err := providerArgs(rest)
		if err != nil {
			return err
		}
		if sub == "set" && len(ids) == 0 && len(rules) == 0 && !noRules || sub == "clear" && (len(ids) > 0 || len(rules) > 0 || noRules) || noRules && len(rules) > 0 {
			return errProviderUsage
		}
		if scope == "" {
			scope = "global"
			if gitErr == nil && !config.SameScope(home, root) {
				scope = "project"
			}
		}
		var policy *config.ProviderPolicy
		inherited := false
		if sub == "set" {
			catalog, err := connectedModelCatalog(ctx, bin)
			if err != nil {
				return err
			}
			connected := catalogProviders(catalog)
			// Start from what this scope already uses: giving only a chain keeps its rules, giving only --for keeps its chain.
			saved, local, err := readModelApplyLayers(home, root, scope)
			if err != nil {
				return err
			}
			if policy = saved.Providers.Clone(); policy == nil {
				policy = &config.ProviderPolicy{}
			}
			hadRules := len(policy.Rules) > 0
			// A project's providers block replaces the global one, so a project without its own copies whatever this keeps.
			inherited = scope == "project" && local.Providers == nil &&
				(len(ids) == 0 && policy.Primary != "" || len(rules) == 0 && !noRules && hadRules)
			if len(ids) > 0 {
				chain, err := providerChain(ids, connected)
				if err != nil {
					return err
				}
				policy.Primary, policy.Fallback, policy.Fallbacks = chain.Primary, "", chain.Fallbacks
			}
			if len(rules) > 0 || noRules {
				if policy.Rules, err = providerRules(rules, connected); err != nil {
					return err
				}
			}
			if policy.Primary == "" && len(policy.Rules) == 0 {
				if noRules && hadRules {
					return fmt.Errorf("%w: no provider order is left after removing the rules; use provider clear instead", errProviderUsage)
				}
				return errProviderUsage
			}
			if err := config.ValidateProviderPolicy(policy); err != nil {
				return err
			}
			if err := checkProviderPolicy(saved, policy, catalog); err != nil {
				return err
			}
		}
		if err := saveProviderPolicy(home, root, scope, policy); err != nil {
			return err
		}
		if policy == nil {
			fmt.Fprintf(w, "Cleared %s provider order. Model names now resolve without a preferred provider.\n", scope)
		} else {
			if policy.Primary != "" {
				fmt.Fprintf(w, "Saved %s provider order: %s\n", scope, strings.Join(append([]string{policy.Primary}, policy.Backups()...), " > "))
			} else {
				fmt.Fprintf(w, "Saved %s provider rules (no default provider order).\n", scope)
			}
			for _, rule := range policy.Rules {
				fmt.Fprintf(w, "  %s: %s\n", rule.Match, strings.Join(append([]string{rule.Primary}, rule.Chain().Backups()...), " > "))
			}
		}
		if inherited {
			fmt.Fprintln(w, "The parts you did not change were copied from your global order into this project; later global changes no longer reach it.")
		}
		fmt.Fprintln(w, "Run codeforge models apply to make Empryo use this order; it shows every change and asks first.")
		return nil
	case "configure":
		return configureProviders(ctx, home, bin, root, gitErr == nil, args, w)
	}
	return fmt.Errorf("%w %q; use configure, show, set or clear", errUnknownSubcommand, sub)
}

// providerRules turns each --for pair into a rule whose providers are connected and listed once.
func providerRules(args []ruleArg, connected []string) ([]config.ProviderRule, error) {
	var rules []config.ProviderRule
	for _, arg := range args {
		chain, err := providerChain(arg.ids, connected)
		if err != nil {
			return nil, fmt.Errorf("--for %s: %w", arg.match, err)
		}
		rules = append(rules, config.ProviderRule{Match: arg.match, Primary: chain.Primary, Fallbacks: chain.Fallbacks})
	}
	return rules, nil
}

// providerChain normalises aliases and checks every provider is connected and listed once.
func providerChain(ids, connected []string) (*config.ProviderPolicy, error) {
	var chain []string
	for _, id := range ids {
		id = config.ProviderID(id)
		if !config.ValidProviderID(id) {
			return nil, fmt.Errorf("%w: %q is not a provider ID", config.ErrProviderPolicy, id)
		}
		if !slices.Contains(connected, id) {
			return nil, fmt.Errorf("%w: %s is not connected in Empryo (connected: %s); sign in with /login first", config.ErrProviderPolicy, id, strings.Join(connected, ", "))
		}
		if slices.Contains(chain, id) {
			return nil, fmt.Errorf("%w: %s is listed twice", config.ErrProviderPolicy, id)
		}
		chain = append(chain, id)
	}
	policy := config.NewProviderPolicy(chain[0], chain[1:])
	return policy, config.ValidateProviderPolicy(policy)
}

// checkProviderChain resolves every saved role on every provider, so the order is never saved half-working.
// A backup without the exact model uses its closest model of the same level; only a provider with nothing comparable fails.
func checkProviderChain(home, root, scope string, policy *config.ProviderPolicy, catalog []string) error {
	cfg, err := readModelApplyConfig(home, root, scope)
	if err != nil {
		return err
	}
	return checkProviderPolicy(cfg, policy, catalog)
}

// checkProviderPolicy is checkProviderChain on a configuration that is already read.
func checkProviderPolicy(cfg config.Config, policy *config.ProviderPolicy, catalog []string) error {
	cfg.Providers = policy
	if _, err := config.ResolvePlan(cfg, catalog); err != nil {
		return fmt.Errorf("%w\nThat provider has no model of the same level to stand in. Choose another model with codeforge models configure, or leave that provider out", err)
	}
	return nil
}

// saveProviderPolicy writes only the providers key of the chosen scope; every other saved choice is kept.
func saveProviderPolicy(home, root, scope string, policy *config.ProviderPolicy) error {
	if scope == "global" {
		cfg, err := config.ReadFile(home)
		if err != nil {
			return err
		}
		cfg.Providers = policy
		return config.WriteFile(home, cfg)
	}
	if config.SameScope(home, root) {
		return fmt.Errorf("%w: this project path is your global configuration; use --global", errProviderUsage)
	}
	if err := config.EnsureProjectEditing(home, root, func() bool {
		return confirm("Replace the unapproved project model file? It is kept if you cancel.")
	}); err != nil {
		return err
	}
	if isSymlinked(root, filepath.Join(".codeforge", "config.yaml")) {
		return config.ErrConfigSymlink
	}
	local, err := config.ReadTrustedProject(home, root, modelTrustApprover())
	if err != nil {
		return err
	}
	if policy == nil {
		// An empty project policy overrides an inherited global one.
		policy = &config.ProviderPolicy{}
	}
	local.Providers = policy
	return configureModelFile(filepath.Dir(config.ProjectPath(root)), local, nil, func(*config.Config, []string) error { return nil }, func(_ string, content string) error {
		return config.Trust(home, config.ProjectPath(root), content)
	})
}

func showProviders(ctx context.Context, home, bin, root string, w io.Writer) error {
	// A printing command never asks to trust a project file: review mode allows it, so it must not write the trust
	// ledger. A project config that is not yet trusted is skipped here; `codeforge trust --models` approves it.
	cfg, err := config.Load(home, root, nil)
	if err != nil {
		return err
	}
	if cfg.Providers == nil || cfg.Providers.Primary == "" {
		fmt.Fprintln(w, "No default provider order is set. Model names resolve on whichever connected provider serves them.")
	} else {
		fmt.Fprintf(w, "Primary: %s\n", cfg.Providers.Primary)
		for i, backup := range cfg.Providers.Backups() {
			fmt.Fprintf(w, "Backup %d: %s\n", i+1, backup)
		}
	}
	if cfg.Providers != nil {
		for _, rule := range cfg.Providers.Rules {
			fmt.Fprintf(w, "Models %s: %s\n", rule.Match, strings.Join(append([]string{config.ProviderID(rule.Primary)}, rule.Chain().Backups()...), " > "))
		}
	}
	if catalog, err := connectedModelCatalog(ctx, bin); err == nil {
		fmt.Fprintf(w, "Connected in Empryo: %s\n", strings.Join(catalogProviders(catalog), ", "))
	}
	fmt.Fprintln(w, "Change it with: codeforge provider configure, or codeforge provider set PRIMARY [BACKUP...] [--for MODEL-GLOB PROVIDER[,PROVIDER...]]")
	return nil
}
