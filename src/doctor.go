package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/guard"
)

const doctorTimeout = 20 * time.Second

var (
	errDoctorUsage  = errors.New("usage: codeforge doctor")
	errDoctorFailed = errors.New("doctor found problems; follow the Next step above")
)

// doctorItem is one line of the doctor report. A failed item stops codeforge from working; a warning
// does not. Both carry the command that fixes them.
type doctorItem struct {
	check
	warn bool
	fix  string
}

func (d doctorItem) failed() bool { return !d.ok && !d.warn }

// runDoctor reports the whole setup in one place and names the single next step. It only reads state.
func runDoctor(ctx context.Context, home string, out io.Writer) error {
	// Diagnostics must stay snappy even if a provider CLI hangs.
	ctx, cancel := context.WithTimeout(ctx, doctorTimeout)
	defer cancel()
	items := doctorItems(ctx, home)
	if renderDoctor(out, items) {
		return errDoctorFailed
	}
	return nil
}

func doctorItems(ctx context.Context, home string) []doctorItem {
	empryoDir := filepath.Join(home, ".empryo")
	var items []doctorItem

	bin, err := findEmpryo(empryoDir)
	if err != nil {
		items = append(items, doctorItem{check: check{name: "Empryo", note: "not installed"}, fix: "codeforge init"})
	} else {
		items = append(items, doctorItem{check: check{name: "Empryo", ok: true, note: empryoVersion(ctx, bin)}})
		if ready := readyProviders(ctx, bin); len(ready) == 0 {
			items = append(items, doctorItem{check: check{name: "sign-in", note: "no model provider is signed in"}, fix: "codeforge init"})
		} else {
			items = append(items, doctorItem{check: check{name: "sign-in", ok: true, note: strings.Join(ready, ", ")}})
		}
	}
	for _, c := range []check{checkGit(ctx), checkGH(ctx, empryoDir), checkSandbox()} {
		item := doctorItem{check: c, fix: doctorFix(c.name)}
		if c.ok && c.name == "sandbox" && runtime.GOOS != "darwin" {
			item.ok, item.warn = false, true // review mode cannot start here, which is not a plain pass
		}
		items = append(items, item)
	}
	root := ""
	if wd, err := os.Getwd(); err == nil {
		if r, err := guard.RepoRoot(ctx, wd); err == nil {
			root = r
		}
	}
	items = append(items, doctorModels(home, root))
	return append(items, doctorRepo(home, root)...)
}

func doctorFix(name string) string {
	switch name {
	case "sandbox":
		return "restore /usr/bin/sandbox-exec (macOS system file); review mode needs it"
	case "git":
		return "install Git 2.54 or newer"
	case "gh":
		return "gh auth login"
	default:
		return "codeforge init"
	}
}

// doctorModels warns, not fails, when roles are unset: built-in model choices still work.
func doctorModels(home, root string) doctorItem {
	cfg, err := readConfigFile(home)
	if err != nil {
		return doctorItem{check: check{name: "models", note: err.Error()}, fix: "fix or remove ~/.codeforge/config.yaml"}
	}
	// Validate what a launch would validate: the approved project config merged over the global one, then
	// environment overrides and defaults. A config that parses but cannot start is not reported as ready.
	explicit, scope, err := doctorEffective(home, root, cfg)
	if err != nil {
		return doctorItem{check: check{name: "models", note: "invalid " + scope + " configuration: " + err.Error()}, fix: "edit the " + scope + " config.yaml, or run codeforge models configure --" + scope}
	}
	unset := 0
	for _, r := range roles {
		if len(current(explicit, r.key)) == 0 {
			unset++
		}
	}
	structural := "; model names are checked at launch, see codeforge models plan"
	if unset > 0 {
		return doctorItem{check: check{name: "models", note: fmt.Sprintf("%d of %d roles use built-in defaults%s", unset, len(roles), structural)}, warn: true, fix: "codeforge models configure"}
	}
	return doctorItem{check: check{name: "models", ok: true, note: "all roles chosen" + structural}}
}

// doctorEffective merges the project config only when it is approved, as a launch does, applies environment
// overrides, and validates the result with defaults. It returns the explicitly chosen settings (before defaults)
// and names the scope that most likely holds a problem: project when an approved project file exists.
func doctorEffective(home, root string, global Config) (Config, string, error) {
	scope := "global"
	cfg := global
	if root != "" && !sameConfigScope(home, root) {
		path := projectConfigPath(root)
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return cfg, "project", fmt.Errorf("reading %s: %w", path, err)
		default:
			if ledger, lerr := loadTrust(home); lerr == nil && ledger[path] == digest(string(data)) {
				local, derr := decodeConfig(path, data)
				if derr != nil {
					return cfg, "project", derr
				}
				cfg = mergeConfig(global, local)
				scope = "project"
			}
		}
	}
	explicit := configEnvironment(cfg)
	if err := validate(configDefaults(explicit)); err != nil {
		return explicit, scope, err
	}
	return explicit, scope, nil
}

// doctorRepo reports the persona and project model configuration of the current Git project, and whether
// each has been approved. Outside a Git project there is nothing to report.
func doctorRepo(home, root string) []doctorItem {
	if root == "" {
		return []doctorItem{{check: check{name: "project", ok: true, note: "not in a Git project; only global settings apply"}}}
	}
	ledger, err := loadTrust(home)
	if err != nil {
		return []doctorItem{{check: check{name: "trust", note: err.Error()}, fix: "remove the unreadable file in ~/.codeforge"}}
	}
	persona := doctorFile("persona", filepath.Join(root, repoPersonaPath), ledger, "codeforge init", "codeforge trust")
	config := doctorFile("config", projectConfigPath(root), ledger, "", "codeforge trust --models")
	return slices.DeleteFunc([]doctorItem{persona, config}, func(d doctorItem) bool { return d.name == "" })
}

// doctorFile reports one repository file. A missing persona is a warning; a missing config is normal.
func doctorFile(name, path string, ledger trustStore, createFix, trustFix string) doctorItem {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if createFix == "" {
			return doctorItem{}
		}
		return doctorItem{check: check{name: name, note: "none for this project"}, warn: true, fix: createFix}
	case err != nil:
		return doctorItem{check: check{name: name, note: err.Error()}, warn: true, fix: "check permissions on " + path}
	}
	content := string(data)
	if name == "persona" {
		content = strings.TrimSpace(content) // the trust ledger stores the trimmed persona, as loadRepoPersona returns it
	}
	if ledger[path] == digest(content) {
		return doctorItem{check: check{name: name, ok: true, note: "approved"}}
	}
	return doctorItem{check: check{name: name, note: "new or changed since you approved it; ignored until approved"}, warn: true, fix: trustFix}
}

// renderDoctor prints the report and the next step, a blocking fix before any warning. It reports whether
// any item blocks codeforge.
func renderDoctor(w io.Writer, items []doctorItem) bool {
	fmt.Fprintln(w)
	failed := false
	for _, d := range items {
		mark := "✓"
		switch {
		case d.failed():
			mark = "✗"
			failed = true
		case !d.ok:
			mark = "!"
		}
		fmt.Fprintf(w, "  %s %-8s %s\n", mark, d.name, d.note)
	}
	fmt.Fprintln(w)
	next := ""
	for _, pick := range []func(doctorItem) bool{doctorItem.failed, func(d doctorItem) bool { return !d.ok }} {
		if i := slices.IndexFunc(items, pick); i >= 0 && next == "" {
			next = items[i].fix
		}
	}
	if next == "" {
		fmt.Fprintln(w, "Everything is ready. Start with: codeforge")
	} else {
		fmt.Fprintf(w, "Next: %s\n", next)
	}
	return failed
}
