package src

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/fsutil"
	"github.com/codeliter/codeforge/src/guard"
)

// previousPersonaFile keeps the persona a save replaced, beside it.
const previousPersonaFile = "persona.previous.md"

// previousSourceFile keeps where that persona came from; empty when nothing was recorded for it.
const previousSourceFile = "persona.source.previous.json"

// openingSection names, in the section picker, what a persona says before its first section.
const openingSection = "@opening"

// personaSection is one "## " section of a persona; the one with no title is what comes before the first.
type personaSection struct {
	title string
	body  string
}

// splitPersona splits a persona at its "## " headings.
func splitPersona(doc string) []personaSection {
	var out []personaSection
	var cur personaSection
	var lines []string
	flush := func() {
		cur.body = strings.TrimSpace(strings.Join(lines, "\n"))
		if cur.title != "" || cur.body != "" {
			out = append(out, cur)
		}
		lines = nil
	}
	// A heading inside a fenced code block is an example the user wrote, not a section.
	var open byte // the character of the fence a line is inside, or 0
	width := 0
	for _, line := range strings.Split(doc, "\n") {
		switch char, n, rest := fenceOf(line); {
		case n == 0:
		case open == 0:
			open, width = char, n
		case char == open && n >= width && strings.TrimSpace(rest) == "":
			open = 0 // only a fence of the same kind, at least as long and with nothing after it, closes one
		}
		if heading, ok := strings.CutPrefix(line, "## "); ok && open == 0 {
			flush()
			cur = personaSection{title: strings.TrimSpace(heading)}
			continue
		}
		lines = append(lines, line)
	}
	flush()
	return out
}

// fenceOf reads a line as a code fence, as Markdown defines one: at most three spaces, then three or more backticks or
// tildes. It gives the character, how many there are and what follows them; n is 0 for any other line.
func fenceOf(line string) (char byte, n int, rest string) {
	const maxIndent, minFence = 3, 3
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > maxIndent || trimmed == "" || trimmed[0] != '`' && trimmed[0] != '~' {
		return 0, 0, ""
	}
	char = trimmed[0]
	for n < len(trimmed) && trimmed[n] == char {
		n++
	}
	if n < minFence {
		return 0, 0, ""
	}
	return char, n, trimmed[n:]
}

func joinPersona(sections []personaSection) string {
	parts := make([]string, 0, len(sections))
	for _, s := range sections {
		if s.title == "" {
			parts = append(parts, s.body)
			continue
		}
		parts = append(parts, "## "+s.title+"\n"+s.body)
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// sectionChange is how one section of a new persona differs from the saved one.
type sectionChange struct {
	title          string
	added, removed int
	fresh          bool // the saved persona has no such section
}

func (c sectionChange) name() string {
	if c.title == "" {
		return "Opening"
	}
	return c.title
}

func (c sectionChange) summary() string {
	if c.fresh {
		return "new"
	}
	return fmt.Sprintf("+%d −%d lines", c.added, c.removed)
}

// personaChanges lists the sections of next that the saved persona lacks or words differently.
func personaChanges(old, next string) []sectionChange {
	was := make(map[string]string)
	for _, s := range splitPersona(old) {
		was[s.title] = s.body
	}
	var changes []sectionChange
	for _, s := range splitPersona(next) {
		body, had := was[s.title]
		// No evidence is not a change: a relearn that could not see an activity (GitHub unreachable, another
		// repository) leaves what the persona already says about it.
		// Nor is an opening that could not be written, which is the bare title.
		if had && (body == s.body || s.body == noEvidence || s.title == "" && s.body == "# Persona") {
			continue
		}
		c := sectionChange{title: s.title, fresh: !had}
		for _, line := range strings.Split(s.body, "\n") {
			if !slices.Contains(strings.Split(body, "\n"), line) {
				c.added++
			}
		}
		for _, line := range strings.Split(body, "\n") {
			if had && !slices.Contains(strings.Split(s.body, "\n"), line) {
				c.removed++
			}
		}
		changes = append(changes, c)
	}
	return changes
}

// mergePersona is the saved persona with every section of next that take accepts put in its place. The saved order is
// kept; a section only next has goes in after the one it follows there. Sections only the saved persona has, such as
// the user's own notes, stay: a refresh never deletes what it did not learn.
func mergePersona(old, next string, take func(title string) bool) string {
	out := splitPersona(old)
	at := func(title string) int {
		return slices.IndexFunc(out, func(s personaSection) bool { return s.title == title })
	}
	after := -1 // where the last section of next sits in out
	for _, s := range splitPersona(next) {
		i := at(s.title)
		switch {
		case i >= 0 && take(s.title):
			out[i].body = s.body
		case i < 0 && !take(s.title):
			continue // a new section the user did not tick is not installed
		case i < 0 && s.title == "":
			out = slices.Insert(out, 0, s)
			i = 0
		case i < 0 && after >= 0:
			out = slices.Insert(out, after+1, s)
			i = after + 1
		case i < 0:
			out = append(out, s)
			i = len(out) - 1
		}
		after = i
	}
	return joinPersona(out)
}

// reconciled is a new persona fitted to the saved one.
type reconciled struct {
	persona []byte
	// base is the saved persona it was fitted to, as read; a save is refused when that has changed since.
	base []byte
	// keep says nothing is to be saved.
	keep bool
	// fresh says every section is as new as what was just learned: all the changes were taken and no section was
	// left as it was for want of evidence.
	fresh bool
}

var errPersonaChanged = errors.New("your persona changed while this was learning (another update, a revert or an edit); nothing was saved, run it again")

// personaSnapshot is the saved global persona as it is now, nil when there is none.
func personaSnapshot(home string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(config.Dir(home), "persona.md"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil //nolint:nilnil // no persona is a state, not a failure
	}
	if err != nil {
		return nil, fmt.Errorf("reading the saved persona: %w", err)
	}
	return data, nil
}

// reconcilePersona fits a new global persona to the one already saved: the user ticks the changed sections to take,
// and the rest stay as they are. With nobody to ask every changed section is taken; with somebody there but no picker
// to ask on, nothing is. before is the saved persona as personaSnapshot read it ahead of the learning: one changed
// since, by another update, a revert or the user's own edit, is not fitted to, since that change would be lost.
func reconcilePersona(home string, before, next []byte) (reconciled, error) {
	old, err := personaSnapshot(home)
	if err != nil {
		return reconciled{}, err
	}
	if !bytes.Equal(old, before) {
		return reconciled{}, errPersonaChanged
	}
	if old == nil {
		return reconciled{persona: next, fresh: true}, nil
	}
	if strings.TrimSpace(string(old)) == "" {
		return reconciled{persona: next, base: old, fresh: true}, nil
	}
	// A section the history showed nothing for is left as it is, so it is as old as it was.
	unlearned := 0
	was := make(map[string]string)
	for _, s := range splitPersona(string(old)) {
		was[s.title] = s.body
	}
	for _, s := range splitPersona(string(next)) {
		body, had := was[s.title]
		noOpening := s.title == "" && s.body == "# Persona"
		if had && body != s.body && (s.body == noEvidence || noOpening) {
			unlearned++
		}
	}
	// So is a learnable section the new persona does not have at all, as when a starter replaces a learned one.
	for title := range was {
		if (title == "" || slices.Contains(personaSections, title)) && !slices.ContainsFunc(splitPersona(string(next)), func(s personaSection) bool { return s.title == title }) {
			unlearned++
		}
	}
	changes := personaChanges(string(old), string(next))
	if len(changes) == 0 {
		fmt.Fprintln(os.Stderr, "codeforge: the new persona says what yours already does; nothing changes.")
		return reconciled{keep: true}, nil
	}
	taken := make(map[string]bool)
	for _, c := range changes {
		taken[c.title] = true
	}
	if interactive() {
		picked, err := pickSections(changes)
		switch {
		case errors.Is(err, forms.ErrAborted):
			return reconciled{keep: true}, nil
		case errors.Is(err, forms.ErrOldHelper), errors.Is(err, forms.ErrNoHelper):
			// A person is here but cannot be asked: their persona is not replaced on their behalf.
			fmt.Fprintln(os.Stderr, "codeforge: "+err.Error()+"\nThe sections to replace could not be offered, so nothing is replaced.")
			return reconciled{keep: true}, nil
		case err != nil:
			return reconciled{}, err
		default:
			taken = picked
		}
	}
	var updated []string
	for _, c := range changes {
		if taken[c.title] {
			updated = append(updated, c.name()+" ("+c.summary()+")")
		}
	}
	if len(updated) == 0 {
		return reconciled{keep: true}, nil
	}
	fmt.Fprintf(os.Stderr, "codeforge: updating %s. The persona it replaces is kept as %s.\n", strings.Join(updated, ", "), filepath.Join(config.Dir(home), previousPersonaFile))
	merged := mergePersona(string(old), string(next), func(title string) bool { return taken[title] })
	return reconciled{persona: []byte(merged), base: old, fresh: len(updated) == len(changes) && unlearned == 0}, nil
}

// commitPersona saves a reconciled persona and, by record, where it came from as one step: another codeforge saving
// or reverting at the same time waits, so the copy kept for revert always pairs a persona with its own source. A
// persona that changed since it was reconciled is not overwritten: the other change would be lost.
func commitPersona(ctx context.Context, home string, r reconciled, record func() error) (string, error) {
	unlock, err := guard.LockContext(ctx, config.Dir(home))
	if err != nil {
		return "", fmt.Errorf("saving the persona: %w", err)
	}
	defer unlock()
	now, err := os.ReadFile(filepath.Join(config.Dir(home), "persona.md"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("reading the saved persona: %w", err)
	}
	if !bytes.Equal(now, r.base) {
		return "", errPersonaChanged
	}
	var p string
	err = allOrNothing(home, func() error {
		if p, err = savePersona(home, r.persona); err != nil {
			return err
		}
		return record()
	})
	return p, err
}

// allOrNothing runs a change to the persona and its records and, when it fails part of the way, puts every one of
// those files back as it was and reinstalls the persona skill, so a persona is never left beside another's source.
func allOrNothing(home string, change func() error) error {
	names := []string{"persona.md", personaSourceFile, previousPersonaFile, previousSourceFile}
	before := make([][]byte, len(names))
	had := make([]bool, len(names))
	for i, name := range names {
		data, err := os.ReadFile(filepath.Join(config.Dir(home), name))
		switch {
		case err == nil:
			before[i], had[i] = data, true
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("reading %s before changing the persona: %w", name, err)
		}
	}
	err := change()
	if err == nil {
		return nil
	}
	for i, name := range names {
		path := filepath.Join(config.Dir(home), name)
		var undo error
		if had[i] {
			undo = fsutil.AtomicWriteFile(path, before[i], privateFile)
		} else if undo = os.Remove(path); errors.Is(undo, fs.ErrNotExist) {
			undo = nil
		}
		if undo != nil {
			err = errors.Join(err, fmt.Errorf("putting %s back: %w", name, undo))
		}
	}
	return errors.Join(err, installPersonaSkill(home))
}

// recordLearned records that the saved persona was learned from repos, with the repositories it was learned from
// before. Unless fresh, when all of it was just learned, the date of the earlier learning stands: sections that were
// not replaced are as old as they were, and the staleness count must still see what they have not.
func recordLearned(home string, repos []string, model string, fresh bool) error {
	src := personaSource{Kind: sourceKindLearned, Repos: slices.Clone(repos), Model: model}
	if old, known := globalPersonaSource(home, nil); known && old.Kind == sourceKindLearned {
		for _, repo := range old.Repos {
			if !slices.Contains(src.Repos, repo) {
				src.Repos = append(src.Repos, repo)
			}
		}
		if !fresh {
			src.At = old.At
		}
	}
	return savePersonaSource(home, src)
}

// pickSections asks which changed sections the new persona replaces; every one starts ticked.
func pickSections(changes []sectionChange) (map[string]bool, error) {
	value := func(title string) string {
		if title == "" {
			return openingSection
		}
		return title
	}
	var options []forms.Option
	var all []string
	for _, c := range changes {
		options = append(options, forms.Option{Label: c.name(), Detail: c.summary(), Value: value(c.title), Group: "Changed sections"})
		all = append(all, value(c.title))
	}
	answer, err := forms.Ask("persona sections", forms.Field{
		Kind: forms.Search, Multi: true, Title: "Update your persona", Header: "Section\tChange", Options: options, Values: all,
		Description: "Ticked sections are replaced by what was just learned; the rest stay as you have them. Esc keeps your persona as it is.",
	})
	if err != nil {
		return nil, err
	}
	picked := make(map[string]bool)
	for _, c := range changes {
		picked[c.title] = slices.Contains(answer.Values, value(c.title))
	}
	return picked, nil
}

// learnFrom is the repositories a global persona is learned from. Named ones are used as given. With none named it is
// this one and every repository the saved persona was learned from that is still there, so a refresh widens what the
// persona knows instead of narrowing it to wherever the command was typed.
func learnFrom(home string, repos []string) []string {
	if len(repos) != 1 || repos[0] != "." {
		return repos
	}
	src, known := globalPersonaSource(home, nil)
	if !known || src.Kind != sourceKindLearned {
		return repos
	}
	here, _ := filepath.Abs(".") //nolint:errcheck // an unresolved path only fails to match a recorded one
	for _, repo := range src.Repos {
		if info, err := os.Stat(repo); err != nil || !info.IsDir() || repo == here || !filepath.IsAbs(repo) {
			continue
		}
		if !slices.Contains(repos, repo) {
			repos = append(repos, repo)
		}
	}
	return repos
}

// absolutePaths records repositories so that they mean the same from any directory.
func absolutePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		out = append(out, p)
	}
	return out
}
