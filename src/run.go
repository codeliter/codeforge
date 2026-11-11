package src

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/codeliter/codeforge/src/guard"
)

// Hook is the guard Empryo runs before every tool call; it skips setup and returns the hook exit code.
func Hook(ctx context.Context) int {
	return withHome("codeforge guard", hookExitBlock, func(home string) int { return runHook(ctx, home, os.Stdin, os.Stderr) })
}

// PrePush is git's pre-push hook. It returns the exit code git uses to allow or stop the push.
func PrePush(ctx context.Context) int {
	return withHome("codeforge", 1, func(home string) int { return runPrePush(ctx, home, os.Stdin, os.Stderr) })
}

// GHShim checks a gh call and then runs the real gh in its place.
func GHShim(ctx context.Context, args []string) int {
	return withHome("codeforge", 1, func(home string) int { return runGHShim(ctx, home, args) })
}

// withHome runs a hook with the user's home directory, or says why there is none and returns fail.
func withHome(prefix string, fail int, run func(home string) int) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, prefix+": finding home dir:", err)
		return fail
	}
	return run(home)
}

// first is the command word, or "" when there is none.
func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// Run dispatches a codeforge command and launches Empryo. Commands are answered in the order of what they need: help
// and version need nothing, learn and doctor need only the home directory, setup, models and provider need Empryo, and
// everything else needs the managed profiles and hooks installed first.
func Run(ctx context.Context, args []string) error {
	// Help comes before everything else, for every command: it reads no config and writes no state, so it is safe in
	// review mode too. A subcommand's help is the full reference, which lists that command; plain help is the short one.
	if len(args) > 0 && isHelp(args[0]) {
		printHelp(os.Stdout, wantsAllHelp(args[1:]))
		return nil
	}
	if WantsHelp(args) {
		printHelp(os.Stdout, true)
		return nil
	}
	if first(args) == "version" || first(args) == "--version" {
		fmt.Println("codeforge", Version)
		return nil
	}
	// Completion only prints, and Homebrew runs it while installing, so it reads no config and repairs nothing.
	if first(args) == "completion" {
		return runCompletion(os.Stdout, args[1:])
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("finding home dir: %w", err)
	}

	// learn needs no Empryo or asset sync, and its outcome argv must reach the check untouched, --cwd included.
	switch first(args) {
	case "learn":
		return runLearn(ctx, home, args[1:], os.Stdout)
	case "allow":
		return errAllowRemoved
	}
	if args, err = changeToCwd(args); err != nil {
		return err
	}

	// A mistyped mode such as `codeforge zsh` stops here, before any repair, model lookup or paid judge call.
	if err := unknownCommand(args); err != nil {
		return err
	}

	// persona only reads, like doctor below, so it runs before anything is repaired or prepared.
	if first(args) == "persona" {
		return runPersona(ctx, home, args[1:], os.Stdout)
	}

	// The gate's own state (accepted findings, the run cap) needs no models or Empryo: only the repository and the branch.
	if first(args) == "gate" && len(args) > 1 {
		return runGateCommand(ctx, home, args[1:], os.Stdout)
	}
	// So are the findings accepted on a PR: they live in the record of the user's posted review.
	if isReviewAccept(args) {
		return runReviewAccept(ctx, args[1:], os.Stdout)
	}

	// doctor only reads state, so it runs before automatic repair, project, config and Empryo preparation.
	if first(args) == "doctor" {
		if len(args) > 1 {
			return errDoctorUsage
		}
		return runDoctor(ctx, home, os.Stdout)
	}

	// sessions picks before anything is repaired or prepared, so that work goes to the project the session opens in.
	// The picked session then opens as --resume opens it: in its own mode, with codeforge's profile and hooks.
	if first(args) == "sessions" {
		dir, id, err := runSessions(ctx, home, args[1:], os.Stdout)
		if err != nil || id == "" {
			return err
		}
		if wd, err := os.Getwd(); err == nil && !sameDir(wd, dir) {
			return Run(ctx, []string{"--cwd", dir, "--resume", id}) // another project's session opens there
		}
		args = []string{"--resume", id}
	}

	repair, err := parseRepairArgs(args)
	if err != nil {
		return err
	}
	// After an upgrade, refresh managed hooks and profiles once, before any mode or command runs.
	// Repair runs in every mode, review included, and before a review's clock starts, so it never costs review time.
	// A call that only prints (models, provider show, ...) is allowed in review mode, so it writes nothing: no repair.
	if !repair.enabled && !guard.ReadOnlyCodeforge(args) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("finding workspace: %w", err)
		}
		if err := autoRepair(home, wd, os.Stderr); err != nil {
			return err
		}
	}

	// Machine setup runs before anything that needs Empryo, because it installs and signs in to Empryo.
	// init runs it first and then continues with the persona; setup stops after it.
	if first(args) == "setup" || first(args) == "init" && !slices.Contains(args, "--repo") {
		if err := runSetup(ctx, home, args[1:], os.Stdout); err != nil {
			return err
		}
		if args[0] == "setup" {
			return nil
		}
		args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--yes" || a == "-y" })
	}

	switch first(args) {
	case "models", "provider", "providers":
		bin, err := findEmpryo(filepath.Join(home, ".empryo"))
		if err != nil {
			return err
		}
		// These commands change providers, models and fallbacks, so the saved model list is rebuilt next launch.
		if changesProviders(args) {
			defer clearCatalogCache(home)
		}
		if args[0] != "models" {
			return runProvider(ctx, home, bin, args[1:], os.Stdout)
		}
		return runModels(ctx, home, bin, args[1:])
	}

	p, err := prepare(ctx, home, repair)
	if err != nil {
		return err
	}
	if repair.enabled {
		return p.finishRepair(os.Stdout)
	}
	if first(args) == "trust" {
		switch {
		case len(args) == 1:
			return runTrust(ctx, home)
		case len(args) == 2 && args[1] == "--models":
			return runTrustModels(home, p.project)
		}
		return fmt.Errorf("%w: use codeforge trust [--models]", errInitUsage)
	}
	return runMode(ctx, p, args)
}

// changeToCwd moves into --cwd, the directory Empryo will work in, so that enforcement, review collection, persona and
// sandbox all follow it. learn is refused after it: its outcome argv must reach the check untouched.
func changeToCwd(args []string) ([]string, error) {
	dir := flagValue(args, "--cwd")
	if dir == "" {
		return args, nil
	}
	if err := os.Chdir(dir); err != nil {
		return nil, fmt.Errorf("changing to --cwd %s: %w", dir, err)
	}
	args = dropFlag(args, "--cwd")
	if first(args) == "learn" {
		return nil, errLearnCwd
	}
	return args, nil
}
