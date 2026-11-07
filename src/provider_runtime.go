package src

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/codeliter/codeforge/src/config"
)

func resolveModelArguments(args []string, policy *config.ProviderPolicy, catalog []string) ([]string, []config.ModelRoute, error) {
	out := append([]string(nil), args...)
	var routes []config.ModelRoute
	for i := 0; i < len(out); i++ {
		flag, value, equal := strings.Cut(out[i], "=")
		if flag == "--model" {
			at := i
			if !equal {
				if i+1 >= len(out) {
					return nil, nil, fmt.Errorf("%w: --model needs a value", config.ErrModelName)
				}
				i++
				value = out[i]
			}
			route, err := config.ResolveModelReference("command-line model", value, policy, catalog)
			if err != nil {
				return nil, nil, err
			}
			if equal {
				out[at] = "--model=" + route.Primary
			} else {
				out[i] = route.Primary
			}
			routes = append(routes, route)
		} else if flagsWithValue[flag] && !equal {
			i++
		}
	}
	return out, routes, nil
}

func argumentsNeedCatalog(args []string) bool {
	for i := 0; i < len(args); i++ {
		flag, value, equal := strings.Cut(args[i], "=")
		if flag == "--model" {
			if !equal {
				if i+1 >= len(args) {
					return true
				}
				i++
				value = args[i]
			}
			if !config.ValidModelID(value) {
				return true
			}
		} else if flagsWithValue[flag] && !equal {
			i++
		}
	}
	return false
}

func printModelPlan(w io.Writer, plan config.ModelPlan) {
	fmt.Fprintln(w, "ROLE\tPRIMARY MODEL\tBACKUP")
	for _, route := range plan.Routes {
		backup := strings.Join(markStandIns(route), " > ")
		if backup == "" {
			backup = "native Empryo policy"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", route.Role, route.Primary, backup)
	}
}

// markStandIns labels backups that serve the closest comparable model rather than the exact one.
func markStandIns(route config.ModelRoute) []string {
	out := make([]string, len(route.Fallbacks))
	for i, id := range route.Fallbacks {
		out[i] = id
		if route.StandIns[id] {
			out[i] += " (closest)"
		}
	}
	return out
}

func verifyModelPlan(ctx context.Context, bin, workspace string, plan config.ModelPlan) error {
	if len(plan.FallbackChains()) == 0 {
		return nil
	}
	native, err := readNativeFallbacks(ctx, bin, workspace)
	if err != nil {
		return err
	}
	if err := errors.Join(plan.VerifyFallbacks(native.declaredFallbacks()), plan.VerifyFallbacks(native.Effective)); err != nil {
		data, encodeErr := json.MarshalIndent(map[string]any{"modelFallback": plan.FallbackChains()}, "", "  ")
		if encodeErr != nil {
			return err
		}
		return fmt.Errorf("%w\nRequired native settings (run codeforge models apply to review scope and apply, or use Empryo /router):\n%s", err, data)
	}
	return nil
}

// Maintenance never guesses a provider-specific ID for an unresolved name.
// A normal launch replaces these baseline templates with catalog-resolved profiles.
func maintenanceProfileConfig(cfg config.Config) config.Config {
	cfg = config.Clone(cfg)
	if !config.ValidModelID(cfg.Model) {
		cfg.Model = config.DefaultModel
	}
	if !config.ValidModelID(cfg.Expert) {
		cfg.Expert = config.DefaultExpertModel
	}
	if !config.ValidModelID(cfg.Review.Model) {
		cfg.Review.Model = config.DefaultReviewModel
	}
	for job, ref := range cfg.Routing {
		if !config.ValidModelID(ref) {
			delete(cfg.Routing, job)
		}
	}
	return cfg
}
