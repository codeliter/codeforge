package src

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/codeliter/codeforge/src/config"

	"github.com/google/uuid"

	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/store"
)

var (
	errSessionMode     = errors.New("unknown historical session: specify ticket, chat, work or review explicitly once")
	errSessionArgs     = errors.New("invalid session flags: supply one session ID and do not combine resume, session-id or fork")
	errSessionRepo     = errors.New("session belongs to a different repository or workspace")
	errSessionMissing  = errors.New("empryo session is missing or unreadable; refusing to start a fresh session")
	errSessionOverride = errors.New("session mode/profile cannot override the original Codeforge launch")
)

func sessionID(args []string) (string, bool, error) {
	var id string
	var resume bool
	count := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, equal := strings.Cut(a, "=")
		switch name {
		case "--fork":
			return "", false, errSessionArgs
		case "--resume", "--session", "-s", "--session-id":
			count++
			resume = name == "--resume" || name == "--session" || name == "-s"
			if !equal {
				if i+1 >= len(args) {
					return "", false, errSessionArgs
				}
				i++
				value = args[i]
			}
			id = value
			if !safeSessionID(id) {
				return "", false, errSessionArgs
			}
		default:
			if flagsWithValue[name] && !equal {
				i++
			}
		}
	}
	if count > 1 {
		return "", false, errSessionArgs
	}
	return id, resume, nil
}

func safeSessionID(id string) bool {
	if id == "" || strings.HasPrefix(id, "-") || id == "." || id == ".." || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func sessionRepository(ctx context.Context, wd string) (string, error) {
	repo, err := guard.RepoRoot(ctx, wd)
	if err != nil {
		repo = wd
	}
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", fmt.Errorf("resolving session repository: %w", err)
	}
	return resolved, nil
}

// Empryo 3.12.3-beta uses --session-id for new TUI/headless IDs and workspace-local session directories.
func verifyEmpryoSession(wd, id string) error {
	path := filepath.Join(wd, ".empryo", "sessions", id)
	f, err := os.Open(filepath.Join(path, "meta.json"))
	if err != nil {
		return fmt.Errorf("%w: %s", errSessionMissing, id)
	}
	defer func() { _ = f.Close() }() // Metadata is read-only; close errors cannot change identity.
	var meta struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	}
	const maxMetadataBytes = 1024 * 1024
	if err := json.NewDecoder(io.LimitReader(f, maxMetadataBytes)).Decode(&meta); err != nil {
		return fmt.Errorf("%w: %s", errSessionMissing, id)
	}
	savedWD, err := filepath.EvalSymlinks(meta.CWD)
	if err != nil || meta.ID != id || savedWD != wd {
		return errSessionRepo
	}
	log, err := os.Open(filepath.Join(path, "session.jsonl"))
	if err != nil {
		return fmt.Errorf("%w: %s", errSessionMissing, id)
	}
	if err := log.Close(); err != nil {
		return fmt.Errorf("checking session log: %w", err)
	}
	return nil
}

// resolveResumeID matches Empryo's exact-ID-first, unique-prefix session lookup.
func resolveResumeID(wd, prefix string) (string, error) {
	dir := filepath.Join(wd, ".empryo", "sessions")
	if info, err := os.Stat(filepath.Join(dir, prefix)); err == nil && info.IsDir() {
		return prefix, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("%w: %s", errSessionMissing, prefix)
	}
	match := ""
	for _, entry := range entries {
		if !entry.IsDir() || !safeSessionID(entry.Name()) || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if match != "" {
			return "", fmt.Errorf("%w: ambiguous prefix %s; use a longer session ID", errSessionArgs, prefix)
		}
		match = entry.Name()
	}
	if match == "" {
		return "", fmt.Errorf("%w: %s", errSessionMissing, prefix)
	}
	return match, nil
}

func historicalMode(wd, id string) string {
	f, err := os.Open(filepath.Join(wd, ".empryo", "sessions", id, "meta.json"))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var meta struct {
		ActiveTabID string `json:"activeTabId"` //nolint:tagliatelle // Empryo's metadata field.
		Tabs        []struct {
			ID    string `json:"id"`
			Agent string `json:"agent"`
		} `json:"tabs"`
	}
	const maxMetadataBytes = 1024 * 1024
	if json.NewDecoder(io.LimitReader(f, maxMetadataBytes)).Decode(&meta) != nil {
		return ""
	}
	for _, tab := range meta.Tabs {
		if tab.ID != meta.ActiveTabID {
			continue
		}
		for _, mode := range modes {
			if tab.Agent == profileFor(mode) {
				return mode
			}
		}
	}
	return ""
}

// restoreMode runs before the picker or task preparation, regardless of configured defaults.
func restoreMode(ctx context.Context, home, explicit string, args []string) (string, bool, error) {
	id, resume, err := sessionID(args)
	if err != nil || !resume {
		return explicit, resume, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", true, fmt.Errorf("finding session workspace: %w", err)
	}
	wd, err = filepath.EvalSymlinks(wd)
	if err != nil {
		return "", true, fmt.Errorf("resolving session workspace: %w", err)
	}
	repo, err := sessionRepository(ctx, wd)
	if err != nil {
		return "", true, err
	}
	s, err := store.Open(ctx, filepath.Join(config.Dir(home), "sessions"))
	if err != nil {
		return "", true, err
	}
	defer func() { _ = s.Close() }()
	id, err = resolveResumeID(wd, id)
	if err != nil {
		return "", true, err
	}
	saved, err := s.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		if explicit == "" {
			explicit = historicalMode(wd, id)
			if explicit == "" {
				return "", true, errSessionMode
			}
		}
		if explicit == "start" {
			explicit = modeTicket
		}
		if err := verifyEmpryoSession(wd, id); err != nil {
			return "", true, err
		}
		return explicit, true, nil
	}
	if err != nil {
		return "", true, err
	}
	if saved.Repository != repo {
		return "", true, errSessionRepo
	}
	if explicit == "start" {
		explicit = modeTicket
	}
	if explicit != "" && explicit != saved.Mode {
		return "", true, errSessionOverride
	}
	if saved.Mode == "" || config.ValidMode(saved.Mode) != nil || saved.Profile != profileFor(saved.Mode) {
		return "", true, errSessionOverride
	}
	if err := verifyEmpryoSession(wd, id); err != nil {
		return "", true, err
	}
	return saved.Mode, true, nil
}

func persistSession(ctx context.Context, home, mode string, args []string) ([]string, error) {
	id, resume, err := sessionID(args)
	if err != nil {
		return nil, err
	}
	if hasFlag(args, "--fork") {
		return nil, errSessionArgs
	}
	if id == "" {
		id = uuid.NewString()
		args = append([]string{"--session-id", id}, args...)
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("finding session workspace: %w", err)
	}
	wd, err = filepath.EvalSymlinks(wd)
	if err != nil {
		return nil, fmt.Errorf("resolving session workspace: %w", err)
	}
	if resume {
		id, err = resolveResumeID(wd, id)
		if err != nil {
			return nil, err
		}
		args = dropFlag(dropFlag(dropFlag(args, "--resume"), "--session"), "-s")
		args = append([]string{"--session", id}, args...)
		if err := verifyEmpryoSession(wd, id); err != nil {
			return nil, err
		}
	} else if _, err := os.Lstat(filepath.Join(wd, ".empryo", "sessions", id)); !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: session ID already exists or is inaccessible", errSessionArgs)
	}
	repo, err := sessionRepository(ctx, wd)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(ctx, filepath.Join(config.Dir(home), "sessions"))
	if err != nil {
		return nil, err
	}
	err = s.Put(ctx, store.Session{ID: id, Mode: mode, Profile: profileFor(mode), Repository: repo})
	if err := errors.Join(err, s.Close()); err != nil {
		return nil, err
	}
	if hasFlag(args, "--headless") && !hasFlag(args, "--save-session") {
		args = append([]string{"--save-session"}, args...)
	}
	return args, nil
}
