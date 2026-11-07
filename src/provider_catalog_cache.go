package src

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/fsutil"
)

const (
	catalogCacheFile = "model-catalog.json"
	catalogCacheTTL  = 24 * time.Hour
)

// catalogCache is the connected-model list saved between launches. It is generated data, so it lives beside the
// config instead of inside it: config.yaml stays what the user wrote. It is keyed to the Empryo executable and to
// the set of ready providers, so an Empryo upgrade, a sign-in, a sign-out or a changed API key refreshes it.
type catalogCache struct {
	Empryo    string    `json:"empryo"`
	SavedAt   time.Time `json:"saved_at"`
	Providers []string  `json:"providers"`
	Models    []string  `json:"models"`
}

// empryoFingerprint identifies the Empryo build whose catalog was saved: path, size and modification time.
func empryoFingerprint(bin string) string {
	info, err := os.Stat(bin)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", bin, info.Size(), info.ModTime().UnixNano())
}

func catalogCachePath(home string) string {
	return filepath.Join(config.Dir(home), catalogCacheFile)
}

// readCatalogCache returns the saved models when they are fresh and belong to this Empryo, else nil.
func readCatalogCache(home, bin string, providers []string, now time.Time) []string {
	data, err := os.ReadFile(catalogCachePath(home))
	if err != nil {
		return nil
	}
	var c catalogCache
	if json.Unmarshal(data, &c) != nil || len(c.Models) == 0 || len(providers) == 0 {
		return nil
	}
	if !slices.Equal(c.Providers, providers) {
		return nil
	}
	if fp := empryoFingerprint(bin); fp == "" || c.Empryo != fp || now.Sub(c.SavedAt) > catalogCacheTTL || c.SavedAt.After(now.Add(time.Minute)) {
		return nil
	}
	return c.Models
}

// writeCatalogCache saves the catalog best-effort: a cache that cannot be written only costs the next launch a fetch.
func writeCatalogCache(home, bin string, providers, models []string, now time.Time) {
	fp := empryoFingerprint(bin)
	if fp == "" || len(models) == 0 {
		return
	}
	data, err := json.Marshal(catalogCache{Empryo: fp, SavedAt: now, Providers: providers, Models: models})
	if err != nil {
		return
	}
	if err := os.MkdirAll(config.Dir(home), privateDir); err != nil {
		return
	}
	_ = fsutil.AtomicWriteFile(catalogCachePath(home), data, privateFile) //nolint:errcheck // best-effort cache
}

// clearCatalogCache forgets the saved catalog, for commands that change which providers are connected.
func clearCatalogCache(home string) {
	if err := os.Remove(catalogCachePath(home)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "codeforge: could not clear the model cache:", err)
	}
}

// launchCatalog returns the connected-model catalog for a launch, from the cache when it is fresh. The second
// result reports whether it came from the cache, so the caller can fetch fresh and retry if a name does not resolve.
func launchCatalog(ctx context.Context, home, bin string) (models []string, cached bool, err error) {
	// Asking which providers are ready is quick; listing every model is the slow part worth caching.
	providers := sortedCopy(readyProviders(ctx, bin))
	if models = readCatalogCache(home, bin, providers, time.Now()); models != nil {
		return models, true, nil
	}
	models, err = freshLaunchCatalog(ctx, home, bin, providers)
	return models, false, err
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// freshLaunchCatalog fetches the catalog from Empryo and saves it.
func freshLaunchCatalog(ctx context.Context, home, bin string, providers []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, "codeforge: request received; resolving model names through connected providers...")
	models, err := connectedModelCatalog(ctx, bin)
	if err != nil {
		return nil, err
	}
	writeCatalogCache(home, bin, providers, models, time.Now())
	return models, nil
}

// changesProviders reports whether a models or provider command can change providers, models or fallbacks.
// Read-only subcommands keep the saved model list.
func changesProviders(args []string) bool {
	if len(args) <= 1 {
		return false
	}
	return !slices.Contains([]string{"plan", "show", "list", "recommend"}, args[1])
}
