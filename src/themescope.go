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
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/codeliter/codeforge/src/guard"
)

// Codeforge Steel is shown in the Empryo TUI that codeforge starts. Empryo reads its theme from the global
// ~/.empryo/config.json only (a project's own config and the environment are ignored for it, and there is no flag), so the
// theme is written there while the TUI is open and the user's own theme is put back when it closes.
//
// Tradeoff: while a Codeforge TUI is open, any other Empryo session that starts shows Steel as well. A theme chosen in the
// TUI with /theme is the user's choice and is never undone.
//
// The theme is put back when the engine has exited, never because a signal arrived: Empryo can rewrite the config for as
// long as it runs. A terminating signal makes codeforge stop the engine first (see runEngineWith) and then undo. What is
// needed to put things back is kept in the user's own ~/.codeforge, so a codeforge that was killed outright (SIGKILL
// cannot be caught) is cleaned up by the next launch.
const (
	steelDark  = "codeforge-steel"
	steelLight = "codeforge-steel-light"
	// themeEnv switches the theme off for a launch: CODEFORGE_THEME=off keeps whatever theme Empryo is set to.
	themeEnv       = "CODEFORGE_THEME"
	themeLockWait  = 2 * time.Second
	appearanceWait = 1500 * time.Millisecond
)

var errNotJSONObject = errors.New("not a JSON object")

// themeLedger is what has to be known to put the user's config back.
type themeLedger struct {
	Config   string        `json:"config"` // the resolved path of the config that was changed
	Holders  []themeHolder `json:"holders"`
	Existed  bool          `json:"existed"`  // the config existed before codeforge wrote the theme
	Original []byte        `json:"original"` // its exact bytes
	Mode     uint32        `json:"mode"`     // and its permissions
	Written  string        `json:"written"`  // sha-256 of what codeforge wrote
	Theme    string        `json:"theme"`    // the Steel theme that was set
	Previous string        `json:"previous"` // the user's theme.name as JSON text, "" when there was none
}

// themeHolder is one codeforge TUI that is open.
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

func steelFor(appearance string) string {
	if appearance == "light" {
		return steelLight
	}
	return steelDark
}

func isSteel(name string) bool { return name == steelDark || name == steelLight }

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

// themeSection is the theme section of a config, or an empty one.
func (c configObject) themeSection() configObject {
	var theme configObject
	if json.Unmarshal(c["theme"], &theme) != nil || theme == nil {
		return configObject{}
	}
	return theme
}

// themeName returns theme.name, or "".
func (c configObject) themeName() string {
	var name string
	if json.Unmarshal(c.themeSection()["name"], &name) != nil {
		return ""
	}
	return name
}

// rawThemeName returns theme.name exactly as written, or "" when there is none.
func (c configObject) rawThemeName() string { return string(c.themeSection()["name"]) }

// withRawThemeName sets theme.name to raw (JSON text), or removes it when raw is empty, keeping every other setting as
// it was. An emptied theme section is dropped.
func (c configObject) withRawThemeName(raw string) (configObject, error) {
	theme := c.themeSection()
	if raw == "" {
		delete(theme, "name")
	} else {
		theme["name"] = json.RawMessage(raw)
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
		return nil, fmt.Errorf("encoding the config: %w", err)
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

// empryoConfigPath is the global Empryo config.
func empryoConfigPath(home string) string {
	return filepath.Join(home, ".empryo", "config.json")
}

func themeLedgerDir(home string) string  { return filepath.Join(configDir(home), "theme-ledger") }
func themeLedgerPath(home string) string { return filepath.Join(themeLedgerDir(home), "global.json") }

func loadThemeLedger(path string) (themeLedger, bool) {
	var l themeLedger
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &l) != nil || l.Theme == "" || l.Config == "" {
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

// revertTheme puts the user's config back. If the file is exactly what codeforge wrote it goes back to the original
// bytes; if something else has saved it since (Empryo saves settings while it runs), only theme.name is put back to what
// it was, and only if it is still the Steel theme codeforge set: a theme chosen in the TUI since is the user's own.
// An error means the file may still hold the Steel theme.
func revertTheme(l themeLedger) error {
	current, err := os.ReadFile(l.Config)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", l.Config, err)
	}
	if hashOf(current) == l.Written {
		if !l.Existed {
			if err := os.Remove(l.Config); err != nil {
				return fmt.Errorf("removing %s: %w", l.Config, err)
			}
			return nil
		}
		return atomicWriteFile(l.Config, l.Original, currentMode(l))
	}
	object, _, _, readErr := readConfigObject(l.Config)
	if readErr != nil || object.themeName() != l.Theme {
		return nil //nolint:nilerr // changed by someone else, or no longer ours: leave it as it is
	}
	restored, err := object.withRawThemeName(l.Previous)
	if err != nil {
		return err
	}
	raw, err := restored.render()
	if err != nil {
		return err
	}
	return atomicWriteFile(l.Config, raw, currentMode(l))
}

// currentMode is the permissions the config has now, so a restore never loosens a file the user has since made private;
// the recorded ones are the fallback.
func currentMode(l themeLedger) os.FileMode {
	if info, err := os.Stat(l.Config); err == nil {
		return info.Mode().Perm()
	}
	if mode := os.FileMode(l.Mode).Perm(); mode != 0 {
		return mode
	}
	return privateFile
}

// scopeTheme shows Codeforge Steel for the life of a TUI and returns the function that undoes it. It does nothing when
// the theme is off, not installed, already the user's choice, or the config cannot be changed safely. Anything left by a
// codeforge that was killed is put back first, whatever else is decided.
func scopeTheme(ctx context.Context, home string) func() {
	nothing := func() {}
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

	ledgerPath := themeLedgerPath(home)
	ledger, have := loadThemeLedger(ledgerPath)
	if have {
		ledger.Holders = slices.DeleteFunc(ledger.Holders, func(h themeHolder) bool { return !holderAlive(h) })
		if len(ledger.Holders) == 0 {
			if revertTheme(ledger) != nil {
				return nothing // still there: keep the ledger so a later launch can try again
			}
			bestEffort(os.Remove(ledgerPath))
			have = false
		}
	}

	if os.Getenv("EMPRYO_HOME") != "" {
		return nothing // Empryo then reads another home than the one Codeforge installs its themes into
	}
	me := themeHolder{Pid: os.Getpid(), ID: strconv.FormatInt(time.Now().UnixNano(), 36), Start: processStart(os.Getpid())}
	if have {
		// Another codeforge TUI already has the theme applied, and this engine reads the same config whatever it chose
		// (even with the theme switched off), so it can save the Steel theme back: it holds the theme until it is gone too.
		ledger.Holders = append(ledger.Holders, me)
		if saveThemeLedger(ledgerPath, ledger) != nil {
			return nothing
		}
		return releaseTheme(ctx, home, me)
	}

	switch strings.ToLower(os.Getenv(themeEnv)) {
	case "off", "0", "none", "system":
		return nothing
	}
	theme := steelFor(appearanceProbe(ctx))
	cfgPath := empryoConfigPath(home)
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfgPath), "themes", theme+".json")); err != nil {
		return nothing // Empryo would fall back to its own theme for an id it cannot find
	}

	// A config that is a link (dotfiles) is changed where it really lives, so the link stays a link.
	target, err := filepath.EvalSymlinks(cfgPath)
	if err != nil {
		return nothing // no config yet, or unreadable: Empryo makes its own
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return nothing
	}
	object, original, existed, err := readConfigObject(target)
	if err != nil || isSteel(object.themeName()) {
		return nothing
	}
	quoted, err := json.Marshal(theme)
	if err != nil {
		return nothing
	}
	withTheme, err := object.withRawThemeName(string(quoted))
	if err != nil {
		return nothing
	}
	written, err := withTheme.render()
	if err != nil {
		return nothing
	}
	mode := uint32(info.Mode().Perm())
	ledger = themeLedger{
		Config: target, Holders: []themeHolder{me}, Existed: existed, Original: original, Mode: mode,
		Written: hashOf(written), Theme: theme, Previous: object.rawThemeName(),
	}
	// The ledger first, so a crash between the two steps still leaves what is needed to undo the second.
	if saveThemeLedger(ledgerPath, ledger) != nil {
		return nothing
	}
	if atomicWriteFile(target, written, os.FileMode(mode)) != nil {
		bestEffort(os.Remove(ledgerPath))
		return nothing
	}
	return releaseTheme(ctx, home, me)
}

// runThemed runs the interactive engine with Codeforge Steel on and the user's theme back when it is gone, however it
// ended: it exited on its own, or codeforge was told to terminate (SIGTERM, SIGHUP, SIGQUIT), in which case runEngineWith
// stops the engine first and then calls the undo, and the deferred undo below is a no-op. Outside a terminal nothing is
// changed. The signals are caught before the config is touched, so one arriving in between cannot kill codeforge with the
// theme still written.
func runThemed(ctx context.Context, home string, argv, env []string) error {
	if !interactive() {
		return runInteractive(ctx, argv, env)
	}
	const earlyBuffer = 4
	early := make(chan os.Signal, earlyBuffer)
	signal.Notify(early, slices.DeleteFunc(catchableSignals(), func(s os.Signal) bool { return s == os.Interrupt })...)
	defer signal.Stop(early)
	undo := sync.OnceFunc(scopeTheme(ctx, home))
	defer undo()
	select {
	case sig := <-early:
		if unix, ok := sig.(syscall.Signal); ok {
			return &EngineExitError{Code: signalExitBase + int(unix)}
		}
		return &EngineExitError{Code: signalExitBase + int(syscall.SIGTERM)}
	default:
	}
	return runEngineWith(ctx, argv, env, time.Time{}, os.Stdout, undo, early)
}

// releaseTheme is the undo returned by scopeTheme: the last codeforge TUI to close puts the user's theme back. If that
// fails, the ledger stays, so the next launch tries again. It does not depend on ctx, which is already cancelled when a
// signal ended the session.
func releaseTheme(ctx context.Context, home string, me themeHolder) func() {
	return func() {
		ledgerDir := themeLedgerDir(home)
		lockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), themeLockWait)
		defer cancel()
		unlock, err := guard.LockContext(lockCtx, ledgerDir)
		if err != nil {
			return
		}
		defer unlock()
		ledgerPath := themeLedgerPath(home)
		ledger, have := loadThemeLedger(ledgerPath)
		if !have {
			return
		}
		ledger.Holders = slices.DeleteFunc(ledger.Holders, func(h themeHolder) bool { return h == me || !holderAlive(h) })
		if len(ledger.Holders) > 0 {
			bestEffort(saveThemeLedger(ledgerPath, ledger))
			return
		}
		if revertTheme(ledger) != nil {
			bestEffort(saveThemeLedger(ledgerPath, ledger)) // no holders left, theme still in place
			return
		}
		bestEffort(os.Remove(ledgerPath))
	}
}
