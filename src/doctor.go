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

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"

	"github.com/codeliter/codeforge/src/guard"
	"github.com/codeliter/codeforge/src/modelcatalog"
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
		if ready := modelcatalog.ReadyProviders(ctx, bin); len(ready) == 0 {
			items = append(items, doctorItem{check: check{name: "sign-in", note: "no model provider is signed in"}, fix: "codeforge init"})
		} else {
			items = append(items, doctorItem{check: check{name: "sign-in", ok: true, note: strings.Join(ready, ", ")}})
		}
	}
	for _, c := range []check{checkGit(ctx), checkGH(ctx, empryoDir), checkSandbox(), checkForms()} {
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
	if note := personaStale(ctx, home, time.Now()); note != "" {
		items = append(items, doctorItem{check: check{name: "global persona", note: note}, warn: true, fix: "codeforge persona update"})
	}
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
	case "forms":
		return "run `make install` in the codeforge checkout (from source) or reinstall codeforge, or point " + forms.HelperEnv + " at codeforge-forms"
	default:
		return "codeforge init"
	}
}

// doctorModels warns, not fails, when roles are unset: built-in model choices still work.
func doctorModels(home, root string) doctorItem {
	cfg, err := config.ReadFile(home)
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
	for _, r := range config.Roles {
		if len(config.Current(explicit, r.Key)) == 0 {
			unset++
		}
	}
	structural := "; model names are checked at launch, see codeforge models plan"
	if unset > 0 {
		return doctorItem{check: check{name: "models", note: fmt.Sprintf("%d of %d roles use built-in defaults%s", unset, len(config.Roles), structural)}, warn: true, fix: "codeforge models configure"}
	}
	return doctorItem{check: check{name: "models", ok: true, note: "all roles chosen" + structural}}
}

// doctorEffective merges the project config only when it is approved, as a launch does, applies environment
// overrides, and validates the result with defaults. It returns the explicitly chosen settings (before defaults)
// and names the scope that most likely holds a problem: project when an approved project file exists.
func doctorEffective(home, root string, global config.Config) (config.Config, string, error) {
	scope := "global"
	cfg := global
	if root != "" && !config.SameScope(home, root) {
		path := config.ProjectPath(root)
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return cfg, "project", fmt.Errorf("reading %s: %w", path, err)
		default:
			if ledger, lerr := config.LoadTrust(home); lerr == nil && ledger[path] == config.Digest(string(data)) {
				local, derr := config.Decode(path, data)
				if derr != nil {
					return cfg, "project", derr
				}
				cfg = config.Merge(global, local)
				scope = "project"
			}
		}
	}
	explicit := config.Environment(cfg)
	if err := config.Validate(config.Defaults(explicit)); err != nil {
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
	ledger, err := config.LoadTrust(home)
	if err != nil {
		return []doctorItem{{check: check{name: "trust", note: err.Error()}, fix: "remove the unreadable file in ~/.codeforge"}}
	}
	persona := doctorFile("persona", filepath.Join(root, repoPersonaPath), ledger, "codeforge init", "codeforge trust")
	project := doctorFile("config", config.ProjectPath(root), ledger, "", "codeforge trust --models")
	return slices.DeleteFunc([]doctorItem{persona, project}, func(d doctorItem) bool { return d.name == "" })
}

// doctorFile reports one repository file. A missing persona is a warning; a missing config is normal.
func doctorFile(name, path string, ledger config.Store, createFix, trustFix string) doctorItem {
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
	if ledger[path] == config.Digest(content) {
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

// checkForms looks for the program that asks questions at the terminal; without it every menu and prompt fails.
func checkForms() check {
	p, err := forms.Helper()
	if err != nil {
		return check{name: "forms", note: "codeforge-forms not found"}
	}
	return check{name: "forms", ok: true, note: p}
}
