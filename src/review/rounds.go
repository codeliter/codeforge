package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const roundTTL = 6 * time.Hour

// MaxGateRuns is how many gate runs in a row may fail on one branch. After that the gate refuses to review again until
// a person looks at what is left: another round of the same findings costs a full review and rarely ends the loop.
const MaxGateRuns = 3

// Round is what a failed gate run left behind, so the next run reviews the delta instead of starting over.
type Round struct {
	Tree     string            `json:"tree"`
	Tier     string            `json:"tier"`
	Files    map[string]string `json:"files"`
	Blocking []Finding         `json:"blocking"`
	At       time.Time         `json:"at"`
	// Runs counts the failed gate runs on the branch since its last pass. Rounds saved before it was kept read as one.
	Runs int `json:"runs,omitempty"`
}

// FailedRuns is how many gate runs in a row have failed on the branch.
func (r Round) FailedRuns() int {
	return max(r.Runs, 1)
}

// Capped reports whether the branch has used its gate runs.
func (r Round) Capped() bool {
	return r.FailedRuns() >= MaxGateRuns
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

// Load returns the round saved under key, unless there is none or it is older than roundTTL.
func (r Rounds) Load(key string, now time.Time) (Round, bool) {
	return readFresh(r.path(key), now, roundTTL, func(rd Round) time.Time { return rd.At })
}

// Save keeps rd under key for the next run.
func (r Rounds) Save(key string, rd Round) error {
	return writeJSON(r.Dir, r.path(key), "review round", rd)
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
	s.WriteString("Files not listed are unchanged since that review, which may not have finished every reviewer, so do not assume they are correct. The brief above holds the diff of the whole change. The pack below holds the changed files that fit its size budget and up to 8 git grep hits per changed exported symbol; other callers are not in it, so check them when a finding depends on them and your step budget allows.\n")
	return s.String()
}
