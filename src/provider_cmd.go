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

	"github.com/codeliter/codeforge/src/guard"
)

var errProviderUsage = errors.New("usage: codeforge provider [show] | provider set PRIMARY [BACKUP...] [--project|--global] | provider clear [--project|--global]")

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
		ids, scope, err := providerArgs(args)
		if err != nil {
			return err
		}
		if sub == "set" && len(ids) == 0 || sub == "clear" && len(ids) > 0 {
			return errProviderUsage
		}
		if scope == "" {
			scope = "global"
			if gitErr == nil && !sameConfigScope(home, root) {
				scope = "project"
			}
		}
		var policy *ProviderPolicy
		if sub == "set" {
			catalog, err := connectedModelCatalog(ctx, bin)
			if err != nil {
				return err
			}
			if policy, err = providerChain(ids, catalogProviders(catalog)); err != nil {
				return err
			}
			if err := checkProviderChain(home, root, scope, policy, catalog); err != nil {
				return err
			}
		}
		if err := saveProviderPolicy(home, root, scope, policy); err != nil {
			return err
		}
		if policy == nil {
			fmt.Fprintf(w, "Cleared %s provider order. Model names now resolve without a preferred provider.\n", scope)
		} else {
			fmt.Fprintf(w, "Saved %s provider order: %s\n", scope, strings.Join(append([]string{policy.Primary}, policy.Backups()...), " > "))
		}
		fmt.Fprintln(w, "Run codeforge models apply to make Empryo use this order; it shows every change and asks first.")
		return nil
	case "configure":
		return configureProviders(ctx, home, bin, root, gitErr == nil, args, w)
	}
	return fmt.Errorf("%w %q; use configure, show, set or clear", errUnknownSubcommand, sub)
}

// providerChain normalises aliases and checks every provider is connected and listed once.
func providerChain(ids, connected []string) (*ProviderPolicy, error) {
	var chain []string
	for _, id := range ids {
		id = providerID(id)
		if !validProviderID(id) {
			return nil, fmt.Errorf("%w: %q is not a provider ID", errProviderPolicy, id)
		}
		if !slices.Contains(connected, id) {
			return nil, fmt.Errorf("%w: %s is not connected in Empryo (connected: %s); sign in with /login first", errProviderPolicy, id, strings.Join(connected, ", "))
		}
		if slices.Contains(chain, id) {
			return nil, fmt.Errorf("%w: %s is listed twice", errProviderPolicy, id)
		}
		chain = append(chain, id)
	}
	policy := newProviderPolicy(chain[0], chain[1:])
	return policy, validateProviderPolicy(policy)
}

// checkProviderChain resolves every saved role on every provider, so the order is never saved half-working.
func checkProviderChain(home, root, scope string, policy *ProviderPolicy, catalog []string) error {
	cfg, err := readModelApplyConfig(home, root, scope)
	if err != nil {
		return err
	}
	cfg.Providers = policy
	if _, err := resolveModelPlan(cfg, catalog); err != nil {
		return fmt.Errorf("%w\nEvery backup provider must serve the same models as the primary. Choose those models with codeforge models configure, or leave that provider out", err)
	}
	return nil
}

// saveProviderPolicy writes only the providers key of the chosen scope; every other saved choice is kept.
func saveProviderPolicy(home, root, scope string, policy *ProviderPolicy) error {
	if scope == "global" {
		cfg, err := readConfigFile(home)
		if err != nil {
			return err
		}
		cfg.Providers = policy
		return writeConfigFile(home, cfg)
	}
	if sameConfigScope(home, root) {
		return fmt.Errorf("%w: this project path is your global configuration; use --global", errProviderUsage)
	}
	if err := ensureProjectModelEditing(home, root, func() bool {
		return confirm("Replace the unapproved project model file? It is kept if you cancel.")
	}); err != nil {
		return err
	}
	if isSymlinked(root, filepath.Join(".codeforge", "config.yaml")) {
		return errConfigSymlink
	}
	local, err := readTrustedProjectConfig(home, root)
	if err != nil {
		return err
	}
	if policy == nil {
		// An empty project policy overrides an inherited global one.
		policy = &ProviderPolicy{}
	}
	local.Providers = policy
	return configureModelFile(filepath.Dir(projectConfigPath(root)), local, nil, func(*Config, []string) error { return nil }, func(_ string, content string) error {
		return trust(home, projectConfigPath(root), content)
	})
}

func showProviders(ctx context.Context, home, bin, root string, w io.Writer) error {
	cfg, err := loadConfigAt(home, root)
	if err != nil {
		return err
	}
	if cfg.Providers == nil || cfg.Providers.Primary == "" {
		fmt.Fprintln(w, "No provider order is set. Model names resolve on whichever connected provider serves them.")
	} else {
		fmt.Fprintf(w, "Primary: %s\n", cfg.Providers.Primary)
		for i, backup := range cfg.Providers.Backups() {
			fmt.Fprintf(w, "Backup %d: %s\n", i+1, backup)
		}
	}
	if catalog, err := connectedModelCatalog(ctx, bin); err == nil {
		fmt.Fprintf(w, "Connected in Empryo: %s\n", strings.Join(catalogProviders(catalog), ", "))
	}
	fmt.Fprintln(w, "Change it with: codeforge provider configure, or codeforge provider set PRIMARY [BACKUP...]")
	return nil
}
