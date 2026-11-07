package src

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/codeliter/codeforge/src/config"

	"github.com/codeliter/codeforge/src/effort"
)

func connectedModelCatalog(ctx context.Context, bin string) ([]string, error) {
	return connectedModelCatalogAt(ctx, bin, "")
}
func connectedModelCatalogAt(ctx context.Context, bin, workspace string) ([]string, error) {
	modelsCommand := exec.CommandContext(ctx, bin, "--list-models")
	modelsCommand.Dir = workspace
	effort.Bound(modelsCommand)
	output, err := modelsCommand.Output()
	effort.Reap(modelsCommand)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	ids := parseModelList(bytes.NewReader(output))
	providersCommand := exec.CommandContext(ctx, bin, "--list-providers")
	providersCommand.Dir = workspace
	effort.Bound(providersCommand)
	providers, err := providersCommand.Output()
	effort.Reap(providersCommand)
	if err != nil {
		return nil, fmt.Errorf("listing connected providers: %w", err)
	}
	connected := make(map[string]bool)
	for _, id := range parseReady(string(providers)) {
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
