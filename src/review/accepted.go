package review

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	acceptedTTL = 30 * 24 * time.Hour
	// acceptNearby is how far a finding may sit from an accepted one and still be the same finding: edits above it move
	// its line, and reviewers point at neighbouring lines of the same code.
	acceptNearby = 5
)

var (
	ErrNotAPlace         = errors.New("not a finding's place; give it as path:line, as the gate prints it")
	ErrTestNotAcceptable = errors.New("a failing test cannot be accepted; fix it")
	ErrNoSuchFinding     = errors.New("the last failed gate run on this branch has no blocking finding")
	ErrNotRecorded       = errors.New("your last posted review of this PR has no finding")
)

// Accepted is a blocking finding the user read and chose not to fix. The gate keeps it off its blocking list on that
// branch, so it stops coming back every round.
type Accepted struct {
	File   string    `json:"file"`
	Line   int       `json:"line"`
	What   string    `json:"what"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

type acceptedFile struct {
	Findings []Accepted `json:"findings"`
	At       time.Time  `json:"at"`
}

// AcceptedStore keeps the accepted findings per branch, under the same key as the branch's rounds.
type AcceptedStore struct {
	Dir string
}

func (s AcceptedStore) path(key string) string {
	return filepath.Join(s.Dir, key+".json")
}

// Load returns the findings accepted on the branch, none when nothing was accepted in acceptedTTL.
func (s AcceptedStore) Load(key string, now time.Time) []Accepted {
	f, _ := readFresh(s.path(key), now, acceptedTTL, func(f acceptedFile) time.Time { return f.At })
	return f.Findings
}

// Save keeps the accepted findings; with none left the file is removed.
func (s AcceptedStore) Save(key string, list []Accepted, now time.Time) error {
	if len(list) == 0 {
		if err := os.Remove(s.path(key)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing accepted findings: %w", err)
		}
		return nil
	}
	return writeJSON(s.Dir, s.path(key), "accepted findings", acceptedFile{Findings: list, At: now})
}

// IsTestFinding reports a finding Verify made from the repository's own tests. A failing test is proof, so it can never
// be accepted.
func IsTestFinding(f Finding) bool {
	return strings.HasPrefix(f.What, "the repository's own tests")
}

// matches reports whether f is the accepted finding a: the same file, within acceptNearby lines.
func (a Accepted) matches(f Finding) bool {
	return a.Covers(f.File, f.Line)
}

// Covers reports whether a finding or thread at path:line is the accepted finding a.
func (a Accepted) Covers(path string, line int) bool {
	return a.File == path && line >= a.Line-acceptNearby && line <= a.Line+acceptNearby
}

// AcceptedAt reports whether list holds an accepted finding at path:line.
func AcceptedAt(list []Accepted, path string, line int) bool {
	return slices.ContainsFunc(list, func(a Accepted) bool { return a.Covers(path, line) })
}

// Accept marks the finding the record holds at path:line as accepted, not fixing: it stops blocking, its threads count
// as settled, and later rounds on the PR leave it out.
func (rec *Record) Accept(path string, line int, reason string, now time.Time) (Accepted, error) {
	f, ok := rec.FindingAt(path, line)
	if !ok {
		return Accepted{}, fmt.Errorf("%w at %s:%d", ErrNotRecorded, path, line)
	}
	a := Accepted{File: f.Path, Line: f.Line, What: f.Text, Reason: reason, At: now}
	if i := slices.IndexFunc(rec.Accepted, func(o Accepted) bool { return o.File == a.File && o.Line == a.Line }); i >= 0 {
		rec.Accepted[i] = a
	} else {
		rec.Accepted = append(rec.Accepted, a)
	}
	for i, g := range rec.Findings {
		if g.Path == f.Path && g.Line == f.Line {
			rec.Findings[i].Blocking = false
		}
	}
	return a, nil
}

// Unaccept makes the accepted finding at path:line count again, blocking as its label says.
func (rec *Record) Unaccept(path string, line int) bool {
	list, ok := Unaccept(rec.Accepted, path, line)
	if !ok {
		return false
	}
	rec.Accepted = list
	for i, g := range rec.Findings {
		if g.Path == path && g.Line == line {
			rec.Findings[i].Blocking = isBlocking(g.Label)
		}
	}
	return true
}

// SetAside splits blocking findings into those that still block and those the user accepted. A test failure always
// blocks. An accepted finding that matched at a moved line is kept at the new line, so it follows the code as it moves.
func SetAside(blocking []Finding, list []Accepted) (kept, accepted []Finding, moved bool) {
	for _, f := range blocking {
		i := -1
		if !IsTestFinding(f) {
			for j, a := range list {
				if a.matches(f) {
					i = j
					break
				}
			}
		}
		if i < 0 {
			kept = append(kept, f)
			continue
		}
		accepted = append(accepted, f)
		if list[i].Line != f.Line {
			list[i].Line, moved = f.Line, true
		}
	}
	return kept, accepted, moved
}

// AcceptedNote tells the reviewers what the user has accepted, so they spend their steps elsewhere.
func AcceptedNote(list []Accepted) string {
	if len(list) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString("\nThese findings were read and accepted as they are; they will not be fixed. Do not report them again, at these lines or nearby:\n")
	for _, a := range list {
		fmt.Fprintf(&s, "- %s:%d %s", a.File, a.Line, a.What)
		if a.Reason != "" {
			fmt.Fprintf(&s, " (why: %s)", a.Reason)
		}
		s.WriteString("\n")
	}
	return s.String()
}

// ParsePlace reads a finding's place as the gate prints it, path:line.
func ParsePlace(place string) (string, int, error) {
	place = strings.TrimSpace(place)
	i := strings.LastIndex(place, ":")
	if i <= 0 {
		return "", 0, fmt.Errorf("%w: %q", ErrNotAPlace, place)
	}
	path, line := place[:i], place[i+1:]
	n, err := strconv.Atoi(line)
	if err != nil || n < 0 {
		return "", 0, fmt.Errorf("%w: %q", ErrNotAPlace, place)
	}
	return path, n, nil
}

// Accept adds the blocking finding of the last failed round at path:line to list. Only a finding the gate reported can be
// accepted, and never a failing test.
func Accept(list []Accepted, prev []Finding, path string, line int, reason string, now time.Time) ([]Accepted, Accepted, error) {
	for _, f := range prev {
		if f.File != path || f.Line != line {
			continue
		}
		if IsTestFinding(f) {
			return list, Accepted{}, fmt.Errorf("%w: %s:%d", ErrTestNotAcceptable, path, line)
		}
		a := Accepted{File: f.File, Line: f.Line, What: f.What, Reason: reason, At: now}
		for i, old := range list {
			if old.File == a.File && old.Line == a.Line {
				list[i] = a
				return list, a, nil
			}
		}
		return append(list, a), a, nil
	}
	return list, Accepted{}, fmt.Errorf("%w at %s:%d", ErrNoSuchFinding, path, line)
}

// Unaccept removes the accepted finding at path:line, so it blocks again.
func Unaccept(list []Accepted, path string, line int) ([]Accepted, bool) {
	for i, a := range list {
		if a.File == path && a.Line == line {
			return append(list[:i:i], list[i+1:]...), true
		}
	}
	return list, false
}
