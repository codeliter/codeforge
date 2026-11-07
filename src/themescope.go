package src

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/codeliter/codeforge/src/guard"
)

// Codeforge Steel is applied to the Empryo TUI that codeforge starts, and only to that one. Empryo has no flag or
// environment variable that picks a theme for one launch, so the choice is written into the project's own
// .empryo/config.json while the TUI is open and taken out again when it closes: other projects, and Empryo sessions
// outside this one, never see it.
//
// What is needed to take it out again is kept in the user's own ~/.codeforge, never in the checkout: a repository is
// untrusted input, and a ledger it supplied could make codeforge delete or overwrite files. A killed codeforge leaves
// the theme in place until the next launch in that project, which puts the file back first.
const (
	steelDark  = "codeforge-steel"
	steelLight = "codeforge-steel-light"
	// themeEnv switches the theme off for a launch: CODEFORGE_THEME=off keeps whatever theme Empryo is set to.
	themeEnv       = "CODEFORGE_THEME"
	themeLockWait  = 3 * time.Second
	appearanceWait = 1500 * time.Millisecond
	gitWait        = 2 * time.Second
)

var (
	errNotJSONObject = errors.New("not a JSON object")
	errUnsafeConfig  = errors.New("the project's .empryo is a link, or not a directory")
)

// themeLedger is what has to be known to take the theme back out of one project.
type themeLedger struct {
	Workspace  string        `json:"workspace"` // the project's real path; the ledger is only ever used for it
	Holders    []themeHolder `json:"holders"`
	Existed    bool          `json:"existed"`     // the project config existed before codeforge wrote the theme
	CreatedDir bool          `json:"created_dir"` // so did the .empryo folder
	Original   []byte        `json:"original"`    // its exact bytes
	Mode       uint32        `json:"mode"`        // and its permissions
	Written    string        `json:"written"`     // sha-256 of what codeforge wrote
	Theme      string        `json:"theme"`
}

// themeHolder is one codeforge TUI that is open on the project.
type themeHolder struct {
	Pid   int    `json:"pid"`
	ID    string `json:"id"`
	Start string `json:"start"` // when that process started, so a reused pid is not mistaken for it
}

// appearanceProbe reports "dark" or "light" for the terminal; tests replace it.
var appearanceProbe = systemAppearance

// systemAppearance reads the light or dark setting the way Empryo does: its own override, then the terminal's
// COLORFGBG, then macOS; dark when nothing says otherwise.
func systemAppearance(ctx context.Context) string {
	if v := strings.ToLower(os.Getenv("EMPRYO_APPEARANCE")); v == "dark" || v == "light" {
		return v
	}
	if parts := strings.Split(os.Getenv("COLORFGBG"), ";"); len(parts) > 1 {
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			if n == 7 || n == 15 {
				return "light"
			}
			return "dark"
		}
	}
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(ctx, appearanceWait)
		defer cancel()
		if err := exec.CommandContext(ctx, "defaults", "read", "-g", "AppleInterfaceStyle").Run(); err != nil && ctx.Err() == nil {
			return "light" // the key is only set in dark mode
		}
	}
	return "dark"
}

// workspaceOf is the directory the engine will treat as the project: --cwd when given, else the working directory.
func workspaceOf(argv []string) string {
	if dir := flagValue(argv, "--cwd"); dir != "" {
		return dir
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func steelFor(appearance string) string {
	if appearance == "light" {
		return steelLight
	}
	return steelDark
}

// bestEffort drops an error the caller has no way to act on: undoing the theme must never stop a session from closing.
func bestEffort(error) {}

// configObject is a config file read as raw values, so every setting other than theme.name keeps its exact text
// (a number too large for a float stays as it was written).
type configObject map[string]json.RawMessage

// readConfigObject reads a JSON object from path. A missing file is an empty object that did not exist; text that is
// not a plain JSON object (comments, for one) is an error, and the caller leaves the file alone.
func readConfigObject(path string) (object configObject, raw []byte, existed bool, err error) {
	raw, err = os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return configObject{}, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return configObject{}, raw, true, nil
	}
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, raw, true, errNotJSONObject
	}
	return object, raw, true, nil
}

// themeObject is the theme section of a config, or an empty one.
func (c configObject) themeObject() configObject {
	var theme configObject
	if json.Unmarshal(c["theme"], &theme) != nil || theme == nil {
		return configObject{}
	}
	return theme
}

// themeName returns theme.name, or "".
func (c configObject) themeName() string {
	var name string
	if json.Unmarshal(c.themeObject()["name"], &name) != nil {
		return ""
	}
	return name
}

// withThemeName sets theme.name, or removes it when name is empty, keeping every other setting as it was.
func (c configObject) withThemeName(name string) (configObject, error) {
	theme := c.themeObject()
	if name == "" {
		delete(theme, "name")
	} else {
		quoted, err := json.Marshal(name)
		if err != nil {
			return nil, fmt.Errorf("encoding the theme name: %w", err)
		}
		theme["name"] = quoted
	}
	out := make(configObject, len(c)+1)
	for k, v := range c {
		out[k] = v
	}
	if len(theme) == 0 {
		delete(out, "theme")
		return out, nil
	}
	encoded, err := json.Marshal(theme)
	if err != nil {
		return nil, fmt.Errorf("encoding the theme section: %w", err)
	}
	out["theme"] = encoded
	return out, nil
}

func (c configObject) render() ([]byte, error) {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the project config: %w", err)
	}
	return append(raw, '\n'), nil
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// processStart is when pid started, as ps prints it, or "" when that cannot be read.
var processStart = func(pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), appearanceWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// holderAlive reports whether the codeforge that recorded h is still running: its pid exists and, when its start time
// was recorded, that process is the one that started then.
func holderAlive(h themeHolder) bool {
	if !pidAlive(h.Pid) {
		return false
	}
	if h.Start == "" {
		return true
	}
	// A start time that cannot be read (ps missing, slow) is not evidence of a different process: the holder stays.
	started := processStart(h.Pid)
	return started == "" || started == h.Start
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func themeLedgerDir(home string) string { return filepath.Join(configDir(home), "theme-ledger") }

func themeLedgerPath(home, workspace string) string {
	return filepath.Join(themeLedgerDir(home), hashOf([]byte(workspace))[:16]+".json")
}

func loadThemeLedger(path, workspace string) (themeLedger, bool) {
	var l themeLedger
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &l) != nil || l.Theme == "" || l.Workspace != workspace {
		return themeLedger{}, false
	}
	return l, true
}

func saveThemeLedger(path string, l themeLedger) error {
	raw, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("encoding the theme ledger: %w", err)
	}
	return atomicWriteFile(path, raw, privateFile)
}

// revertTheme takes codeforge's theme out of the project config. If the file is exactly what codeforge wrote it goes
// back to the original bytes (or is removed when there was none); if someone has changed it since, only the theme.name
// codeforge set is taken out and nothing else is touched. An error means the file may still hold the theme.
func revertTheme(workspace string, l themeLedger) error {
	root := filepath.Join(workspace, ".empryo")
	cfgPath := filepath.Join(root, "config.json")
	if err := safeConfigPath(root, cfgPath); err != nil {
		return err
	}
	return restoreConfig(root, cfgPath, l)
}

func restoreConfig(root, cfgPath string, l themeLedger) error {
	current, err := os.ReadFile(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", cfgPath, err)
	}
	if hashOf(current) == l.Written {
		if l.Existed {
			mode := os.FileMode(l.Mode).Perm()
			if mode == 0 {
				mode = privateFile
			}
			return atomicWriteFile(cfgPath, l.Original, mode)
		}
		return removeConfig(root, cfgPath, l)
	}
	object, _, _, readErr := readConfigObject(cfgPath)
	if readErr != nil || object.themeName() != l.Theme {
		return nil //nolint:nilerr // changed by someone else, or no longer ours: leave it as it is
	}
	without, err := object.withThemeName("")
	if err != nil {
		return err
	}
	if len(without) == 0 && !l.Existed {
		return removeConfig(root, cfgPath, l) // nothing but our theme was ever in it
	}
	raw, err := without.render()
	if err != nil {
		return err
	}
	mode := os.FileMode(privateFile)
	if info, err := os.Stat(cfgPath); err == nil {
		mode = info.Mode().Perm()
	}
	return atomicWriteFile(cfgPath, raw, mode)
}

func removeConfig(root, cfgPath string, l themeLedger) error {
	if err := os.Remove(cfgPath); err != nil {
		return fmt.Errorf("removing %s: %w", cfgPath, err)
	}
	if l.CreatedDir {
		bestEffort(os.Remove(root)) // only succeeds while it is empty
	}
	return nil
}

// configIsSafeFromGit reports whether the temporary config can never be staged or committed: the project is not a git
// repository, or git already ignores both .empryo/config.json and the temporary files written beside it. Codeforge does
// not edit a repository's ignore rules to make this true; where .empryo is not ignored the theme is simply not applied.
// Git missing counts as no repository, and so does git's own "not a git repository"; every other failure (a timeout, an
// ownership refusal, a broken repository) counts as unsafe.
func configIsSafeFromGit(ctx context.Context, project string) bool {
	ctx, cancel := context.WithTimeout(ctx, gitWait)
	defer cancel()
	var stderr bytes.Buffer
	probe := exec.CommandContext(ctx, "git", "-C", project, "rev-parse", "--is-inside-work-tree")
	probe.Stderr = &stderr
	probe.Env = append(os.Environ(), "LC_ALL=C") // git's message is matched below, so it must not be translated
	if err := probe.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(stderr.String(), "not a git repository") {
			return true // there is no repository to stage into
		}
		return false
	}
	names := append([]string{"config.json"}, themeProbeNames()...)
	for _, name := range names {
		if exec.CommandContext(ctx, "git", "-C", project, "check-ignore", "-q", "--", filepath.Join(".empryo", name)).Run() != nil {
			return false
		}
	}
	return true
}

// themeProbeNames are names of the kind atomicWriteFile gives its temporary files (tempPrefix and a run of digits),
// which hold a full copy of the config beside it for a moment. A rule that ignores them all, whatever digits come up,
// is a rule for the whole family.
func themeProbeNames() []string {
	return []string{tempPrefix + "0", tempPrefix + "1234567", tempPrefix + "4294967295", tempPrefix + "9999999999"}
}

// safeConfigPath refuses to write through a link: a symlinked .empryo or config.json could point at another project's
// settings, or at the user's global ones.
func safeConfigPath(root, cfgPath string) error {
	if info, err := os.Lstat(root); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errUnsafeConfig
	}
	if info, err := os.Lstat(cfgPath); err == nil && !info.Mode().IsRegular() {
		return errUnsafeConfig
	}
	return nil
}

func isSteel(name string) bool { return name == steelDark || name == steelLight }

// scopeTheme applies Codeforge Steel to the project for the life of a TUI and returns the function that undoes it.
// It does nothing, and says nothing, when the theme is off, not installed, already chosen, or the project has a theme of
// its own: a theme the user set is never replaced. Anything left by a codeforge that was killed is put back first,
// whatever else is decided.
func scopeTheme(ctx context.Context, home, workspace string) func() {
	nothing := func() {}
	if workspace == "" {
		return nothing
	}
	// One identity per project however it was spelled ("." or a full path, through a link or not): the ledger, the lock
	// and the home-directory check all use it.
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return nothing
	}
	project, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nothing
	}
	// In the home directory the project's .empryo is Empryo's own global one.
	if homeReal, err := filepath.EvalSymlinks(home); err != nil || homeReal == project {
		return nothing
	}

	ledgerDir := themeLedgerDir(home)
	if err := os.MkdirAll(ledgerDir, privateDir); err != nil {
		return nothing
	}
	lockCtx, cancel := context.WithTimeout(ctx, themeLockWait)
	defer cancel()
	unlock, err := guard.LockContext(lockCtx, ledgerDir)
	if err != nil {
		return nothing
	}
	defer unlock()

	ledgerPath := themeLedgerPath(home, project)
	ledger, have := loadThemeLedger(ledgerPath, project)
	if have {
		ledger.Holders = slices.DeleteFunc(ledger.Holders, func(h themeHolder) bool { return !holderAlive(h) })
		if len(ledger.Holders) == 0 {
			if revertTheme(project, ledger) != nil {
				return nothing // still there: keep the ledger so a later launch can try again
			}
			bestEffort(os.Remove(ledgerPath))
			have = false
		}
	}

	switch strings.ToLower(os.Getenv(themeEnv)) {
	case "off", "0", "none", "system":
		return nothing
	}
	theme := steelFor(appearanceProbe(ctx))
	if _, err := os.Stat(filepath.Join(home, ".empryo", "themes", theme+".json")); err != nil {
		return nothing // Empryo would fall back to its own theme for an id it cannot find
	}
	if global, _, _, err := readConfigObject(filepath.Join(home, ".empryo", "config.json")); err == nil && isSteel(global.themeName()) {
		return nothing
	}

	me := themeHolder{Pid: os.Getpid(), ID: strconv.FormatInt(time.Now().UnixNano(), 36), Start: processStart(os.Getpid())}
	if have {
		// Another codeforge TUI already has it applied on this project; join it.
		ledger.Holders = append(ledger.Holders, me)
		if saveThemeLedger(ledgerPath, ledger) != nil {
			return nothing
		}
		return releaseTheme(ctx, home, project, me)
	}

	root := filepath.Join(project, ".empryo")
	cfgPath := filepath.Join(root, "config.json")
	if safeConfigPath(root, cfgPath) != nil || !configIsSafeFromGit(ctx, project) {
		return nothing
	}
	object, original, existed, err := readConfigObject(cfgPath)
	if err != nil || object.themeName() != "" {
		return nothing
	}
	withTheme, err := object.withThemeName(theme)
	if err != nil {
		return nothing
	}
	written, err := withTheme.render()
	if err != nil {
		return nothing
	}
	createdDir := false
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		if os.Mkdir(root, dirPerm) != nil {
			return nothing
		}
		createdDir = true
	}
	mode := uint32(privateFile)
	if info, err := os.Stat(cfgPath); err == nil {
		mode = uint32(info.Mode().Perm())
	}
	ledger = themeLedger{Workspace: project, Holders: []themeHolder{me}, Existed: existed, CreatedDir: createdDir, Original: original, Mode: mode, Written: hashOf(written), Theme: theme}
	if saveThemeLedger(ledgerPath, ledger) != nil {
		return nothing
	}
	if atomicWriteFile(cfgPath, written, os.FileMode(mode)) != nil {
		bestEffort(os.Remove(ledgerPath))
		return nothing
	}
	return releaseTheme(ctx, home, project, me)
}

// releaseTheme is the undo returned by scopeTheme: the last codeforge TUI to close takes the theme out. If that fails,
// the ledger stays, so the next launch in the project tries again.
func releaseTheme(ctx context.Context, home, workspace string, me themeHolder) func() {
	return func() {
		ledgerDir := themeLedgerDir(home)
		lockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), themeLockWait)
		defer cancel()
		unlock, err := guard.LockContext(lockCtx, ledgerDir)
		if err != nil {
			return
		}
		defer unlock()
		ledgerPath := themeLedgerPath(home, workspace)
		ledger, have := loadThemeLedger(ledgerPath, workspace)
		if !have {
			return
		}
		ledger.Holders = slices.DeleteFunc(ledger.Holders, func(h themeHolder) bool { return h == me || !holderAlive(h) })
		if len(ledger.Holders) > 0 {
			bestEffort(saveThemeLedger(ledgerPath, ledger))
			return
		}
		if revertTheme(workspace, ledger) != nil {
			bestEffort(saveThemeLedger(ledgerPath, ledger)) // no holders left, theme still in place
			return
		}
		bestEffort(os.Remove(ledgerPath))
	}
}
