// Package modelcatalog asks Empryo which providers are ready and which models they serve, and keeps that list between
// launches.
package modelcatalog

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/effort"
)

var providerLine = regexp.MustCompile(`^(\S+)\s+Ready\b`)

// Available lists provider/model ids from `empryo --list-models`, keeping the provider order Empryo prints.
func Available(ctx context.Context, bin string) ([]string, error) {
	cmd := exec.CommandContext(ctx, bin, "--list-models")
	effort.Bound(cmd)
	out, err := cmd.Output()
	effort.Reap(cmd)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	return ParseModels(bytes.NewReader(out)), nil
}

// Contexts is each listed model's context size as a person reads it ("1M", "200K"), by model id. It is for display
// only, so a listing that cannot be read gives none.
func Contexts(ctx context.Context, bin string) map[string]string {
	cmd := exec.CommandContext(ctx, bin, "--list-models")
	effort.Bound(cmd)
	out, err := cmd.Output()
	effort.Reap(cmd)
	if err != nil {
		return nil
	}
	return ParseContexts(bytes.NewReader(out))
}

// ParseContexts reads the context sizes out of Empryo's model listing, whose lines are "provider/model  1000k ctx …".
func ParseContexts(r io.Reader) map[string]string {
	const perMillion = 1000
	sizes := make(map[string]string)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[2] != "ctx" || !config.ValidModelID(f[0]) {
			continue
		}
		k, err := strconv.Atoi(strings.TrimSuffix(f[1], "k"))
		if err != nil || !strings.HasSuffix(f[1], "k") {
			continue
		}
		if k < perMillion {
			sizes[f[0]] = strconv.Itoa(k) + "K"
			continue
		}
		sizes[f[0]] = strings.TrimSuffix(strconv.FormatFloat(float64(k)/perMillion, 'f', 1, 64), ".0") + "M"
	}
	return sizes
}

// ParseModels reads the model ids worth suggesting out of Empryo's model listing, one per line.
func ParseModels(r io.Reader) []string {
	var models []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) > 0 && config.ValidModelID(f[0]) && config.SuggestedModel(f[0]) {
			models = append(models, f[0])
		}
	}
	return models
}

// ReadyProviders lists the providers Empryo reports as ready; none when it cannot be asked.
func ReadyProviders(ctx context.Context, bin string) []string {
	cmd := exec.CommandContext(ctx, bin, "--list-providers")
	effort.Bound(cmd)
	out, err := cmd.Output()
	effort.Reap(cmd)
	if err != nil {
		return nil
	}
	return ParseReady(string(out))
}

// ParseReady reads the ready providers out of Empryo's provider listing.
func ParseReady(list string) []string {
	var ready []string
	for line := range strings.SplitSeq(list, "\n") {
		if m := providerLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			ready = append(ready, m[1])
		}
	}
	return ready
}

// Connected lists the models of the providers that are ready.
func Connected(ctx context.Context, bin string) ([]string, error) {
	return ConnectedAt(ctx, bin, "")
}

// ConnectedAt is Connected as Empryo answers from workspace, whose project settings may differ.
func ConnectedAt(ctx context.Context, bin, workspace string) ([]string, error) {
	modelsCommand := exec.CommandContext(ctx, bin, "--list-models")
	modelsCommand.Dir = workspace
	effort.Bound(modelsCommand)
	output, err := modelsCommand.Output()
	effort.Reap(modelsCommand)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	ids := ParseModels(bytes.NewReader(output))
	providersCommand := exec.CommandContext(ctx, bin, "--list-providers")
	providersCommand.Dir = workspace
	effort.Bound(providersCommand)
	providers, err := providersCommand.Output()
	effort.Reap(providersCommand)
	if err != nil {
		return nil, fmt.Errorf("listing connected providers: %w", err)
	}
	connected := make(map[string]bool)
	for _, id := range ParseReady(string(providers)) {
		connected[id] = true
	}
	var models []string
	for _, id := range ids {
		provider, _, _ := strings.Cut(id, "/")
		if connected[provider] {
			models = append(models, id)
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("%w: no connected provider has a usable model catalog; sign in with Empryo first", config.ErrNoModels)
	}
	return models, nil
}
