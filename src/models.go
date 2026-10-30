package src

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
)

var (
	errUnknownRole       = errors.New("unknown role")
	errNoModels          = errors.New("no models given")
	errOneModel          = errors.New("role takes exactly one model")
	errNotProviderModel  = errors.New("model is not provider/model")
	errUnknownSubcommand = errors.New("unknown subcommand")
	errModelsUsage       = errors.New("usage: codeforge models set <role> <provider/model>")
)

// role describes one model slot. Candidates are model names without the provider, best first,
// so the same list works whether the user reaches a model through copilot, anthropic or openai.
type role struct {
	key        string
	count      int
	why        string
	candidates []string
}

var roles = []role{
	{"model", 1, "writes the code; strongest coding model you have", []string{"claude-opus-5.5", "gpt-6.1-sol", "claude-opus-5", "gpt-6-sol", "claude-sonnet-5.5"}},
	{"judges", 2, "score effort before launch; cheapest fast models, two families", []string{"claude-haiku-5.5", "gpt-6-luna", "gemini-3.8-flash", "gpt-5.6-luna", "mai-code-1.1-flash", "gpt-5.4-nano"}},
	{"review.model", 1, "writes the review; strongest model from a different family than the coder", []string{"gpt-6.1-sol", "claude-opus-5.5", "gpt-5.5", "claude-opus-5", "gemini-4-argon"}},
	{"review.second_opinion", 2, "independent reviewers; frontier models from other families", []string{"kimi-k3", "gpt-6.1-sol", "gpt-5.5", "claude-opus-5.5"}},
	{"init", 1, "distils your persona once; best writer", []string{"claude-opus-5.5", "gpt-6.1-sol", "claude-sonnet-5.5"}},
}

// independentOf lists the roles whose model families a role must avoid, so reviews never share the coder's blind spots.
var independentOf = map[string][]string{
	"review.model":          {"model"},
	"review.second_opinion": {"model", "review.model"},
}

func family(model string) string {
	name := model[strings.LastIndex(model, "/")+1:]
	if i := strings.IndexAny(name, "-."); i > 0 {
		return name[:i]
	}
	return name
}

func modelName(model string) string {
	return model[strings.LastIndex(model, "/")+1:]
}

// availableModels lists provider/model ids from `empryo --list-models`, keeping the provider order Empryo prints.
func availableModels(ctx context.Context, bin string) ([]string, error) {
	out, err := exec.CommandContext(ctx, bin, "--list-models").Output()
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	return parseModelList(bytes.NewReader(out)), nil
}

func parseModelList(r io.Reader) []string {
	var models []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) > 0 && strings.Count(f[0], "/") == 1 && !strings.HasSuffix(f[0], ":") {
			models = append(models, f[0])
		}
	}
	return models
}

// recommend picks, per role, the first available candidates; multi-model roles never repeat a family,
// and second opinions avoid the reviewer's family so the review is truly independent.
func recommend(available []string) map[string][]string {
	return recommendKeeping(available, Config{})
}

// recommendKeeping fills roles around the ones already configured, so reviewers still avoid the
// families of models the user chose themselves.
func recommendKeeping(available []string, keep Config) map[string][]string {
	byName := map[string]string{}
	for _, m := range available {
		if _, ok := byName[modelName(m)]; !ok {
			byName[modelName(m)] = m
		}
	}

	rec := map[string][]string{}
	for _, r := range roles {
		if have := current(keep, r.key); len(have) > 0 {
			rec[r.key] = have
			continue
		}
		avoid := map[string]bool{}
		for _, other := range independentOf[r.key] {
			for _, m := range slices.Concat(rec[other], current(keep, other)) {
				avoid[family(m)] = true
			}
		}
		for _, c := range r.candidates {
			m, ok := byName[c]
			if !ok || avoid[family(m)] {
				continue
			}
			rec[r.key] = append(rec[r.key], m)
			if r.count > 1 {
				avoid[family(m)] = true
			}
			if len(rec[r.key]) == r.count {
				break
			}
		}
	}
	return rec
}

func current(cfg Config, key string) []string {
	switch key {
	case "model":
		return nonEmpty(cfg.Model)
	case "judges":
		return cfg.Judges
	case "review.model":
		return nonEmpty(cfg.Review.Model)
	case "review.second_opinion":
		return cfg.Review.SecondOpinion
	case "init":
		return nonEmpty(cfg.Init)
	}
	return nil
}

func apply(cfg *Config, key string, models []string) error {
	r, ok := findRole(key)
	if !ok {
		return fmt.Errorf("%w %q; roles: %s", errUnknownRole, key, roleKeys())
	}
	if len(models) == 0 {
		return fmt.Errorf("%w: %s", errNoModels, key)
	}
	if r.count == 1 && len(models) != 1 {
		return fmt.Errorf("%w: %s", errOneModel, key)
	}
	for _, m := range models {
		if strings.Count(m, "/") != 1 {
			return fmt.Errorf("%w: %q", errNotProviderModel, m)
		}
	}

	switch key {
	case "model":
		cfg.Model = models[0]
	case "judges":
		cfg.Judges = models
	case "review.model":
		cfg.Review.Model = models[0]
	case "review.second_opinion":
		cfg.Review.SecondOpinion = models
	case "init":
		cfg.Init = models[0]
	}
	return validate(*cfg)
}

func findRole(key string) (role, bool) {
	i := slices.IndexFunc(roles, func(r role) bool { return r.key == key })
	if i < 0 {
		return role{}, false
	}
	return roles[i], true
}

func roleKeys() string {
	keys := make([]string, len(roles))
	for i, r := range roles {
		keys[i] = r.key
	}
	return strings.Join(keys, ", ")
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// runModels implements `codeforge models [recommend|apply|set <role> <model>...]`.
func runModels(ctx context.Context, home, bin string, args []string) error {
	cfg, err := readConfigFile(home)
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
			return fmt.Errorf("%w; roles: %s", errModelsUsage, roleKeys())
		}
		key, models := rest[0], rest[1:]
		if err := apply(&cfg, key, models); err != nil {
			return err
		}
		if err := writeConfigFile(home, cfg); err != nil {
			return err
		}
		fmt.Printf("%s = %s\n", key, strings.Join(models, ", "))
		return nil

	case "recommend", "apply", "fill":
		available, err := availableModels(ctx, bin)
		if err != nil {
			return err
		}
		rec := recommend(available)
		if sub == "fill" {
			rec = recommendKeeping(available, cfg)
		}
		printModels(os.Stdout, cfg, rec)
		if sub == "recommend" {
			fmt.Println("\nRun `codeforge models apply` to use the recommendations, or `codeforge models set <role> <model>...`.")
			return nil
		}
		for _, r := range roles {
			if sub == "fill" && len(current(cfg, r.key)) > 0 {
				continue
			}
			if len(rec[r.key]) > 0 {
				if err := apply(&cfg, r.key, rec[r.key]); err != nil {
					return err
				}
			}
		}
		if err := writeConfigFile(home, cfg); err != nil {
			return err
		}
		fmt.Printf("\nWrote %s\n", filepath.Join(configDir(home), "config.json"))
		return nil
	}
	return fmt.Errorf("%w %q; use recommend, apply or set", errUnknownSubcommand, sub)
}

func printModels(w io.Writer, cfg Config, rec map[string][]string) {
	const padding = 2
	tw := tabwriter.NewWriter(w, 0, 0, padding, ' ', 0)
	fmt.Fprintln(tw, "ROLE\tCURRENT\tRECOMMENDED\tWHY")
	for _, r := range roles {
		cur := strings.Join(current(cfg, r.key), ", ")
		if cur == "" {
			cur = "(default)"
		}
		recd := strings.Join(rec[r.key], ", ")
		if recd == "" {
			recd = "(none available)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.key, cur, recd, r.why)
	}
	tw.Flush()
}
