package src

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/fsutil"
)

// The global persona is kept as one file a section under ~/.codeforge/persona, so each part can be read, edited and
// loaded on its own. A persona saved by an older codeforge is the single file persona.md; it is read as it is and
// becomes a directory the next time it is saved.
const (
	personaDirName    = "persona"
	legacyPersonaFile = "persona.md"
	// openingFile holds what a persona says before its first section.
	openingFile = "about.md"
	// storeLockFile is locked, shared to read and alone to write, so a reader gets one whole persona.
	storeLockFile = "persona.lock"
	// savingFile holds the whole persona being saved, from before the first section file is written until after the
	// last: a save cut short is finished from it, and until then it is what readers are given.
	savingFile = "persona.saving.md"
)

func personaDir(home string) string {
	return filepath.Join(config.Dir(home), personaDirName)
}

// isSectionFile reports whether a directory entry is a section of the persona: a regular Markdown file. Links,
// directories and other files in the directory are the user's and are neither read nor removed.
func isSectionFile(e fs.DirEntry) bool {
	return e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".md")
}

// personaPath is where the global persona is, by the same test the reader uses: its directory when that holds a
// section file or a save is under way, else the single file of one not yet saved by this version.
func personaPath(home string) string {
	if _, err := os.Stat(filepath.Join(config.Dir(home), savingFile)); err == nil {
		return personaDir(home)
	}
	if entries, err := os.ReadDir(personaDir(home)); err == nil && slices.ContainsFunc(entries, isSectionFile) {
		return personaDir(home)
	}
	return filepath.Join(config.Dir(home), legacyPersonaFile)
}

// sectionFile names the file of a section: its title in lower case, with anything but letters and digits as a dash.
func sectionFile(title string) string {
	if title == "" {
		return openingFile
	}
	slug := strings.Trim(strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return '-'
	}, title), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	if slug == "" || slug+".md" == openingFile {
		slug = "section-" + slug
	}
	return slug + ".md"
}

// readPersonaDir reads the section files of the persona's directory; none when it does not exist.
func readPersonaDir(dir string) ([]personaSection, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the persona: %w", err)
	}
	var sections []personaSection
	for _, e := range entries {
		if !isSectionFile(e) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading the persona: %w", err)
		}
		body := strings.TrimSpace(string(data))
		if e.Name() == openingFile {
			sections = append(sections, personaSection{body: body})
			continue
		}
		// A section's file starts with its heading; one without is named after its file.
		title := strings.TrimSuffix(e.Name(), ".md")
		if first, rest, _ := strings.Cut(body, "\n"); strings.HasPrefix(first, "## ") {
			title, body = strings.TrimSpace(strings.TrimPrefix(first, "## ")), strings.TrimSpace(rest)
		}
		sections = append(sections, personaSection{title: title, body: body})
	}
	return sections, nil
}

// lockStore locks the persona store, shared for readers and exclusive for a writer, and returns how to let go. The
// persona is several files, so without it a reader could take some from before a save and some from after. ok is
// false when there is no ~/.codeforge at all: then there is no persona, and nothing is read.
func lockStore(home string, exclusive bool) (unlock func(), ok bool, err error) {
	f, err := os.OpenFile(filepath.Join(config.Dir(home), storeLockFile), os.O_CREATE|os.O_RDWR, privateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return func() {}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("locking the persona: %w", err)
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	for {
		err = syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close() //nolint:errcheck // nothing was locked
		return nil, false, fmt.Errorf("locking the persona: %w", err)
	}
	return func() { _ = f.Close() }, true, nil //nolint:errcheck // closing releases the lock
}

// readPersona reads the global persona as one document: the opening, the learned sections in their order, then the
// user's own by file name. It is nil when there is no persona. It holds the store's lock while it reads, so it sees
// the persona from before a save or from after it, never some files of each; a save that was cut short is read
// from the whole document it was saving.
func readPersona(home string) ([]byte, error) {
	unlock, ok, err := lockStore(home, false)
	if err != nil || !ok {
		return nil, err
	}
	defer unlock()
	if saving, err := os.ReadFile(filepath.Join(config.Dir(home), savingFile)); err == nil {
		return saving, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading the persona: %w", err)
	}
	sections, err := readPersonaDir(personaDir(home))
	if err != nil {
		return nil, err
	}
	if len(sections) == 0 {
		data, err := os.ReadFile(filepath.Join(config.Dir(home), legacyPersonaFile))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil //nolint:nilnil // no persona is a state, not a failure
		}
		if err != nil {
			return nil, fmt.Errorf("reading the persona: %w", err)
		}
		return data, nil
	}
	rank := func(s personaSection) int {
		if s.title == "" {
			return -1
		}
		if i := slices.Index(personaSections, s.title); i >= 0 {
			return i
		}
		return len(personaSections)
	}
	slices.SortStableFunc(sections, func(a, b personaSection) int { return rank(a) - rank(b) })
	return []byte(joinPersona(sections)), nil
}

// sectionFiles lays a document out as the files it is saved in, in order. Sections with the same title are one file,
// in the order they came; two titles that read the same as file names get a file each.
func sectionFiles(doc []byte) (names []string, texts map[string]string) {
	texts = make(map[string]string)
	titles := make(map[string]string)
	for _, s := range splitPersona(string(doc)) {
		name := sectionFile(s.title)
		for n := 2; ; n++ {
			if title, used := titles[name]; !used || title == s.title {
				break
			}
			// "~" sorts after ".", so the later title's file is read after the earlier one's.
			name = fmt.Sprintf("%s~%d.md", strings.TrimSuffix(sectionFile(s.title), ".md"), n)
		}
		if have, ok := texts[name]; ok {
			texts[name] = strings.TrimSpace(have) + "\n\n" + s.body + "\n"
			continue
		}
		names, titles[name] = append(names, name), s.title
		texts[name] = s.body + "\n"
		if s.title != "" {
			texts[name] = "## " + s.title + "\n" + texts[name]
		}
	}
	return names, texts
}

// writePersona saves the persona as one file a section, under the store's lock. The whole document is first set
// down in one file, and that is the save: the section files are then brought in line with it, and a save cut short
// there is finished by the next one, with readers given the whole document meanwhile. The files of sections the
// document no longer has are removed, and nothing else in the directory is touched.
func writePersona(home string, doc []byte) error {
	if err := os.MkdirAll(personaDir(home), privateDir); err != nil {
		return fmt.Errorf("creating the persona directory: %w", err)
	}
	unlock, _, err := lockStore(home, true)
	if err != nil {
		return err
	}
	defer unlock()
	names, _ := sectionFiles(doc)
	// Refused before anything is written: a directory or link where a section file goes is the user's.
	for _, name := range names {
		if info, err := os.Lstat(filepath.Join(personaDir(home), name)); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s is in the way of a section file", errPersonaStore, filepath.Join(personaDir(home), name))
		}
	}
	if err := fsutil.AtomicWriteFile(filepath.Join(config.Dir(home), savingFile), doc, privateFile); err != nil {
		return fmt.Errorf("writing the persona: %w", err)
	}
	return applySaving(home, doc)
}

var errPersonaStore = errors.New("the persona cannot be saved")

// applySaving brings the section files in line with the document being saved, then ends the save.
func applySaving(home string, doc []byte) error {
	dir := personaDir(home)
	names, texts := sectionFiles(doc)
	for _, name := range names {
		if err := fsutil.AtomicWriteFile(filepath.Join(dir, name), []byte(texts[name]), privateFile); err != nil {
			return fmt.Errorf("writing the persona: %w", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("writing the persona: %w", err)
	}
	for _, e := range entries {
		if !isSectionFile(e) || slices.Contains(names, e.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("removing a section the persona no longer has: %w", err)
		}
	}
	if err := os.Remove(filepath.Join(config.Dir(home), legacyPersonaFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the single-file persona this replaces: %w", err)
	}
	if err := os.Remove(filepath.Join(config.Dir(home), savingFile)); err != nil {
		return fmt.Errorf("finishing the save: %w", err)
	}
	return nil
}

// removePersona removes the global persona in either form: its section files, never anything else the user keeps in
// the directory, which goes only when that leaves it empty.
func removePersona(home string) error {
	unlock, ok, err := lockStore(home, true)
	if err != nil || !ok {
		return err
	}
	defer unlock()
	dir := personaDir(home)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the persona: %w", err)
	}
	for _, e := range entries {
		if isSectionFile(e) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return fmt.Errorf("removing the persona: %w", err)
			}
		}
	}
	_ = os.Remove(dir) //nolint:errcheck // stays when the user keeps other things in it
	for _, name := range []string{legacyPersonaFile, savingFile} {
		if err := os.Remove(filepath.Join(config.Dir(home), name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing the persona: %w", err)
		}
	}
	return nil
}

// personaSkill is one part of the persona installed as a skill of its own, loaded only for the work it covers.
type personaSkill struct {
	name, section, when, otherwise string
}

var onDemandSkills = []personaSkill{
	{"codeforge-persona-code", "Code style", "writing, changing or reviewing code", "codeforge-code"},
	{"codeforge-persona-commits", "Commit messages", "writing a commit message", "codeforge-voice"},
	{"codeforge-persona-prs", "Pull requests", "opening or editing a pull request", "codeforge-voice"},
	{"codeforge-persona-reviewing", "Review comments", "reviewing a change or writing review comments", "codeforge-review"},
	{"codeforge-persona-replies", "Replies to reviewers", "answering review feedback on the user's own change", "codeforge-respond"},
}

// reviewAlias is the skill the reviewing profiles of the previous version loaded. A session started before an upgrade
// still names it, so it is kept one more release, holding what those profiles now load as three skills.
const reviewAlias = "codeforge-persona-review"

// installPersonaSkill installs the persona as skills: what applies to everything (the opening, the voice, the rules
// and the user's own sections) as codeforge-persona, which every profile loads, and each other part as a skill of
// its own that is loaded for the work it covers. With no persona, or none for a part, a placeholder is installed so
// that a profile's skill list always resolves.
func installPersonaSkill(home string) error {
	data, err := readPersona(home)
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".empryo")
	bodies := make(map[string]string)
	var always []personaSection
	for _, s := range splitPersona(string(data)) {
		if slices.ContainsFunc(onDemandSkills, func(k personaSkill) bool { return k.section == s.title }) {
			// A section written twice, or once more in a file of the user's, keeps all of its rules.
			bodies[s.title] = strings.TrimSpace(bodies[s.title] + "\n\n" + s.body)
			continue
		}
		always = append(always, s)
	}
	base := emptyPersona
	if strings.TrimSpace(string(data)) != "" {
		var index strings.Builder
		index.WriteString("\n## The rest of this persona\nThese parts are skills of their own. Load each before the work it covers, every time; never do that work from this file alone.\n")
		for _, k := range onDemandSkills {
			index.WriteString("- `" + k.name + "`: before " + k.when + ".\n")
		}
		base = personaHeader + joinPersona(always) + index.String()
	}
	if err := writeFile(root, "skills/codeforge-persona/SKILL.md", []byte(base), privateFile); err != nil {
		return err
	}
	for _, k := range onDemandSkills {
		body := bodies[k.section]
		if body == "" || body == noEvidence {
			body = "Nothing is known yet about how the user does this. Follow `" + k.otherwise + "`."
		}
		text := "---\nname: " + k.name + "\ndescription: The user's own way of " + k.when + ", part of codeforge-persona. Load before " + k.when + ".\n---\n\n" +
			"Part of `codeforge-persona`, whose clarity baseline and voice apply here too.\n\n## " + k.section + "\n" + body + "\n"
		if err := writeFile(root, "skills/"+k.name+"/SKILL.md", []byte(text), privateFile); err != nil {
			return err
		}
	}
	alias := "---\nname: " + reviewAlias + "\ndescription: Replaced by codeforge-persona, codeforge-persona-reviewing and codeforge-persona-code; kept for sessions started before the upgrade.\n---\n\n" + clarityBaseline + "\n\n"
	if strings.TrimSpace(string(data)) == "" {
		alias += "No persona yet. Follow codeforge-voice and codeforge-code.\n"
	} else {
		for _, title := range []string{"Review comments", "Code style"} {
			if bodies[title] != "" {
				always = append(always, personaSection{title: title, body: bodies[title]})
			}
		}
		alias += joinPersona(always)
	}
	return writeFile(root, "skills/"+reviewAlias+"/SKILL.md", []byte(alias), privateFile)
}
