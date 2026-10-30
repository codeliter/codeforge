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

func (c Cache) Get(key string, now time.Time) (Verdict, bool) {
	data, err := os.ReadFile(filepath.Join(c.Dir, key+".json"))
	if err != nil {
		return Verdict{}, false
	}

	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil || now.Sub(e.At) > cacheTTL {
		return Verdict{}, false
	}
	return e.Verdict, true
}

func (c Cache) Put(key string, v Verdict, now time.Time) error {
	data, err := json.Marshal(cacheEntry{Verdict: v, At: now})
	if err != nil {
		return fmt.Errorf("encoding cached verdict: %w", err)
	}

	if err := os.MkdirAll(c.Dir, cacheDirPerm); err != nil {
		return fmt.Errorf("creating review cache: %w", err)
	}
	if err := os.WriteFile(filepath.Join(c.Dir, key+".json"), data, cacheFilePerm); err != nil {
		return fmt.Errorf("writing review cache: %w", err)
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
