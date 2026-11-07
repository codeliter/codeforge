package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	cacheDirPerm  = 0o700
	cacheFilePerm = 0o600
	cacheTTL      = 7 * 24 * time.Hour
)

// Cache stores verdicts by content, so an unchanged tree is never reviewed twice by the same model and rubric.
type Cache struct {
	Dir string
}

type cacheEntry struct {
	Verdict Verdict   `json:"verdict"`
	At      time.Time `json:"at"`
}

func CacheKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns the verdict cached under key, unless there is none or it is older than cacheTTL.
func (c Cache) Get(key string, now time.Time) (Verdict, bool) {
	e, ok := readFresh(c.path(key), now, cacheTTL, func(e cacheEntry) time.Time { return e.At })
	return e.Verdict, ok
}

// Put caches v under key.
func (c Cache) Put(key string, v Verdict, now time.Time) error {
	return writeJSON(c.Dir, c.path(key), "cached verdict", cacheEntry{Verdict: v, At: now})
}

// Delete removes one cached verdict; a missing file is not an error.
func (c Cache) Delete(key string) {
	os.Remove(c.path(key))
}

func (c Cache) path(key string) string {
	return filepath.Join(c.Dir, key+".json")
}

// readFresh reads the JSON value at path, unless it is missing, unreadable or older than ttl by its own timestamp.
func readFresh[T any](path string, now time.Time, ttl time.Duration, at func(T) time.Time) (T, bool) {
	var v, zero T
	data, err := os.ReadFile(path)
	if err != nil {
		return zero, false
	}
	if err := json.Unmarshal(data, &v); err != nil || now.Sub(at(v)) > ttl {
		return zero, false
	}
	return v, true
}

// writeJSON writes v as JSON to path, creating dir for codeforge alone; what names the value in errors.
func writeJSON(dir, path, what string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", what, err)
	}
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return fmt.Errorf("creating %s directory: %w", what, err)
	}
	if err := os.WriteFile(path, data, cacheFilePerm); err != nil {
		return fmt.Errorf("writing %s: %w", what, err)
	}
	return nil
}

// Prune drops expired verdicts so the cache stays small.
func (c Cache) Prune(now time.Time) {
	entries, err := os.ReadDir(c.Dir)
	if errors.Is(err, fs.ErrNotExist) || err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && now.Sub(info.ModTime()) > cacheTTL {
			os.Remove(filepath.Join(c.Dir, e.Name()))
		}
	}
}
