package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const roundTTL = 6 * time.Hour

// Round is what a failed gate run left behind, so the next run reviews the delta instead of starting over.
type Round struct {
	Tree     string            `json:"tree"`
	Tier     string            `json:"tier"`
	Files    map[string]string `json:"files"`
	Blocking []Finding         `json:"blocking"`
	At       time.Time         `json:"at"`
}

// Rounds stores the last round per branch.
type Rounds struct {
	Dir string
}

func RoundKey(repo, branch, base string) string {
	return CacheKey(repo, branch, base)
}

func (r Rounds) path(key string) string {
	return filepath.Join(r.Dir, key+".json")
}

func (r Rounds) Load(key string, now time.Time) (Round, bool) {
	data, err := os.ReadFile(r.path(key))
	if err != nil {
		return Round{}, false
	}
	var rd Round
	if err := json.Unmarshal(data, &rd); err != nil || now.Sub(rd.At) > roundTTL {
		return Round{}, false
	}
	return rd, true
}

func (r Rounds) Save(key string, rd Round) error {
	data, err := json.Marshal(rd)
	if err != nil {
		return fmt.Errorf("encoding review round: %w", err)
	}
	if err := os.MkdirAll(r.Dir, cacheDirPerm); err != nil {
		return fmt.Errorf("creating review rounds: %w", err)
	}
	if err := os.WriteFile(r.path(key), data, cacheFilePerm); err != nil {
		return fmt.Errorf("writing review round: %w", err)
	}
	return nil
}

func (r Rounds) Clear(key string) {
	os.Remove(r.path(key))
}

// FileDigests fingerprints the changed files that can be sent for review. Secrets and unreadable files are skipped.
func FileDigests(repo string, files []string) map[string]string {
	out := make(map[string]string, len(files))
	for _, f := range files {
		if secretName.MatchString(f) {
			continue
		}
		data, err := readInRepo(repo, f)
		if err != nil {
			out[f] = "unreadable"
			continue
		}
		sum := sha256.Sum256(data)
		out[f] = hex.EncodeToString(sum[:])
	}
	return out
}

// ChangedSince lists the files whose content differs from the previous round, including new and removed ones.
func (r Round) ChangedSince(now map[string]string) []string {
	var out []string
	for f, d := range now {
		if r.Files[f] != d {
			out = append(out, f)
		}
	}
	for f := range r.Files {
		if _, ok := now[f]; !ok {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

// FilterDiff keeps only the per-file sections of a git diff that belong to files.
func FilterDiff(diff string, files []string) string {
	var out strings.Builder
	keep := false
	for _, line := range strings.SplitAfter(diff, "\n") {
		if rest, ok := strings.CutPrefix(line, "diff --git a/"); ok {
			path, _, _ := strings.Cut(rest, " b/")
			keep = slices.Contains(files, path)
		}
		if keep {
			out.WriteString(line)
		}
	}
	return out.String()
}

// DeltaNote tells the reviewer what the previous round found and what changed since, so it checks the fixes
// first and does not re-derive the rest of the review.
func DeltaNote(prev Round, changed []string) string {
	var s strings.Builder
	s.WriteString("\nThis is a follow-up round. A previous review of this branch found these Blocking issues:\n")
	for _, f := range prev.Blocking {
		fmt.Fprintf(&s, "- %s:%d %s\n", f.File, f.Line, f.What)
	}
	fmt.Fprintf(&s, "\nSince then these files changed: %s\n", strings.Join(changed, ", "))
	s.WriteString("First check that each previous finding is fixed or wrong. Then review the changed files and what they affect. ")
	s.WriteString("Files not listed were already reviewed and are unchanged: the pack below holds only the changed files, so read others only if a finding depends on them.\n")
	return s.String()
}
