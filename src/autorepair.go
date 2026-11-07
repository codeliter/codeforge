package src

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/codeliter/codeforge/src/config"
)

const repairStampFile = "repaired-version"

// autoRepairVersion reports the version whose managed files must be refreshed, or "" when none is due.
// Development builds never repair on their own. Agent shells inside a Codeforge session never repair either:
// only the user's own launch may rewrite the hooks that guard that session.
func autoRepairVersion(home string) string {
	// A review session is read-only, so a codeforge run from its shell never rewrites the hooks that guard it, whether or
	// not the launch marker came through.
	if Version == "" || Version == "dev" || os.Getenv(launchMarker) == "1" || os.Getenv("CODEFORGE_GUARD") == "review" {
		return ""
	}
	// A missing or unreadable stamp repairs again: a needless repair is cheap, a skipped one leaves stale hooks.
	data, err := os.ReadFile(filepath.Join(config.Dir(home), repairStampFile))
	if err == nil && strings.TrimSpace(string(data)) == Version {
		return ""
	}
	return Version
}

// autoRepair runs codeforge repair once after an upgrade, before any mode or command starts.
func autoRepair(home, workspace string, w io.Writer) error {
	version := autoRepairVersion(home)
	if version == "" {
		return nil
	}
	fmt.Fprintf(w, "codeforge: updated to %s; repairing hooks, profiles and skills...\n", version)
	if err := repairManagedFiles(home, workspace, nil); err != nil {
		return fmt.Errorf("automatic repair after upgrade failed (run codeforge repair): %w", err)
	}
	if err := writeFile(config.Dir(home), repairStampFile, []byte(version+"\n"), privateFile); err != nil {
		return fmt.Errorf("recording repaired version: %w", err)
	}
	fmt.Fprintln(w, "codeforge: repair done. Personas, sessions and logins were kept.")
	return nil
}

// repairManagedFiles rebuilds everything codeforge repair owns; user data is never touched.
func repairManagedFiles(home, workspace string, legacyHooks []string) error {
	globalCfg, err := config.LoadBase(home)
	if err != nil {
		globalCfg = config.Config{}
	}
	cfg := maintenanceProfileConfig(globalCfg)
	empryoDir := filepath.Join(home, ".empryo")
	if err := syncAssets(empryoDir, cfg); err != nil {
		return fmt.Errorf("installing profiles: %w", err)
	}
	if err := installPersonaSkill(home); err != nil {
		return fmt.Errorf("installing persona: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding codeforge binary: %w", err)
	}
	hookBin, err := installHookBinary(empryoDir, self)
	if err != nil {
		return fmt.Errorf("installing hook executable: %w", err)
	}
	if err := installHooks(empryoDir, hookBin, legacyHooks...); err != nil {
		return fmt.Errorf("installing guard: %w", err)
	}
	if err := removeGHShim(empryoDir); err != nil {
		// A stale shim only bypasses nothing now, so a permissions problem must not block every launch.
		fmt.Fprintf(os.Stderr, "codeforge: could not remove the old gh shim (%v); delete ~/.empryo/bin/gh yourself\n", err)
	}
	return syncProjectAgents(workspace, cfg)
}
