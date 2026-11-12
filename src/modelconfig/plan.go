package modelconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/codeliter/codeforge/src/config"
)

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

// VerifyPlan checks that Empryo's own fallback settings match the chains the plan declares.
func VerifyPlan(ctx context.Context, bin, workspace string, plan config.ModelPlan) error {
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

// MaintenanceConfig is cfg with every model name that is not yet a provider/model id replaced by its default:
// maintenance never guesses a provider-specific ID for an unresolved name. A normal launch replaces these baseline
// templates with catalog-resolved profiles.
func MaintenanceConfig(cfg config.Config) config.Config {
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
