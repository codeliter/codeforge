package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/fsutil"
	"github.com/codeliter/codeforge/src/guard"
)

// A persona is stale when the user has done this much since it was learned: it no longer describes how they work now.
const (
	staleCommits = 200
	staleAge     = 60 * 24 * time.Hour
	// nudgeFile records the day the stale notice was last shown, so a launch says it once a day at most.
	nudgeFile = "persona.nudged"
)

var (
	errNoGlobalPersona = errors.New("there is no global persona to update; set one with codeforge init --global")
	errNoPrevious      = errors.New("there is no earlier persona to put back")
	errUnknownSection  = errors.New("unknown persona section")
)

// runPersonaUpdate relearns the global persona, or the sections named, from the repositories it was learned from
// before and any named now. The changed sections are shown and only the ticked ones replaced.
func runPersonaUpdate(ctx context.Context, home, bin string, cfg config.Config, args []string) error {
	var only, repos []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--section" && i+1 < len(args):
			i++
			title, err := sectionNamed(args[i])
			if err != nil {
				return err
			}
			only = append(only, title)
		case strings.HasPrefix(args[i], "-"):
			return errPersonaUsage
		default:
			repos = append(repos, args[i])
		}
	}
	before, err := personaSnapshot(home)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(before)) == "" {
		return errNoGlobalPersona
	}
	repos = absolutePaths(repos)
	if src, known := globalPersonaSource(home, nil); known && src.Kind == sourceKindLearned {
		for _, repo := range src.Repos {
			if info, err := os.Stat(repo); err == nil && info.IsDir() && filepath.IsAbs(repo) && !slices.Contains(repos, repo) {
				repos = append(repos, repo)
			}
		}
	}
	if len(repos) == 0 {
		repos = absolutePaths([]string{"."})
	}
	persona, err := learnPersona(ctx, home, bin, cfg.Init, repos, only...)
	if err != nil {
		return err
	}
	fitted, err := reconcilePersona(home, before, persona)
	if err != nil {
		return err
	}
	if fitted.keep {
		fmt.Fprintln(os.Stderr, "codeforge: your persona is unchanged.")
		return nil
	}
	p, err := commitPersona(ctx, home, fitted, func() error {
		return recordLearned(home, repos, cfg.Init, fitted.fresh && len(only) == 0)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "codeforge: global persona updated at %s. Undo with codeforge persona revert.\n", p)
	return nil
}

// sectionNamed is the learnable section a user named, in any case.
func sectionNamed(name string) (string, error) {
	titles := make([]string, 0, len(facets))
	for _, f := range facets {
		if strings.EqualFold(f.title, strings.TrimSpace(name)) {
			return f.title, nil
		}
		titles = append(titles, f.title)
	}
	return "", fmt.Errorf("%w %q; the sections are: %s", errUnknownSection, name, strings.Join(titles, ", "))
}

// revertPersona puts back the persona the last save replaced. The one it replaces is kept in turn, so a second revert
// undoes the first.
func revertPersona(ctx context.Context, home string, w io.Writer) error {
	unlock, err := guard.LockContext(ctx, config.Dir(home))
	if err != nil {
		return fmt.Errorf("putting back the persona: %w", err)
	}
	defer unlock()
	previous, err := os.ReadFile(filepath.Join(config.Dir(home), previousPersonaFile))
	if err != nil || strings.TrimSpace(string(previous)) == "" {
		return errNoPrevious
	}
	// Read before the save, which keeps the current source in this file's place.
	source, err := os.ReadFile(filepath.Join(config.Dir(home), previousSourceFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading the earlier persona's source: %w", err)
	}
	var p string
	err = allOrNothing(home, func() error {
		if p, err = savePersona(home, previous); err != nil {
			return err
		}
		record := filepath.Join(config.Dir(home), personaSourceFile)
		if len(source) > 0 {
			return fsutil.AtomicWriteFile(record, source, privateFile)
		}
		if err := os.Remove(record); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing the source record: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("putting back the persona: %w", err)
	}
	fmt.Fprintf(w, "codeforge: put back the earlier persona at %s. Run it again to undo.\n", p)
	return nil
}

// personaStale says, in one line, why the global persona is due an update, or "" when it is not: a learned persona
// the user has since made staleCommits commits past, or one older than staleAge with any commits since.
func personaStale(ctx context.Context, home string, now time.Time) string {
	src, known := globalPersonaSource(home, nil)
	if !known || src.Kind != sourceKindLearned || src.At.IsZero() {
		return ""
	}
	n := commitsSince(ctx, src)
	age := now.Sub(src.At)
	if n < staleCommits && (age < staleAge || n == 0) {
		return ""
	}
	const day = 24 * time.Hour
	return fmt.Sprintf("learned %d days and %s of yours ago", int(age/day), plural(n, "commit"))
}

// staleNotice tells the user, once a day at most, that the persona is due an update. It never updates anything: that
// costs model calls and changes how the agent writes, so it is the user's to start.
func staleNotice(ctx context.Context, home string, w io.Writer) {
	if !interactive() {
		return
	}
	today := time.Now().Format(time.DateOnly)
	stamp := filepath.Join(config.Dir(home), nudgeFile)
	if seen, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(seen)) == today {
		return
	}
	note := personaStale(ctx, home, time.Now())
	if note == "" {
		return
	}
	fmt.Fprintf(w, "codeforge: your persona was %s. Refresh it with codeforge persona update.\n", note)
	_ = fsutil.AtomicWriteFile(stamp, []byte(today+"\n"), privateFile) //nolint:errcheck // an unwritten stamp only repeats the notice
}
