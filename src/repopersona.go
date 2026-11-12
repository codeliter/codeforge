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
	"slices"
	"strings"
	"time"

	"github.com/codeliter/codeforge/assets"
	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/fsutil"
	"github.com/codeliter/codeforge/src/gitroot"
	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/review"
)

const (
	// A repository persona is a directory, one file a section, like the global one; a single persona.md from before
	// is read as it is.
	repoPersonaDirPath  = ".codeforge/persona"
	repoPersonaPath     = ".codeforge/persona.md"
	maxRepoPersonaBytes = 20_000
	// maxRepoPersonaRaw bounds a persona on disk, blank lines and all, before it is parsed.
	maxRepoPersonaRaw = 2 * maxRepoPersonaBytes
	// maxRepoSections bounds how many section files a repository persona has.
	maxRepoSections     = 200
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

// hasSections reports whether dir holds a persona's section files.
func hasSections(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && slices.ContainsFunc(entries, isSectionFile)
}

// rawSize is how many bytes a persona takes on disk, in either form, without reading it.
func rawSize(dir, file string) int64 {
	var n int64
	if entries, err := os.ReadDir(dir); err == nil {
		files := 0
		for _, e := range entries {
			if info, err := e.Info(); err == nil && isSectionFile(e) {
				n += info.Size()
				files++
			}
		}
		// A repository persona is a handful of sections: a directory with far more is not read file by file.
		if files > maxRepoSections {
			return maxRepoPersonaRaw + 1
		}
		// With section files the directory is the persona, whatever their size: the single file is not read.
		if files > 0 {
			return n
		}
	}
	if n == 0 {
		if info, err := os.Stat(file); err == nil {
			n = info.Size()
		}
	}
	return n
}

// readSectionDoc reads a repository persona as one document: from its directory of section files, or failing that
// from the single file of one saved before. It is "" when there is neither.
func readSectionDoc(dir, file, what string) (string, error) {
	// Refused by its size on disk before any of it is read: blank lines do not count towards the limit below.
	if n := rawSize(dir, file); n > maxRepoPersonaRaw {
		return "", fmt.Errorf("%w: %d bytes on disk", errRepoPersonaSize, n)
	}
	sections, ordered, err := readPersonaDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", what, err)
	}
	var doc string
	if len(sections) > 0 {
		if !ordered {
			sections = orderSections(sections)
		}
		doc = joinPersona(sections)
	}
	// A directory that says nothing, its files empty or only headings, does not hide a single file that does.
	if !slices.ContainsFunc(sections, func(s personaSection) bool { return s.body != "" }) {
		if info, err := os.Stat(file); err == nil && info.Size() > maxRepoPersonaRaw {
			return "", fmt.Errorf("%w: %d bytes on disk", errRepoPersonaSize, info.Size())
		}
		data, err := os.ReadFile(file)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("reading %s: %w", what, err)
		}
		// A single file is held to its size as it is, as it always was.
		if len(data) > maxRepoPersonaBytes {
			return "", fmt.Errorf("%w: %d bytes, limit %d", errRepoPersonaSize, len(data), maxRepoPersonaBytes)
		}
		return strings.TrimSpace(string(data)), nil
	}
	if n := personaSize(doc); n > maxRepoPersonaBytes {
		return "", fmt.Errorf("%w: %d bytes, limit %d", errRepoPersonaSize, n, maxRepoPersonaBytes)
	}
	return strings.TrimSpace(doc), nil
}

// personaSize is the size a persona is held to: that of the section files it is kept in. A persona therefore
// measures the same as one file, as one file a section, and before and after sections that share a title are joined.
func personaSize(doc string) int {
	n := 0
	// Each file's closing newline is not counted, so a single file at the limit is still at it as section files.
	_, texts := sectionFiles([]byte(doc))
	for _, text := range texts {
		n += len(strings.TrimSuffix(text, "\n"))
	}
	return n
}

// sharedPersonaPath is where the persona shared in the repository is: its directory, or the single file.
func sharedPersonaPath(root string) string {
	if !usableFile(filepath.Join(root, repoPersonaPath)) && hasSections(filepath.Join(root, repoPersonaDirPath)) {
		return filepath.Join(root, repoPersonaDirPath)
	}
	return filepath.Join(root, repoPersonaPath)
}

func loadRepoPersona(root string) (string, error) {
	// A single file in the repository wins over a directory beside it: it is what a team that has not moved to
	// section files maintains, and a directory must never hide it.
	if file := filepath.Join(root, repoPersonaPath); usableFile(file) {
		return readSectionDoc("", file, "repository persona")
	}
	return readSectionDoc(filepath.Join(root, repoPersonaDirPath), filepath.Join(root, repoPersonaPath), "repository persona")
}

// writeSectionDir writes a persona as a new directory of section files. It is written beside its place and moved
// in whole, and never over a directory that holds anything. It returns the persona as it reads back from those files.
func writeSectionDir(dir string, doc []byte, dirMode, fileMode os.FileMode) (string, error) {
	// All of it is checked before any of it is written: what could not be loaded back is not published.
	const maxFileName = 200
	names, texts := sectionFiles(doc)
	if len(names) > maxRepoSections {
		return "", fmt.Errorf("%w: %d sections, limit %d", errRepoPersonaSize, len(names), maxRepoSections)
	}
	var sections []personaSection
	for _, name := range names {
		if len(name) > maxFileName {
			return "", fmt.Errorf("%w: a section's title is too long to name its file", errRepoPersonaSize)
		}
		body := strings.TrimSpace(texts[name])
		if name == openingFile {
			sections = append(sections, personaSection{body: body})
			continue
		}
		first, rest, _ := strings.Cut(body, "\n")
		sections = append(sections, personaSection{title: strings.TrimSpace(strings.TrimPrefix(first, "## ")), body: strings.TrimSpace(rest)})
	}
	if n := personaSize(string(doc)); n > maxRepoPersonaBytes {
		return "", fmt.Errorf("%w: %d bytes as section files, limit %d", errRepoPersonaSize, n, maxRepoPersonaBytes)
	}
	// Headings with nothing under them say nothing, and would read back as no persona at all.
	if !slices.ContainsFunc(sections, func(s personaSection) bool { return s.body != "" }) {
		return "", errEmptyPersona
	}
	// What the directory must read back as, from the document given and not from the disk.
	expected := strings.TrimSpace(joinPersona(sections))

	if err := os.MkdirAll(filepath.Dir(dir), dirMode); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(dir), err)
	}
	staged, err := os.MkdirTemp(filepath.Dir(dir), ".persona-*")
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	defer os.RemoveAll(staged) //nolint:errcheck // ours alone; gone once moved in, or never needed
	if err := os.Chmod(staged, dirMode); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(staged, name), []byte(texts[name]), fileMode); err != nil {
			return "", fmt.Errorf("writing %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(staged, orderFile), orderList(names), fileMode); err != nil {
		return "", fmt.Errorf("writing %s: %w", dir, err)
	}
	// Moved into a place nothing has yet, in one step that fails if anything is there, even an empty directory.
	if err := fsutil.RenameNoReplace(staged, dir); errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("%w: %s", errRepoPersonaExists, dir)
	} else if err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return expected, nil
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

// privateRepoPersonaDir is the directory form of the user's own persona for a repository.
func privateRepoPersonaDir(ctx context.Context, home, root string) string {
	return filepath.Join(filepath.Dir(privateRepoPersonaPath(ctx, home, root)), personaDirName)
}

// privatePersonaWhere is where the user's own persona for a repository is, to show: its directory, or the single file.
func privatePersonaWhere(ctx context.Context, home, root string) string {
	if dir := privateRepoPersonaDir(ctx, home, root); hasSections(dir) {
		return dir
	}
	return privateRepoPersonaPath(ctx, home, root)
}

// holdsPersona reports whether one entry of the private store holds a persona, in either form.
func holdsPersona(dir string) bool {
	if info, err := os.Lstat(filepath.Join(dir, "persona.md")); err == nil && info.Mode().IsRegular() {
		return true
	}
	info, err := os.Lstat(filepath.Join(dir, personaDirName))
	return err == nil && info.IsDir() && hasSections(filepath.Join(dir, personaDirName))
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
		if !e.IsDir() || !holdsPersona(filepath.Join(dir, e.Name())) {
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
	if !holdsPersona(dir) {
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
	return readSectionDoc(privateRepoPersonaDir(ctx, home, root), privateRepoPersonaPath(ctx, home, root), "your repository persona")
}

// splitRepoPersona splits a repository persona into what applies to everything and its parts by the work they
// cover. The parts named in inline stay with the rest: a profile that cannot load skills needs them in its prompt.
func splitRepoPersona(doc string, inline []string) (always string, parts map[string]string) {
	parts = make(map[string]string)
	var rest []personaSection
	for _, s := range splitPersona(doc) {
		onDemand := slices.ContainsFunc(onDemandSkills, func(k personaSkill) bool { return k.section == s.title })
		if !onDemand || slices.Contains(inline, s.title) {
			rest = append(rest, s)
			continue
		}
		parts[s.title] = strings.TrimSpace(parts[s.title] + "\n\n" + s.body)
	}
	if len(rest) == 0 {
		return "", parts
	}
	return strings.TrimSpace(joinPersona(rest)), parts
}

// repoSkill is one part of the repository's personas installed as a project skill.
type repoSkill struct {
	personaSkill
	name string
}

const (
	repoSkillPrefix = "codeforge-repo-persona-"
	// repoSkillAge is how long a project skill no launch has used is kept: a session still running may load it.
	repoSkillAge = 30 * 24 * time.Hour
)

var errRepoSkill = errors.New("a project skill of the repository persona could not be installed")

// installRepoSkills installs the parts of the repository's personas as skills Empryo loads on demand. They go in
// the user's own ~/.empryo/skills, never in the checkout: nothing of a private persona can be staged from there, and
// no file the repository tracks is touched. A skill is named after its content, so one is never changed once
// written: a session already running keeps what it was told to load, and each launch names exactly the skills that
// hold its own conventions. Nothing is written through a link. Skills no launch has used for repoSkillAge are removed.
func installRepoSkills(ctx context.Context, home, root string, shared, private map[string]string) ([]repoSkill, error) {
	skills := filepath.Join(home, ".empryo", "skills")
	// When the checkout is, or holds, the home directory, that would be inside it after all. Both are taken as
	// the filesystem has them, links resolved, and compared a whole path element at a time.
	realRoot, rootErr := filepath.EvalSymlinks(root)
	realHome, homeErr := filepath.EvalSymlinks(home)
	rel, relErr := filepath.Rel(realRoot, filepath.Join(realHome, ".empryo", "skills"))
	outside := rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
	if rootErr != nil || homeErr != nil || relErr != nil || !outside {
		return nil, fmt.Errorf("%w: %s is inside the repository, or cannot be told to be outside it", errRepoSkill, skills)
	}
	// One launch at a time installs, refreshes and sweeps, so none removes a skill another has just taken up.
	unlock, err := guard.LockContext(ctx, config.Dir(home))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRepoSkill, err)
	}
	defer unlock()
	if fsutil.Symlinked(home, filepath.Join(".empryo", "skills")) {
		return nil, fmt.Errorf("%w: %s is, or is under, a link", errRepoSkill, skills)
	}
	var installed []repoSkill
	for _, k := range onDemandSkills {
		team, own := shared[k.section], private[k.section]
		if team == "" && own == "" {
			continue
		}
		var b strings.Builder
		b.WriteString("One repository's conventions. They override `" + k.name + "` and the generic skills where they differ, never hard stops, safety rules, the user's instructions or the clarity baseline.")
		if team != "" && own != "" {
			b.WriteString(" Two sets follow; where they differ, the team's wins.")
		}
		if team != "" {
			b.WriteString("\n\n## " + k.section + " (the team's, shared in the repository)\n" + team)
		}
		if own != "" {
			b.WriteString("\n\n## " + k.section + " (the user's own for this repository)\n" + own)
		}
		const short = 6
		sum := sha256.Sum256([]byte(b.String()))
		name := repoSkillPrefix + strings.TrimPrefix(k.name, "codeforge-persona-") + "-" + hex.EncodeToString(sum[:short])
		text := "---\nname: " + name + "\ndescription: One repository's own way of " + k.when + ". Load only when your instructions name this skill.\n---\n\n" + b.String() + "\n"
		if err := installRepoSkill(skills, name, text); err != nil {
			return nil, err
		}
		installed = append(installed, repoSkill{personaSkill: k, name: name})
	}
	sweepRepoSkills(skills, installed)
	return installed, nil
}

func digestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// generatedMark is the file that marks a skill directory as one installRepoSkill made.
const generatedMark = ".codeforge-generated"

// installRepoSkill puts one skill in place whole, or finds it there already. One of that name with other content is
// not replaced.
func installRepoSkill(skills, name, text string) error {
	dir := filepath.Join(skills, name)
	same := func() bool {
		info, err := os.Lstat(filepath.Join(dir, "SKILL.md"))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		have, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		return err == nil && string(have) == text
	}
	if info, err := os.Lstat(dir); err == nil {
		// A link there could lead anywhere, and is neither read nor touched.
		if !info.IsDir() || !same() {
			return fmt.Errorf("%w: %s holds something else", errRepoSkill, dir)
		}
		now := time.Now()
		_ = os.Chtimes(filepath.Join(dir, "SKILL.md"), now, now) //nolint:errcheck // only keeps it from the sweep
		return nil
	}
	if err := os.MkdirAll(skills, dirPerm); err != nil {
		return fmt.Errorf("%w: %w", errRepoSkill, err)
	}
	staged, err := os.MkdirTemp(skills, ".staged-*")
	if err != nil {
		return fmt.Errorf("%w: %w", errRepoSkill, err)
	}
	defer os.RemoveAll(staged) //nolint:errcheck // gone once moved in, or never needed
	err = errors.Join(
		os.WriteFile(filepath.Join(staged, generatedMark), []byte(digestOf(text)), privateFile),
		os.WriteFile(filepath.Join(staged, "SKILL.md"), []byte(text), privateFile),
		os.Chmod(staged, dirPerm))
	if err == nil {
		// Into a name nothing has: a directory put there meanwhile, even an empty one, is not replaced.
		err = fsutil.RenameNoReplace(staged, dir)
	}
	if err != nil && !same() { // another launch may have put the same skill there meanwhile
		return fmt.Errorf("%w: %w", errRepoSkill, err)
	}
	return nil
}

// sweepRepoSkills removes the repository-persona skills no launch has used for repoSkillAge. Only what
// installRepoSkill made goes: a directory with its mark, and from it only the two files it wrote, so anything else
// kept there stays. What is left is named by no instructions, and says to load it only when they do.
func sweepRepoSkills(skills string, keep []repoSkill) {
	entries, err := os.ReadDir(skills)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), repoSkillPrefix) || slices.ContainsFunc(keep, func(k repoSkill) bool { return k.name == e.Name() }) {
			continue
		}
		dir := filepath.Join(skills, e.Name())
		if mark, err := os.Lstat(filepath.Join(dir, generatedMark)); err != nil || !mark.Mode().IsRegular() {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, "SKILL.md"))
		if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) < repoSkillAge {
			continue
		}
		// And only as it was written: the mark holds the digest of the whole file, so one edited since is kept.
		text, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		mark, markErr := os.ReadFile(filepath.Join(dir, generatedMark))
		if err != nil || markErr != nil || string(mark) != digestOf(string(text)) {
			continue
		}
		for _, name := range []string{"SKILL.md", generatedMark} {
			_ = os.Remove(filepath.Join(dir, name)) //nolint:errcheck // tried again at the next launch
		}
		_ = os.Remove(dir) //nolint:errcheck // goes only when that left it empty
	}
}

// repoPersonaPrompt lays the repository's personas out as system instructions: the one the team shares in the
// repository, which wins, and the user's own for it, then the parts that are skills to load.
func repoPersonaPrompt(shared, private string, skills []repoSkill) string {
	var b strings.Builder
	b.WriteString(repoPersonaPreamble)
	if shared != "" && private != "" {
		b.WriteString(" Two sets follow: the team's, shared in the repository, and the user's own for this repository. Where they differ, the team's wins.")
	}
	if shared != "" {
		b.WriteString("\n\n<repo_persona>\n" + shared + "\n</repo_persona>")
	}
	if private != "" {
		b.WriteString("\n\n<my_repo_persona>\n" + private + "\n</my_repo_persona>")
	}
	if len(skills) > 0 {
		b.WriteString("\n\nThe rest of these conventions are project skills. Load each before the work it covers, every time:")
		for _, k := range skills {
			b.WriteString("\n- `" + k.name + "`: before " + k.when + ".")
		}
	}
	return b.String()
}

// withRepoPersona is withRepoPersonaFor for a mode that can load skills.
func withRepoPersona(ctx context.Context, home string, args []string) ([]string, error) {
	return withRepoPersonaFor(ctx, home, "", args)
}

// withRepoPersonaFor adds the repository's personas, unless the user passed their own --system: the one shared in
// the repository once trusted, and the user's own, which they wrote and so needs no approval. What applies to
// everything goes in as system instructions; each other part is installed as a project skill, loaded for the work
// it covers. Review mode gets the reviewing and code parts in its instructions, since it reviews from the start.
func withRepoPersonaFor(ctx context.Context, home, mode string, args []string) ([]string, error) {
	if hasFlag(args, "--system") {
		return args, nil
	}

	root, err := repoRoot(ctx)
	if err != nil {
		return nil, err
	}

	// Both are read under the lock a share holds, so a persona being copied from one to the other is seen whole.
	// The lock is let go before anyone is asked anything.
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
		p := sharedPersonaPath(root)
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
		fmt.Fprintf(os.Stderr, "codeforge: using %s\n", privatePersonaWhere(ctx, home, root))
	}
	var inline []string
	if mode == modeReview {
		inline = []string{"Review comments", "Code style"}
	}
	sharedAlways, sharedParts := splitRepoPersona(shared, inline)
	privateAlways, privateParts := splitRepoPersona(private, inline)
	skills, err := installRepoSkills(ctx, home, root, sharedParts, privateParts)
	if err != nil {
		// The skills could not be installed, so nothing is left to be loaded later: all of it goes in now.
		sharedAlways, privateAlways, skills = shared, private, nil
	}
	if sharedAlways == "" && privateAlways == "" && len(skills) == 0 {
		return args, nil
	}
	return append([]string{"--system", repoPersonaPrompt(sharedAlways, privateAlways, skills)}, args...), nil
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
	p := sharedPersonaPath(root)
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
	if len(persona) > maxRepoPersonaRaw {
		return "", fmt.Errorf("%w: %d bytes", errRepoPersonaSize, len(persona))
	}
	if n := personaSize(string(persona)); n > maxRepoPersonaBytes {
		return "", fmt.Errorf("%w: %d bytes, limit %d", errRepoPersonaSize, n, maxRepoPersonaBytes)
	}
	if existing, err := loadRepoPersona(root); err != nil {
		return "", err
	} else if existing != "" && !unfinishedRepoPersona([]byte(existing)) {
		return "", fmt.Errorf("%w: %s", errRepoPersonaExists, sharedPersonaPath(root))
	}
	if dir := filepath.Join(root, repoPersonaDirPath); hasSections(dir) {
		// A scaffold kept as a directory is the team's to fill in: nothing in the repository is replaced from here.
		return "", fmt.Errorf("%w: %s holds an unfilled template; fill its files in, or remove it", errRepoPersonaExists, dir)
	}
	if shared := filepath.Join(root, repoPersonaPath); fileExists(shared) {
		return "", fmt.Errorf("%w: %s holds an unfilled template; fill it in, or remove it", errRepoPersonaExists, shared)
	}
	// Checked and written under one lock: another codeforge setting this repository up at the same time, here or in
	// another clone of it, must not have its persona replaced.
	unlock, err := guard.LockContext(ctx, config.Dir(home))
	if err != nil {
		return "", fmt.Errorf("saving your repository persona: %w", err)
	}
	defer unlock()
	existing, err := loadPrivateRepoPersona(ctx, home, root)
	if err != nil {
		return "", err
	}
	if existing != "" && !unfinishedRepoPersona([]byte(existing)) {
		return "", fmt.Errorf("%w: %s", errRepoPersonaExists, privatePersonaWhere(ctx, home, root))
	}
	file, dir := privateRepoPersonaPath(ctx, home, root), privateRepoPersonaDir(ctx, home, root)
	if err := os.MkdirAll(filepath.Dir(file), privateDir); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(file), err)
	}
	// Which repository it is for, for persona list; the persona works without it.
	_ = fsutil.AtomicWriteFile(filepath.Join(filepath.Dir(file), repoKeyFile), []byte(withoutCredentials(repoKey(ctx, root))+"\n"), privateFile) //nolint:errcheck // only a label
	// The blank template is one file to fill in by hand; a persona with something in it is one file a section.
	if unfinishedRepoPersona(persona) {
		if err := fsutil.AtomicWriteFile(file, persona, privateFile); err != nil {
			return "", fmt.Errorf("writing your repository persona: %w", err)
		}
		return file, nil
	}
	if _, err := writeSectionDir(dir, persona, privateDir, privateFile); err != nil {
		return "", err
	}
	return dir, nil
}

// usableFile reports whether path is a file that can be read: a link to nothing is not one.
func usableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
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
	private := privatePersonaWhere(ctx, home, root)
	// Read as the runtime reads it, so what it would refuse to load (too large, unreadable) is not shared.
	persona, err := loadPrivateRepoPersona(ctx, home, root)
	if err != nil {
		return err
	}
	if persona == "" {
		if _, statErr := os.Lstat(private); statErr != nil {
			return errNothingToShare
		}
	}
	if unfinishedRepoPersona([]byte(persona)) {
		return fmt.Errorf("%w: %s is still the unfilled template; fill it in first", errNothingToShare, private)
	}
	// Whatever is in the repository is the team's, even an unfilled scaffold: it is never replaced from here.
	if existing, err := os.ReadFile(filepath.Join(root, repoPersonaPath)); err == nil && unfinishedRepoPersona(existing) {
		return fmt.Errorf("%w: %s is an unfilled scaffold; remove it and share again", errRepoPersonaExists, filepath.Join(root, repoPersonaPath))
	}
	if err := createShared(home, root, []byte(persona)); err != nil {
		return err
	}
	fmt.Fprintf(w, "codeforge: shared your persona for this repository in %s, one file a section. Commit it.\n", filepath.Join(root, repoPersonaDirPath))
	fmt.Fprintf(w, "Your own copy at %s is kept; the shared one wins where they differ. Remove yours with codeforge persona forget.\n", private)
	return nil
}

// createShared writes the shared persona, as a directory of section files, only if the repository has none in either
// form: one that appeared since the check, from another codeforge or a pull, is never replaced.
func createShared(home, root string, persona []byte) error {
	dir := filepath.Join(root, repoPersonaDirPath)
	if fsutil.Symlinked(root, repoPersonaDirPath) {
		return fmt.Errorf("%w: %s", config.ErrConfigSymlink, dir)
	}
	if _, err := os.Lstat(filepath.Join(root, repoPersonaPath)); err == nil {
		return fmt.Errorf("%w: %s", errRepoPersonaExists, filepath.Join(root, repoPersonaPath))
	}
	written, err := writeSectionDir(dir, persona, dirPerm, publicFile)
	if err != nil {
		return err
	}
	// Only what was just written is approved: if the directory reads differently now, something else changed it
	// (a checkout, an editor), and that is the team's to be looked at, not approved or removed from here.
	now, err := loadRepoPersona(root)
	if err != nil {
		return err
	}
	// Nor when a single-file persona appeared meanwhile: the directory would hide it.
	if now != written || fileExists(filepath.Join(root, repoPersonaPath)) {
		return fmt.Errorf("%w: %s changed while it was being shared and was not approved; look at it, then run codeforge trust", errRepoPersonaExists, filepath.Dir(dir))
	}
	if err := config.Trust(home, dir, written); err != nil {
		// It stays where it is: removing it could take with it what someone else has written there since.
		return fmt.Errorf("%s is in the repository but could not be approved (%w); once that is repaired, run codeforge trust", dir, err)
	}
	return nil
}

func unfinishedRepoPersona(data []byte) bool {
	body := strings.TrimSpace(string(data))
	// Compared as sections, so the template reads as unfilled whether it is kept as one file or one file a section.
	norm := func(doc string) string { return strings.TrimSpace(joinPersona(splitPersona(doc))) }
	return body == "" || norm(body) == norm(assets.RepoPersonaTemplate)
}

func repoPersonaSeed(starterName string) ([]byte, error) {
	if starterName == "" {
		return []byte(assets.RepoPersonaTemplate), nil
	}
	return starterPersona(starterName)
}
