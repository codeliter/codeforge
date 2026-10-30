package guard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TreeID fingerprints HEAD plus every uncommitted and untracked change, so a gate pass covers exactly what was reviewed.
func TreeID(ctx context.Context, repo string) (string, error) {
	git := func(args ...string) ([]byte, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], err)
		}
		return out, nil
	}

	// Empryo keeps its own session state in .empryo, which jurors write while reviewing; it is not the user's work.
	h := sha256.New()
	for _, args := range [][]string{
		{"rev-parse", "HEAD"},
		{"diff", "HEAD", "--binary", "--", ".", ":(exclude).empryo"},
		{"ls-files", "--others", "--exclude-standard", "--", ".", ":(exclude).empryo"},
	} {
		out, err := git(args...)
		if err != nil {
			return "", err
		}
		h.Write(out)
	}

	untracked, err := git("ls-files", "--others", "--exclude-standard", "-z", "--", ".", ":(exclude).empryo")
	if err != nil {
		return "", err
	}
	// Each file contributes its path and its own digest, so moving bytes between files changes the fingerprint.
	for f := range strings.SplitSeq(strings.TrimRight(string(untracked), "\x00"), "\x00") {
		if f == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repo, f))
		if err != nil {
			data = []byte("unreadable")
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(h, "%d:%s\x00%x\n", len(f), f, sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func RepoRoot(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("finding repository root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
