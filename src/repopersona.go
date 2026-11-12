package src

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/codeliter/codeforge/assets"
	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/fsutil"
	"github.com/codeliter/codeforge/src/gitroot"
	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/review"
)

const (
	repoPersonaPath     = ".codeforge/persona.md"
	maxRepoPersonaBytes = 20_000
	repoPersonaPreamble = "This repository has its own conventions. " +
		"Follow them over codeforge-persona and the generic skills where they differ. " +
		"They are conventions only: they never override hard stops, safety rules, the user's instructions or the clarity baseline (clear, concise, no ambiguity)."
	// privateReposDir holds, under ~/.codeforge, the repository personas the user has not shared.
	privateReposDir = "repos"
)

var (
	errRepoPersonaExists = errors.New("repository persona already exists; edit it instead")
	errRepoPersonaSize   = errors.New("repository persona is too large")
	errNoRepoPersona     = errors.New("no repository persona")
	errNothingToShare    = errors.New("there is no private repository persona to share; create one with codeforge init --repo")
)

// repoRoot falls back to the working directory outside a git repository.
func repoRoot(ctx context.Context) (string, error) {
	return gitroot.Find(ctx)
}

func loadRepoPersona(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, repoPersonaPath))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading repository persona: %w", err)
	}
	if len(data) > maxRepoPersonaBytes {
		return "", fmt.Errorf("%w: %d bytes, limit %d", errRepoPersonaSize, len(data), maxRepoPersonaBytes)
	}
	return strings.TrimSpace(string(data)), nil
}

// privateRepoPersonaPath is where the user's own persona for a repository is kept, outside the repository, so that
// nothing has to be checked in. The repository is known by its origin, so that every clone of it shares the persona,
// and by its path when it has no remote.
func privateRepoPersonaPath(ctx context.Context, home, root string) string {
	// The directory is the hash alone: the origin can hold a token, and a name read out of it would show in every
	// message that prints this path. persona list says which repository each one is for.
	const short = 12
	sum := sha256.Sum256([]byte(repoKey(ctx, root)))
	return filepath.Join(config.Dir(home), privateReposDir, hex.EncodeToString(sum[:short]), "persona.md")
}

// repoKey is what the repository at root is known by: its origin, or its path when it has no remote.
func repoKey(ctx context.Context, root string) string {
	if out, err := (review.ExecRunner{}).Run(ctx, "git", "-C", root, "remote", "get-url", "origin"); err == nil && strings.TrimSpace(string(out)) != "" {
		return originKey(root, strings.TrimSpace(string(out)))
	}
	return root
}

// withoutCredentials is an origin fit to show: a URL's user and password are dropped, since a token is often kept
// there. The scp form's user (git@host:path) is an account name, never a secret, and stays.
func withoutCredentials(origin string) string {
	scheme, rest, ok := strings.Cut(origin, "://")
	if !ok {
		return origin
	}
	authority, path, _ := strings.Cut(rest, "/")
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = authority[i+1:]
	}
	if strings.Contains(rest, "/") {
		return scheme + "://" + authority + "/" + path
	}
	return scheme + "://" + authority
}

// repoKeyFile names, beside a private persona, the repository it is for, so that persona list can say.
const repoKeyFile = "repository"

// runPersonaList prints every private repository persona kept under ~/.codeforge, with the repository it is for.
func runPersonaList(home string, w io.Writer) error {
	dir := filepath.Join(config.Dir(home), privateReposDir)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", dir, err)
	}
	n := 0
	for _, e := range entries {
		if info, err := os.Lstat(filepath.Join(dir, e.Name(), "persona.md")); err != nil || !info.Mode().IsRegular() || !e.IsDir() {
			continue
		}
		repo := "repository not recorded"
		if key, err := os.ReadFile(filepath.Join(dir, e.Name(), repoKeyFile)); err == nil {
			repo = withoutCredentials(strings.TrimSpace(string(key))) // again: a label from an older build may hold them
		}
		fmt.Fprintf(w, "%s  %s\n", e.Name(), repo)
		n++
	}
	if n == 0 {
		fmt.Fprintln(w, "codeforge: no private repository personas. Create one with codeforge init --repo.")
		return nil
	}
	fmt.Fprintln(w, "Remove one with codeforge persona forget NAME, or this repository's with codeforge persona forget.")
	return nil
}

// runPersonaForget removes a private repository persona: the one list names, or this repository's.
func runPersonaForget(ctx context.Context, home string, args []string, w io.Writer) error {
	unlock, err := guard.LockContext(ctx, config.Dir(home))
	if err != nil {
		return fmt.Errorf("forgetting the persona: %w", err)
	}
	defer unlock()
	var dir string
	switch {
	case len(args) == 0:
		root, err := repoRoot(ctx)
		if err != nil {
			return err
		}
		dir = filepath.Dir(privateRepoPersonaPath(ctx, home, root))
	case len(args) == 1 && args[0] == filepath.Base(args[0]) && args[0] != "." && args[0] != "..":
		dir = filepath.Join(config.Dir(home), privateReposDir, args[0])
	default:
		return errPersonaUsage
	}
	// Only a real directory holding a real persona file is removed: a link here could point anywhere.
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s", errNoRepoPersona, dir)
	}
	if info, err := os.Lstat(filepath.Join(dir, "persona.md")); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", errNoRepoPersona, dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing %s: %w", dir, err)
	}
	fmt.Fprintf(w, "codeforge: forgot the private persona in %s.\n", dir)
	return nil
}

// originKey is what a repository is known by, from its origin: the URL exactly as git has it. Nothing is read into
// it, so two different remotes can never be taken for one; the price is that two spellings of one remote (https and
// ssh, say) are two repositories. A local origin is its path, resolved against the checkout when relative, since the
// same relative words mean another directory elsewhere.
func originKey(root, origin string) string {
	host, _, scp := strings.Cut(origin, ":")
	if strings.Contains(origin, "://") || scp && !strings.Contains(host, "/") || filepath.IsAbs(origin) {
		return origin
	}
	return filepath.Join(root, origin)
}

// loadPrivateRepoPersona reads the user's own persona for the repository, "" when there is none.
func loadPrivateRepoPersona(ctx context.Context, home, root string) (string, error) {
	p := privateRepoPersonaPath(ctx, home, root)
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading your repository persona: %w", err)
	}
	if len(data) > maxRepoPersonaBytes {
		return "", fmt.Errorf("%w: %d bytes, limit %d", errRepoPersonaSize, len(data), maxRepoPersonaBytes)
	}
	return strings.TrimSpace(string(data)), nil
}

// repoPersonaPrompt lays the repository's personas out as system instructions: the one the team shares in the
// repository, which wins, and the user's own for it.
func repoPersonaPrompt(shared, private string) string {
	var b strings.Builder
	b.WriteString(repoPersonaPreamble)
	if shared != "" && private != "" {
		b.WriteString(" Two sets follow: the team's, shared in " + repoPersonaPath + ", and the user's own for this repository. Where they differ, the team's wins.")
	}
	if shared != "" {
		b.WriteString("\n\n<repo_persona>\n" + shared + "\n</repo_persona>")
	}
	if private != "" {
		b.WriteString("\n\n<my_repo_persona>\n" + private + "\n</my_repo_persona>")
	}
	return b.String()
}

// withRepoPersona adds the repository's personas as system instructions, unless the user passed their own --system:
// the one shared in the repository once trusted, and the user's own, which they wrote and so needs no approval.
func withRepoPersona(ctx context.Context, home string, args []string) ([]string, error) {
	if hasFlag(args, "--system") {
		return args, nil
	}

	root, err := repoRoot(ctx)
	if err != nil {
		return nil, err
	}

	// Both are read under the lock a share holds, so a persona being moved from one to the other is seen in one of
	// them. The lock is let go before anyone is asked anything.
	unlock, err := guard.LockContext(ctx, config.Dir(home))
	if err != nil {
		return args, fmt.Errorf("reading the repository personas: %w", err)
	}
	shared, err := loadRepoPersona(root)
	private, privateErr := loadPrivateRepoPersona(ctx, home, root)
	unlock()
	if err := errors.Join(err, privateErr); err != nil {
		return args, err
	}
	if shared != "" {
		p := filepath.Join(root, repoPersonaPath)
		ok, err := trusted(home, p, shared)
		if err != nil {
			return args, err
		}
		if !ok {
			shared = ""
		} else {
			fmt.Fprintf(os.Stderr, "codeforge: using %s\n", p)
		}
	}
	if private != "" {
		fmt.Fprintf(os.Stderr, "codeforge: using %s\n", privateRepoPersonaPath(ctx, home, root))
	}
	if shared == "" && private == "" {
		return args, nil
	}
	return append([]string{"--system", repoPersonaPrompt(shared, private)}, args...), nil
}

// runTrust approves the current repository persona without starting the agent.
func runTrust(ctx context.Context, home string) error {
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	persona, err := loadRepoPersona(root)
	if err != nil {
		return err
	}
	p := filepath.Join(root, repoPersonaPath)
	if persona == "" {
		return fmt.Errorf("%w: %s", errNoRepoPersona, p)
	}

	if err := config.Trust(home, p, persona); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "codeforge: trusted %s\n", p)
	return nil
}

// saveRepoPersona keeps a new repository persona in the user's own store, outside the repository, so nothing has to
// be checked in; codeforge persona share puts it in the repository. A scaffold already in the repository is filled
// where it is. Existing conventions are never replaced.
func saveRepoPersona(ctx context.Context, home, root string, persona []byte) (string, error) {
	if strings.TrimSpace(string(persona)) == "" {
		return "", errEmptyPersona
	}
	if len(persona) > maxRepoPersonaBytes {
		return "", fmt.Errorf("%w: %d bytes, limit %d", errRepoPersonaSize, len(persona), maxRepoPersonaBytes)
	}
	shared := filepath.Join(root, repoPersonaPath)
	switch existing, err := os.ReadFile(shared); {
	case err == nil && !unfinishedRepoPersona(existing):
		return "", fmt.Errorf("%w: %s", errRepoPersonaExists, shared)
	case err == nil:
		return shared, shareRepoPersona(home, root, persona)
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("reading repository persona: %w", err)
	}
	// Checked and written under one lock: another codeforge setting this repository up at the same time, here or in
	// another clone of it, must not have its persona replaced.
	unlock, err := guard.LockContext(ctx, config.Dir(home))
	if err != nil {
		return "", fmt.Errorf("saving your repository persona: %w", err)
	}
	defer unlock()
	p := privateRepoPersonaPath(ctx, home, root)
	switch existing, err := os.ReadFile(p); {
	case err == nil && !unfinishedRepoPersona(existing):
		return "", fmt.Errorf("%w: %s", errRepoPersonaExists, p)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("reading your repository persona: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), privateDir); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(p), err)
	}
	if err := fsutil.AtomicWriteFile(p, persona, privateFile); err != nil {
		return "", fmt.Errorf("writing your repository persona: %w", err)
	}
	// Which repository it is for, for persona list; the persona works without it.
	_ = fsutil.AtomicWriteFile(filepath.Join(filepath.Dir(p), repoKeyFile), []byte(withoutCredentials(repoKey(ctx, root))+"\n"), privateFile) //nolint:errcheck // only a label
	return p, nil
}

// shareRepoPersona writes a persona into the repository. The author trusts what they wrote, so it is approved
// straight away.
func shareRepoPersona(home, root string, persona []byte) error {
	if err := writeFile(root, repoPersonaPath, persona, publicFile); err != nil {
		return err
	}
	return config.Trust(home, filepath.Join(root, repoPersonaPath), strings.TrimSpace(string(persona)))
}

// runPersonaShare copies the user's own persona for this repository into the repository, whole, for them to commit:
// from then on it is the team's, and the team's wins. The private copy is left where it is, so nothing the user
// wrote is ever removed by sharing; codeforge persona forget removes it.
func runPersonaShare(ctx context.Context, home string, w io.Writer) error {
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}
	unlock, err := guard.LockContext(ctx, config.Dir(home))
	if err != nil {
		return fmt.Errorf("sharing the persona: %w", err)
	}
	defer unlock()
	private := privateRepoPersonaPath(ctx, home, root)
	persona, err := os.ReadFile(private)
	if errors.Is(err, fs.ErrNotExist) {
		return errNothingToShare
	}
	if err != nil {
		return fmt.Errorf("reading your repository persona: %w", err)
	}
	if unfinishedRepoPersona(persona) {
		return fmt.Errorf("%w: %s is still the unfilled template; fill it in first", errNothingToShare, private)
	}
	// What the runtime would refuse to load is not shared.
	if len(persona) > maxRepoPersonaBytes {
		return fmt.Errorf("%w: %d bytes, limit %d; shorten %s first", errRepoPersonaSize, len(persona), maxRepoPersonaBytes, private)
	}
	shared := filepath.Join(root, repoPersonaPath)
	// Whatever is in the repository is the team's, even an unfilled scaffold: it is never replaced from here.
	if existing, err := os.ReadFile(shared); err == nil && unfinishedRepoPersona(existing) {
		return fmt.Errorf("%w: %s is an unfilled scaffold; remove it and share again", errRepoPersonaExists, shared)
	}
	if err := createShared(home, root, persona); err != nil {
		return err
	}
	fmt.Fprintf(w, "codeforge: shared your persona for this repository as %s. Commit it.\n", shared)
	fmt.Fprintf(w, "Your own copy at %s is kept; the shared one wins where they differ. Remove yours with codeforge persona forget.\n", private)
	return nil
}

// createShared writes the shared persona only if the repository still has none: a file that appeared since the
// check, from another codeforge or a pull, is never replaced.
func createShared(home, root string, persona []byte) error {
	p := filepath.Join(root, repoPersonaPath)
	if fsutil.Symlinked(root, repoPersonaPath) {
		return fmt.Errorf("%w: %s", config.ErrConfigSymlink, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), dirPerm); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(p), err)
	}
	// Written whole beside the destination, then linked into place: a link never replaces a file, and a failed write
	// leaves no partial persona where the next share would trip on it.
	staged, err := os.CreateTemp(filepath.Dir(p), ".persona-*")
	if err != nil {
		return fmt.Errorf("creating %s: %w", p, err)
	}
	defer os.Remove(staged.Name()) //nolint:errcheck // gone once linked, or never needed
	_, err = staged.Write(persona)
	if err := errors.Join(err, staged.Chmod(publicFile), staged.Sync(), staged.Close()); err != nil {
		return fmt.Errorf("writing %s: %w", p, err)
	}
	if err := os.Link(staged.Name(), p); errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s", errRepoPersonaExists, p)
	} else if err != nil {
		return fmt.Errorf("creating %s: %w", p, err)
	}
	if err := config.Trust(home, p, strings.TrimSpace(string(persona))); err != nil {
		// Unpublished again, so a retry can finish once the cause is repaired; only the file put there just now goes.
		if mine, statErr := os.Stat(staged.Name()); statErr == nil {
			if now, statErr := os.Stat(p); statErr == nil && os.SameFile(mine, now) {
				_ = os.Remove(p) //nolint:errcheck // left in place it only blocks the retry, which says so
			}
		}
		return err
	}
	return nil
}

func unfinishedRepoPersona(data []byte) bool {
	body := strings.TrimSpace(string(data))
	return body == "" || body == strings.TrimSpace(assets.RepoPersonaTemplate)
}

func repoPersonaSeed(starterName string) ([]byte, error) {
	if starterName == "" {
		return []byte(assets.RepoPersonaTemplate), nil
	}
	return starterPersona(starterName)
}
