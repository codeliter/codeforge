package src

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCatalogCache(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "empryo")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), execPerm))
	now := time.Now()
	models := []string{"llmgateway/claude-sonnet-5-5", "copilot/gpt-6.1-sol"}
	ready := []string{"copilot", "llmgateway"}

	require.Nil(t, readCatalogCache(home, bin, ready, now), "nothing saved yet")
	writeCatalogCache(home, bin, ready, models, now)
	require.Equal(t, models, readCatalogCache(home, bin, ready, now.Add(time.Hour)))

	info, err := os.Stat(catalogCachePath(home))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(privateFile), info.Mode().Perm())

	require.Nil(t, readCatalogCache(home, bin, ready, now.Add(catalogCacheTTL+time.Minute)), "an old list is refetched")
	require.Nil(t, readCatalogCache(home, bin, ready, now.Add(-time.Hour)), "a list from the future is not trusted")

	require.Nil(t, readCatalogCache(home, bin, []string{"copilot"}, now), "signing out of a provider refetches")
	require.Nil(t, readCatalogCache(home, bin, []string{"copilot", "openrouter", "llmgateway"}, now), "signing in to a provider refetches")
	require.Nil(t, readCatalogCache(home, bin, nil, now), "no ready provider list never trusts the cache")

	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n# upgraded\n"), execPerm))
	require.Nil(t, readCatalogCache(home, bin, ready, now), "a different Empryo build refetches")

	writeCatalogCache(home, bin, ready, models, now)
	clearCatalogCache(home)
	require.Nil(t, readCatalogCache(home, bin, ready, now))
	clearCatalogCache(home) // clearing twice is fine

	require.NoError(t, os.MkdirAll(configDir(home), privateDir))
	require.NoError(t, os.WriteFile(catalogCachePath(home), []byte("not json"), privateFile))
	require.Nil(t, readCatalogCache(home, bin, ready, now), "a damaged cache is ignored, not fatal")
	writeCatalogCache(home, bin, ready, nil, now)
	writeCatalogCache(home, filepath.Join(home, "missing"), ready, models, now)
}

func TestLaunchCatalog_UsesCacheWhenProvidersMatch(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "empryo")
	script := "#!/bin/sh\ncase \"$1\" in\n--list-providers) echo 'copilot  Ready  (X)'; echo 'llmgateway  Ready  (Y)';;\n--list-models) echo FAIL >&2; exit 1;;\nesac\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), execPerm))
	models := []string{"llmgateway/claude-sonnet-5-5"}
	writeCatalogCache(home, bin, []string{"copilot", "llmgateway"}, models, time.Now())

	got, cached, err := launchCatalog(t.Context(), home, bin)
	require.NoError(t, err, "the model list is not fetched while the cache matches")
	require.True(t, cached)
	require.Equal(t, models, got)

	// The user signed out of copilot: the saved list names a disconnected provider, so it must not be used.
	writeCatalogCache(home, bin, []string{"copilot", "llmgateway", "openrouter"}, models, time.Now())
	_, cached, err = launchCatalog(t.Context(), home, bin)
	require.Error(t, err, "a changed provider set forces a fetch, and this Empryo fails it")
	require.False(t, cached)
}

func TestFreshLaunchCatalog_AnnouncesBeforeFetching(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "empryo")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), execPerm))
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	saved := os.Stderr
	os.Stderr = writer
	t.Cleanup(func() { os.Stderr = saved })
	_, fetchErr := freshLaunchCatalog(t.Context(), home, bin, []string{"copilot"})
	require.NoError(t, writer.Close())
	os.Stderr = saved
	require.Error(t, fetchErr)
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Contains(t, string(data), "resolving model names", "the message appears even when the listing fails")
}

func TestChangesProviders(t *testing.T) {
	require.False(t, changesProviders([]string{"models"}))
	require.False(t, changesProviders([]string{"models", "plan"}))
	require.False(t, changesProviders([]string{"provider", "show"}))
	require.True(t, changesProviders([]string{"models", "apply"}))
	require.True(t, changesProviders([]string{"provider", "set", "llmgateway"}))
	require.True(t, changesProviders([]string{"providers", "configure"}))
}
