package src

import (
	"context"
	"fmt"
	"strings"
)

func connectedModelCatalog(ctx context.Context, bin string) ([]string, error) {
	ids, err := availableModels(ctx, bin)
	if err != nil {
		return nil, err
	}
	ready := readyProviders(ctx, bin)
	connected := make(map[string]bool)
	for _, id := range ready {
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
		return nil, fmt.Errorf("%w: no connected provider has a usable model catalog; sign in with Empryo first", errNoModels)
	}
	return models, nil
}
