package src

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/codeliter/codeforge/src/config"
)

// prepared is what every mode and every Empryo-bound command needs: where codeforge, Empryo and the repository are,
// the global configuration, and the managed profiles, persona skill and hooks installed and current.
type prepared struct {
	home, project, empryoDir, workspace, self, hookBin string
	globalCfg                                          config.Config
}

// prepare installs the managed assets for the current workspace. Hooks named in repair are the legacy ones to replace.
func prepare(ctx context.Context, home string, repair repairOptions) (prepared, error) {
	p := prepared{home: home, empryoDir: filepath.Join(home, ".empryo")}
	var err error
	if p.project, err = repoRoot(ctx); err != nil {
		return p, err
	}
	if p.globalCfg, err = config.LoadBase(home); err != nil {
		return p, err
	}
	if err := syncAssets(p.empryoDir, maintenanceProfileConfig(p.globalCfg)); err != nil {
		return p, fmt.Errorf("installing profiles: %w", err)
	}
	if p.workspace, err = os.Getwd(); err != nil {
		return p, fmt.Errorf("finding workspace: %w", err)
	}
	if err := installPersonaSkill(home); err != nil {
		return p, fmt.Errorf("installing persona: %w", err)
	}
	if p.self, err = os.Executable(); err != nil {
		return p, fmt.Errorf("finding codeforge binary: %w", err)
	}
	if p.hookBin, err = installHookBinary(p.empryoDir, p.self); err != nil {
		return p, fmt.Errorf("installing hook executable: %w", err)
	}
	if err := installHooks(p.empryoDir, p.hookBin, repair.legacyHooks...); err != nil {
		return p, fmt.Errorf("installing guard: %w", err)
	}
	if err := removeGHShim(p.empryoDir); err != nil {
		fmt.Fprintf(os.Stderr, "codeforge: could not remove the old gh shim (%v); delete ~/.empryo/bin/gh yourself\n", err)
	}
	return p, nil
}

// finishRepair completes `codeforge repair`: the project profiles are rebuilt and the repaired version is recorded, so
// that the automatic repair does not run again for it.
func (p prepared) finishRepair(w io.Writer) error {
	if err := syncProjectAgents(p.workspace, maintenanceProfileConfig(p.globalCfg)); err != nil {
		return err
	}
	if Version != "dev" {
		if err := writeFile(config.Dir(p.home), repairStampFile, []byte(Version+"\n"), privateFile); err != nil {
			return fmt.Errorf("recording repaired version: %w", err)
		}
	}
	fmt.Fprintf(w, "codeforge: repaired profiles, skills and hooks, and removed the old gh shim. Hook executable: %s\n", p.hookBin)
	fmt.Fprintln(w, "Personas, sessions and logins were kept. Start a new Codeforge session to use the repaired hooks.")
	return nil
}
