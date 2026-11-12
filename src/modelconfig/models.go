package modelconfig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/modelcatalog"

	"github.com/codeliter/codeforge/src/gitroot"
)

var (
	errUnknownSubcommand = errors.New("unknown subcommand")
	errModelsUsage       = errors.New("usage: codeforge models set <role> <model-name or provider/model>")
)

// independentOf lists the roles whose model families a role must avoid, so reviews never share the coder's blind spots.
var independentOf = map[string][]string{
	"review.model":          {"model"},
	"review.second_opinion": {"model", "review.model"},
}

// recommend picks, per role, the first available candidates; multi-model roles never repeat a family,
// and second opinions avoid the reviewer's family so the review is truly independent.
func recommend(available []string) map[string][]string {
	return recommendKeeping(available, config.Config{})
}
func recommendConfigured(catalog []string, cfg config.Config, keep bool) map[string][]string {
	// Each model comes from the provider its own chain starts with: its rule's, else the default one.
	routed := func(id string) string { return cfg.Providers.PrimaryFor(config.ModelName(id)) }
	if cfg.Providers != nil {
		catalog = slices.DeleteFunc(slices.Clone(catalog), func(id string) bool {
			p, _, _ := strings.Cut(id, "/")
			primary := routed(id)
			return primary != "" && p != primary
		})
	}
	retained := config.Config{}
	if keep {
		retained = cfg
	}
	rec := recommendKeeping(catalog, retained)
	for key, ids := range rec {
		if keep && len(config.Current(cfg, key)) > 0 {
			continue
		}
		for i, id := range ids {
			if routed(id) != "" {
				ids[i] = config.ModelName(id)
			}
		}
	}
	return rec
}

// recommendKeeping fills roles around the ones already configured, so reviewers still avoid the
// families of models the user chose themselves.
func recommendKeeping(available []string, keep config.Config) map[string][]string {
	byName := map[string]string{}
	for _, m := range available {
		key := config.ModelKey(config.ModelName(m))
		if _, ok := byName[key]; !ok {
			byName[key] = m
		}
	}

	rec := map[string][]string{}
	for _, r := range config.Roles {
		if have := config.Current(keep, r.Key); len(have) > 0 {
			if slices.ContainsFunc(have, func(id string) bool { return !config.SuggestedModel(id) }) {
				continue
			}
			rec[r.Key] = have
			continue
		}
		avoid := map[string]bool{}
		for _, other := range independentOf[r.Key] {
			for _, m := range slices.Concat(rec[other], config.Current(keep, other)) {
				avoid[config.Family(m)] = true
			}
		}
		for _, c := range r.Candidates {
			m, ok := byName[config.ModelKey(c)]
			if !ok || avoid[config.Family(m)] {
				continue
			}
			rec[r.Key] = append(rec[r.Key], m)
			if r.Count > 1 {
				avoid[config.Family(m)] = true
			}
			if len(rec[r.Key]) == r.Count {
				break
			}
		}
	}
	return rec
}

// RunModels implements `codeforge models [recommend|apply|set <role> <model>...]`.
func RunModels(ctx context.Context, home, bin string, args []string) error {
	if len(args) > 0 && args[0] == "configure" {
		return runModelConfigure(ctx, home, bin, args[1:])
	}
	if len(args) > 0 && args[0] == "apply" {
		return runModelApply(ctx, home, bin, args[1:])
	}
	cfg, err := config.ReadFile(home)
	if err != nil {
		return err
	}

	sub := "recommend"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "set":
		rest := args[1:]
		if len(rest) == 0 {
			return fmt.Errorf("%w; roles: %s", errModelsUsage, config.RoleKeys())
		}
		key, models := rest[0], rest[1:]
		if err := config.Apply(&cfg, key, models); err != nil {
			return err
		}
		if err := config.WriteFile(home, cfg); err != nil {
			return err
		}
		fmt.Printf("%s = %s\nWrote global %s\n", key, strings.Join(models, ", "), filepath.Join(config.Dir(home), "config.yaml"))
		return nil

	case "plan":
		if len(args) != 1 {
			return fmt.Errorf("%w: codeforge models plan takes no arguments", errModelsUsage)
		}
		root, err := gitroot.Find(ctx)
		if err != nil {
			return err
		}
		cfg, err = config.Load(home, root, TrustApprover())
		if err != nil {
			return err
		}
		var catalog []string
		if config.NeedsCatalog(cfg) {
			catalog, err = modelcatalog.Connected(ctx, bin)
			if err != nil {
				return err
			}
		}
		plan, err := config.ResolvePlan(cfg, catalog)
		if err != nil {
			return err
		}
		printModelPlan(os.Stdout, plan)
		if err := VerifyPlan(ctx, bin, root, plan); err != nil {
			return err
		}
		if len(plan.FallbackChains()) > 0 {
			fmt.Fprintln(os.Stdout, "Native fallback verification passed; no native settings were changed.")
		} else {
			fmt.Fprintln(os.Stdout, "No Codeforge fallback preference to verify; native policy was not changed.")
		}
		return nil
	case "recommend", "fill":
		if sub == "recommend" {
			root, err := gitroot.Find(ctx)
			if err != nil {
				return err
			}
			// A printing command never asks to trust a project file: review mode allows it, so it must not write the
			// trust ledger. An untrusted project config is skipped here; `codeforge trust --models` approves it.
			cfg, err = config.Load(home, root, nil)
			if err != nil {
				return err
			}
		}
		available, err := modelcatalog.Available(ctx, bin)
		if err != nil {
			return err
		}
		rec := recommendConfigured(available, cfg, sub == "fill")
		printModels(os.Stdout, cfg, rec)
		if sub == "recommend" {
			fmt.Println("\nRun `codeforge models apply --recommended` to save these after confirmation, or change saved choices with `codeforge models configure` / `codeforge models set <role> <model>...`.")
			return nil
		}
		for _, r := range config.Roles {
			if sub == "fill" && len(config.Current(cfg, r.Key)) > 0 {
				continue
			}
			if len(rec[r.Key]) > 0 {
				if err := config.Apply(&cfg, r.Key, rec[r.Key]); err != nil {
					return err
				}
			}
		}
		if err := config.WriteFile(home, cfg); err != nil {
			return err
		}
		fmt.Printf("\nWrote %s\n", filepath.Join(config.Dir(home), "config.yaml"))
		return nil
	}
	return fmt.Errorf("%w %q; use configure, plan, recommend, apply, fill or set", errUnknownSubcommand, sub)
}

func printModels(w io.Writer, cfg config.Config, rec map[string][]string) {
	const padding = 2
	tw := tabwriter.NewWriter(w, 0, 0, padding, ' ', 0)
	fmt.Fprintln(tw, "ROLE\tCURRENT\tRECOMMENDED\tWHY")
	for _, r := range config.Roles {
		cur := strings.Join(config.Current(cfg, r.Key), ", ")
		if cur == "" {
			cur = "(default)"
		}
		recd := strings.Join(rec[r.Key], ", ")
		if recd == "" {
			recd = "(none available)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Key, cur, recd, r.Why)
	}
	tw.Flush()
}
