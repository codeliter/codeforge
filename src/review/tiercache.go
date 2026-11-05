package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TierCache remembers how a tree was classified, so rerunning the gate on unchanged work does not pay the judges again.
type TierCache struct {
	Dir string
}

type tierEntry struct {
	Tier   string    `json:"tier"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// TierByName maps a stored tier name back to the current definition, so a changed budget is never served stale.
func TierByName(name string) (Tier, bool) {
	for _, t := range []Tier{TierDocs, TierSmall, TierStandard, TierRisky} {
		if t.Name == name {
			return t, true
		}
	}
	return Tier{}, false
}

func (c TierCache) Get(key string, now time.Time) (Tier, string, bool) {
	data, err := os.ReadFile(filepath.Join(c.Dir, "tier-"+key+".json"))
	if err != nil {
		return Tier{}, "", false
	}
	var e tierEntry
	if err := json.Unmarshal(data, &e); err != nil || now.Sub(e.At) > cacheTTL {
		return Tier{}, "", false
	}
	t, ok := TierByName(e.Tier)
	return t, e.Reason, ok
}

// Put stores a classification. A fallback made because no judge answered is never stored: it says nothing about the tree.
func (c TierCache) Put(key string, t Tier, reason string, now time.Time) {
	if strings.Contains(reason, "no judge answered") {
		return
	}
	data, err := json.Marshal(tierEntry{Tier: t.Name, Reason: reason, At: now})
	if err != nil || os.MkdirAll(c.Dir, cacheDirPerm) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(c.Dir, "tier-"+key+".json"), data, cacheFilePerm) //nolint:errcheck // best-effort cache //nolint:errcheck // best-effort cache
}
