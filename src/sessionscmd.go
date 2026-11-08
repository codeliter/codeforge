package src

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/store"
)

// `codeforge sessions` lists the sessions of this directory (with --all, of every project codeforge has run in), newest
// first, each with what tells it apart (when it was last used, its mode, its title, how much was done in it and what it
// cost), under a search box, and opens the one picked the way `codeforge --resume ID` does: in its own mode and
// directory, with codeforge's profile and hooks. Empryo's own session list knows none of that, so a codeforge session
// resumed from it starts in the wrong mode.

var (
	errSessionsUsage = errors.New("usage: codeforge sessions [--all] [--list]")
	errSessionPick   = errors.New("no such session")
)

// sessionInfo is one session as the list shows it.
type sessionInfo struct {
	ID       string
	Dir      string // the directory it was started in, where it opens again
	Mode     string
	Title    string
	Model    string
	Headless bool
	Started  time.Time
	Updated  time.Time
	Turns    int
	Cost     float64
}

// sessionMeta is the part of Empryo's meta.json the list reads.
type sessionMeta struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	CustomTitle string `json:"customTitle"` //nolint:tagliatelle // Empryo's metadata field.
	CWD         string `json:"cwd"`
	StartedAt   int64  `json:"startedAt"`   //nolint:tagliatelle // Empryo's metadata field.
	UpdatedAt   int64  `json:"updatedAt"`   //nolint:tagliatelle // Empryo's metadata field.
	ActiveTabID string `json:"activeTabId"` //nolint:tagliatelle // Empryo's metadata field.
	Tabs        []struct {
		ID          string `json:"id"`
		ActiveModel string `json:"activeModel"` //nolint:tagliatelle // Empryo's metadata field.
		TokenUsage  struct {
			Cost float64 `json:"cost"`
		} `json:"tokenUsage"` //nolint:tagliatelle // Empryo's metadata field.
	} `json:"tabs"`
}

// runSessions lists the sessions and returns the one the user picks to open, by its directory and ID, or "" when
// there is nothing to open: --list, no terminal to ask on, no session, or the user left the picker.
func runSessions(ctx context.Context, home string, args []string, w io.Writer) (dir, id string, err error) {
	list, all := false, false
	for _, a := range args {
		switch a {
		case "--list":
			list = true
		case "--all":
			all = true
		default:
			return "", "", errSessionsUsage
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("finding the workspace: %w", err)
	}
	if wd, err = filepath.EvalSymlinks(wd); err != nil {
		return "", "", fmt.Errorf("resolving the workspace: %w", err)
	}
	var sessions []sessionInfo
	var skipped int
	if all {
		sessions, skipped, err = listAllSessions(ctx, home)
	} else {
		sessions, skipped, err = listSessions(ctx, home, wd)
	}
	if err != nil {
		return "", "", err
	}
	if len(sessions) == 0 {
		where := "in " + wd + ". Sessions belong to the directory they were started in; --all lists every project's"
		if all {
			where = "that codeforge started"
		}
		fmt.Fprintln(w, "codeforge: no sessions to open "+where+".")
		return "", "", nil
	}
	now := time.Now()
	if !list && interactive() {
		picked, err := pickSession(sessions, now, all, wd)
		if !errors.Is(err, forms.ErrOldHelper) && !errors.Is(err, forms.ErrNoHelper) {
			if err != nil || picked < 0 {
				return "", "", err
			}
			return sessions[picked].Dir, sessions[picked].ID, nil
		}
		// Without a forms helper that can search, the list is printed: it is all the picker would show.
		fmt.Fprintln(w, "codeforge: "+err.Error()+"\nThe sessions are listed instead.")
	}
	fmt.Fprintln(w, sessionHeader(all))
	for _, s := range sessions {
		fmt.Fprintln(w, sessionLabel(s, now, all)) // ends with the ID's first characters, which --resume takes as a prefix
	}
	if skipped > 0 {
		fmt.Fprintf(w, "(%s left out: empty, or not started by codeforge so their mode is unknown)\n", plural(skipped, "session"))
	}
	fmt.Fprintln(w, "A mode with * ran headless, as a review or a gate does; opening it continues in its chat.")
	if !list {
		fmt.Fprintln(w, "Open one with: codeforge --resume ID, in the directory it was started in")
	}
	return "", "", nil
}

// pickSession shows the sessions under a search box and returns the index of the one picked, -1 when the user left.
func pickSession(sessions []sessionInfo, now time.Time, all bool, wd string) (int, error) {
	options := make([]forms.Option, len(sessions))
	for i, s := range sessions {
		options[i] = forms.Option{Label: sessionLabel(s, now, all), Value: strconv.Itoa(i)}
	}
	where := "in " + filepath.Base(wd)
	if all {
		where = "in every project"
	}
	a, err := forms.Ask("sessions", forms.Field{
		Kind: forms.Search, Title: "Open a session",
		Description: fmt.Sprintf("%s %s, newest first. * ran headless (a review or a gate).", plural(len(sessions), "session"), where),
		Header:      sessionHeader(all),
		Placeholder: "Type to search by title, mode, project, model, day or ID",
		Options:     options, Value: "0",
	})
	if errors.Is(err, forms.ErrAborted) {
		return -1, nil
	}
	if err != nil {
		return -1, err
	}
	i, err := strconv.Atoi(a.Value)
	if err != nil || i < 0 || i >= len(sessions) {
		return -1, fmt.Errorf("%w: the picker answered %q", errSessionPick, a.Value)
	}
	return i, nil
}

// listAllSessions reads the sessions codeforge started in every project it has a record of, newest first. A session
// lives in the directory it was started in, and the record knows only its project's root, so a session started in a
// subdirectory is listed from that subdirectory, not here.
func listAllSessions(ctx context.Context, home string) (sessions []sessionInfo, skipped int, err error) {
	st, err := store.Open(ctx, filepath.Join(config.Dir(home), "sessions"))
	if err != nil {
		return nil, 0, err
	}
	saved, err := st.Sessions(ctx)
	if err := errors.Join(err, st.Close()); err != nil {
		return nil, 0, err
	}
	for _, rec := range saved {
		if !safeSessionID(rec.ID) || rec.Repository == "" {
			continue
		}
		path := filepath.Join(rec.Repository, ".empryo", "sessions", rec.ID)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue // removed, or started in a subdirectory: listed from there
		}
		s, ok := readSession(path, rec.Repository)
		if !ok {
			skipped++
			continue
		}
		s.Mode = rec.Mode
		if s.Mode == "" || s.Turns == 0 {
			skipped++
			continue
		}
		sessions = append(sessions, s)
	}
	slices.SortFunc(sessions, func(a, b sessionInfo) int { return b.Updated.Compare(a.Updated) })
	return sessions, skipped, nil
}

// listSessions reads the sessions Empryo keeps in wd, newest first. A session with nothing in it, or one codeforge
// cannot open in its own mode (started by Empryo directly, from another directory, or missing its log), is left out
// and counted in skipped.
func listSessions(ctx context.Context, home, wd string) (sessions []sessionInfo, skipped int, err error) {
	dir := filepath.Join(wd, ".empryo", "sessions")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("reading sessions: %w", err)
	}
	repo, err := sessionRepository(ctx, wd)
	if err != nil {
		return nil, 0, err
	}
	st, err := store.Open(ctx, filepath.Join(config.Dir(home), "sessions"))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = st.Close() }() // read only; a close error changes nothing listed
	for _, e := range entries {
		if !e.IsDir() || !safeSessionID(e.Name()) {
			continue
		}
		s, ok := readSession(filepath.Join(dir, e.Name()), wd)
		if !ok {
			skipped++
			continue
		}
		saved, err := st.Get(ctx, s.ID)
		switch {
		case err == nil && saved.Repository == repo:
			s.Mode = saved.Mode
		case errors.Is(err, store.ErrNotFound):
			s.Mode = historicalMode(wd, s.ID)
		}
		// Any other record (another repository's, or one that cannot be read) leaves the mode unknown: skipped below.
		if s.Mode == "" || s.Turns == 0 {
			skipped++
			continue
		}
		sessions = append(sessions, s)
	}
	slices.SortFunc(sessions, func(a, b sessionInfo) int { return b.Updated.Compare(a.Updated) })
	return sessions, skipped, nil
}

// readSession reads one session's metadata and log; ok is false when it is not a session of wd that can be resumed.
func readSession(path, wd string) (sessionInfo, bool) {
	f, err := os.Open(filepath.Join(path, "meta.json"))
	if err != nil {
		return sessionInfo{}, false
	}
	defer func() { _ = f.Close() }() // read only
	var meta sessionMeta
	const maxMetadataBytes = 1024 * 1024
	if json.NewDecoder(io.LimitReader(f, maxMetadataBytes)).Decode(&meta) != nil || meta.ID != filepath.Base(path) {
		return sessionInfo{}, false
	}
	if cwd, err := filepath.EvalSymlinks(meta.CWD); err != nil || cwd != wd {
		return sessionInfo{}, false
	}
	s := sessionInfo{
		ID:      meta.ID,
		Dir:     wd,
		Title:   sessionTitle(cmp.Or(meta.CustomTitle, meta.Title)),
		Started: time.UnixMilli(meta.StartedAt),
		Updated: time.UnixMilli(max(meta.UpdatedAt, meta.StartedAt)),
	}
	for _, t := range meta.Tabs {
		s.Cost += t.TokenUsage.Cost
		if t.ID == meta.ActiveTabID || s.Model == "" {
			s.Model = t.ActiveModel
		}
	}
	var ok bool
	s.Turns, s.Headless, ok = readSessionLog(filepath.Join(path, "session.jsonl"))
	return s, ok
}

// sessionTitle is a session's title as the list shows it. Empryo titles a headless run by its prompt, and every review's
// prompt says how to review after naming what: only the what tells two reviews apart.
func sessionTitle(title string) string {
	title = strings.TrimSpace(title)
	if before, _, found := strings.Cut(title, " with the codeforge-"); found && strings.HasPrefix(before, "Review ") {
		return before
	}
	return title
}

// readSessionLog counts the user's turns in a session log and reads whether it ran headless (a review, a gate), from
// the log's first line. It reads line starts only, so a long log costs one pass and little memory.
func readSessionLog(path string) (turns int, headless, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, false
	}
	defer func() { _ = f.Close() }() // read only
	const bufferBytes = 64 * 1024
	r := bufio.NewReaderSize(f, bufferBytes)
	userLine, first, atStart := []byte(`{"k":"user"`), true, true
	for {
		line, err := r.ReadSlice('\n')
		if atStart {
			if first {
				headless = bytes.Contains(line, []byte(`"host":"headless"`))
				first = false
			}
			if bytes.HasPrefix(line, userLine) {
				turns++
			}
		}
		// A line longer than the buffer comes in pieces; only the first piece starts a line.
		atStart = !errors.Is(err, bufio.ErrBufferFull)
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			break
		}
	}
	return turns, headless, true
}

// The columns of the session list.
const (
	whenWidth    = 16
	modeWidth    = 7
	projectWidth = 18
	titleWidth   = 44
)

// sessionHeader names the columns sessionLabel lays a session out in.
func sessionHeader(withProject bool) string {
	return sessionColumns("WHEN", "MODE", "PROJECT", "TITLE", "TURNS · TIME · COST · MODEL · ID", withProject)
}

func sessionColumns(when, mode, project, title, details string, withProject bool) string {
	line := fmt.Sprintf("%-*s %-*s ", whenWidth, when, modeWidth, mode)
	if withProject {
		line += fmt.Sprintf("%-*s ", projectWidth, clip(project, projectWidth))
	}
	return line + fmt.Sprintf("%-*s  %s", titleWidth, clip(title, titleWidth), details)
}

// clip shortens s to width runes, marking the cut.
func clip(s string, width int) string {
	if r := []rune(s); len(r) > width {
		return string(r[:width-1]) + "…"
	}
	return s
}

// sessionLabel is one session as the list shows it: when it was last used, its mode, its project (with --all), its
// title, then how much was done in it, for how long, at what cost, with which model, and the start of its ID.
func sessionLabel(s sessionInfo, now time.Time, withProject bool) string {
	mode := s.Mode
	if s.Headless {
		mode += "*" // ran headless, as a review or a gate does; resuming opens its chat
	}
	details := []string{plural(s.Turns, "turn")}
	if d := s.Updated.Sub(s.Started); d >= time.Minute {
		details = append(details, shortDuration(d))
	}
	if s.Cost > 0 {
		details = append(details, fmt.Sprintf("$%.2f", s.Cost))
	}
	if s.Model != "" {
		details = append(details, config.ModelName(s.Model))
	}
	const shortID = 8
	details = append(details, s.ID[:min(shortID, len(s.ID))])
	return sessionColumns(whenLabel(s.Updated, now), mode, filepath.Base(s.Dir), cmp.Or(s.Title, "(untitled)"), strings.Join(details, " · "), withProject)
}

// whenLabel says when a session was last used: how long ago within the hour, the time today, the day and time within
// the week, the date before that.
func whenLabel(t, now time.Time) string {
	t = t.In(now.Location())
	d := now.Sub(t)
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	today := y1 == y2 && m1 == m2 && d1 == d2
	const week = 7 * 24 * time.Hour
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case today:
		return "today " + t.Format("15:04")
	case now.AddDate(0, 0, -1).Format(time.DateOnly) == t.Format(time.DateOnly):
		return "yesterday " + t.Format("15:04")
	case d < week:
		return t.Format("Mon 15:04")
	case y1 == y2:
		return t.Format("2 Jan 15:04")
	}
	return t.Format("2 Jan 2006")
}

// shortDuration is a duration in its two largest units, such as 1h12m or 3d4h.
func shortDuration(d time.Duration) string {
	const day = 24 * time.Hour
	switch {
	case d >= day:
		return fmt.Sprintf("%dd%dh", int(d/day), int(d%day/time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dm", int(d/time.Minute))
}

// sameDir reports whether a and b are the same directory, symlinks resolved.
func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
