package src

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// Hook is the guard Empryo runs before every tool call; it skips setup and returns the hook exit code.
func Hook(ctx context.Context) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge guard: finding home dir:", err)
		return hookExitBlock
	}
	return runHook(ctx, home, os.Stdin, os.Stderr)
}

// PrePush is git's pre-push hook. It returns the exit code git uses to allow or stop the push.
func PrePush(ctx context.Context) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge: finding home dir:", err)
		return 1
	}
	return runPrePush(ctx, home, os.Stdin, os.Stderr)
}

// GHShim checks a gh call and then runs the real gh in its place.
func GHShim(ctx context.Context, args []string) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge: finding home dir:", err)
		return 1
	}
	return runGHShim(ctx, home, args)
}

// Run dispatches a codeforge command and launches Empryo.
func Run(ctx context.Context, args []string) error {
	if len(args) > 0 && isHelp(args[0]) {
		printHelp(os.Stdout)
		return nil
	}
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("codeforge", Version)
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("finding home dir: %w", err)
	}

	// learn needs no Empryo or asset sync, and its outcome argv must reach the check untouched, --cwd included.
	if len(args) > 0 && args[0] == "learn" {
		return runLearn(ctx, home, args[1:], os.Stdout)
	}
	if len(args) > 0 && args[0] == "allow" {
		return errAllowRemoved
	}

	// Enforcement, review collection, persona and sandbox all follow the directory Empryo will work in.
	if dir := flagValue(args, "--cwd"); dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("changing to --cwd %s: %w", dir, err)
		}
		args = dropFlag(args, "--cwd")
		if len(args) > 0 && args[0] == "learn" {
			return errLearnCwd
		}
	}

	repair, err := parseRepairArgs(args)
	if err != nil {
		return err
	}

	// Machine setup runs before anything that needs Empryo, because it installs and signs in to Empryo.
	// init runs it first and then continues with the persona; setup stops after it.
	if len(args) > 0 && (args[0] == "setup" || args[0] == "init" && !slices.Contains(args, "--repo")) {
		if err := runSetup(ctx, home, args[1:], os.Stdout); err != nil {
			return err
		}
		if args[0] == "setup" {
			return nil
		}
		args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--yes" || a == "-y" })
	}

	if len(args) > 0 && args[0] == "models" {
		bin, err := findEmpryo(filepath.Join(home, ".empryo"))
		if err != nil {
			return err
		}
		return runModels(ctx, home, bin, args[1:])
	}
	project, err := repoRoot(ctx)
	if err != nil {
		return err
	}
	globalCfg, err := loadBaseConfig(home)
	if err != nil {
		return err
	}

	empryoDir := filepath.Join(home, ".empryo")
	if err := syncAssets(empryoDir, maintenanceProfileConfig(globalCfg)); err != nil {
		return fmt.Errorf("installing profiles: %w", err)
	}
	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
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
	if err := installHooks(empryoDir, hookBin, repair.legacyHooks...); err != nil {
		return fmt.Errorf("installing guard: %w", err)
	}
	if err := installGHShim(empryoDir, hookBin); err != nil {
		return fmt.Errorf("installing gh guard: %w", err)
	}

	if repair.enabled {
		if err := syncProjectAgents(workspace, maintenanceProfileConfig(globalCfg)); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "codeforge: repaired profiles, skills, hooks and gh shim. Hook executable: %s\n", hookBin)
		fmt.Fprintln(os.Stdout, "Personas, sessions and logins were kept. Start a new Codeforge session to use the repaired hooks.")
		return nil
	}

	if len(args) > 0 && args[0] == "trust" {
		if len(args) == 1 {
			return runTrust(ctx, home)
		}
		if len(args) == 2 && args[1] == "--models" {
			return runTrustModels(home, project)
		}
		return fmt.Errorf("%w: use codeforge trust [--models]", errInitUsage)
	}
	cfg, err := loadConfigAt(home, project)
	if err != nil {
		return err
	}
	bin, err := findEmpryo(empryoDir)
	if err != nil {
		return err
	}
	var catalog []string
	if needsModelCatalog(cfg) || argumentsNeedCatalog(args) {
		fmt.Fprintln(os.Stderr, "codeforge: request received; resolving model names through connected providers...")
		catalog, err = connectedModelCatalog(ctx, bin)
		if err != nil {
			return err
		}
	}
	plan, err := resolveModelPlan(cfg, catalog)
	if err != nil {
		return err
	}
	args, overrides, err := resolveModelArguments(args, cfg.Providers, catalog)
	if err != nil {
		return err
	}
	plan.Routes = append(plan.Routes, overrides...)
	if len(plan.fallbackChains()) > 0 {
		fmt.Fprintln(os.Stderr, "codeforge: checking existing native fallback settings (read-only)...")
		if err := verifyModelPlan(ctx, bin, workspace, plan); err != nil {
			return err
		}
	}
	cfg = plan.Config
	if base, err := resolveModelPlan(globalCfg, catalog); err == nil {
		if err := syncAssets(empryoDir, base.Config); err != nil {
			return err
		}
	}
	if err := syncProjectAgents(workspace, cfg); err != nil {
		return fmt.Errorf("installing project profiles: %w", err)
	}

	cmd, rest := "", args
	if len(args) > 0 {
		cmd, rest = args[0], args[1:]
	}

	// Removes the retired pre-push hook from repositories that still register it.
	if err := ensureEnforced(ctx, self); err != nil {
		return err
	}

	switch cmd {
	case "init":
		return runInit(ctx, home, bin, cfg, rest)
	case "gate":
		return runGate(ctx, home, bin, cfg, os.Stdout)
	case "respond":
		if _, resume, err := sessionID(rest); err != nil {
			return err
		} else if resume {
			return errSessionArgs
		}
		acknowledgeTask(os.Stderr, "respond", rest)
		var done bool
		if args, done, err = respondArgs(ctx, rest); err != nil || done {
			return err
		}
		if cfg.Model != "" && !hasFlag(args, "--model") {
			args = append([]string{"--model", cfg.Model}, args...)
		}
		return launch(ctx, home, bin, modeTicket, args)
	case modeTicket, "start", modeChat, modeWork, modeReview:
	default:
		cmd, rest = "", args
	}

	cmd, resumed, err := restoreMode(ctx, home, cmd, rest)
	if err != nil {
		return err
	}
	if !resumed && cmd == "" {
		if cmd, err = pickMode(cfg, rest); err != nil {
			return err
		}
	}
	if !resumed {
		acknowledgeTask(os.Stderr, cmd, rest)
	}
	mode, args, err := modeArgs(ctx, bin, cfg, cmd, rest)
	if err != nil {
		return err
	}
	return launch(ctx, home, bin, mode, args)
}

// launch supervises the terminal UI so the resume hint appears after it closes.
func launch(ctx context.Context, home, bin, mode string, args []string) error {
	argv, env, err := launchCommand(ctx, home, bin, mode, args)
	if err != nil {
		return err
	}
	if !hasFlag(args, "--headless") {
		_, resume, err := sessionID(args)
		if err != nil {
			return err
		}
		if !resume && promptOf(args) != "" {
			// Empryo's TUI does not submit positional prompts. Submit every mode's
			// initial task through its headless API, then reopen the saved conversation.
			fmt.Fprintln(os.Stderr, "codeforge: context ready; waiting for the model. The TUI opens when the task finishes.")
			initial := append(append([]string(nil), argv...), "--headless", "--save-session")
			if err := runInteractive(ctx, initial, env); err != nil {
				return err
			}
			id := flagValue(argv, "--session-id")
			argv, env, err = launchCommand(ctx, home, bin, mode, []string{"--session", id})
			if err != nil {
				return err
			}
		}
		return runInteractive(ctx, argv, env)
	}
	if err := syscall.Exec(argv[0], argv, env); err != nil {
		return fmt.Errorf("starting empryo: %w", err)
	}
	return nil
}

func launchCommand(ctx context.Context, home, bin, mode string, args []string) (command []string, environment []string, result error) {
	if mode == "" || validMode(mode) != nil {
		return nil, nil, errUnknownMode
	}
	_, resume, err := sessionID(args)
	if err != nil {
		return nil, nil, err
	}
	forbidden := []string{"--profile", "--agent", "--no-profile"}
	if resume || mode == modeReview {
		forbidden = append(forbidden, "--mode", "--plan", "--auto", "--lite")
	}
	for _, flag := range forbidden {
		if hasFlag(args, flag) {
			return nil, nil, errSessionOverride
		}
	}
	if args, err = withRepoPersona(ctx, home, args); err != nil {
		return nil, nil, err
	}
	env := append(os.Environ(), "EMPRYO_HOOK_ENV=CODEFORGE_*", launchMarker+"=1", "CODEFORGE_RESUME_HINT=launcher")
	argv := append([]string{bin, "--profile", profileFor(mode)}, args...)
	if mode == modeReview {
		env = append(env, "CODEFORGE_GUARD=review")
		argv = append([]string{bin, "--profile", profileFor(mode), "--mode", "plan"}, args...)
		if argv, err = readOnlyRepo(ctx, argv); err != nil {
			return nil, nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("launch cancelled: %w", err)
	}
	// Close storage before starting Empryo, including review's read-only sandbox.
	savedArgs, err := persistSession(ctx, home, mode, args)
	if err != nil {
		return nil, nil, err
	}
	argv = append(argv[:len(argv)-len(args)], savedArgs...)
	return argv, env, nil
}

func modeArgs(ctx context.Context, bin string, cfg Config, cmd string, args []string) (string, []string, error) {
	// Resumes keep their saved task and settings instead of preparing a new task.
	if _, resume, err := sessionID(args); err != nil {
		return cmd, nil, err
	} else if resume {
		if err := checkEffort(args); err != nil {
			return cmd, nil, err
		}
		if cmd == "start" {
			cmd = modeTicket
		}
		return cmd, args, nil
	}
	var err error
	switch cmd {
	case modeReview:
		args, err = reviewArgs(ctx, cfg.Review, args)
		return modeReview, args, err
	case modeWork:
		// Skip effort judges for conversational work; explicit effort still wins.
		if !hasEffort(args) {
			args = append([]string{"--effort", "medium"}, args...)
		}
		args, err = taskArgs(ctx, bin, cfg, args)
		return modeWork, args, err
	case modeChat:
		if promptOf(args) == "" && !hasEffort(args) {
			args = append([]string{"--effort", "low"}, args...)
		}
		args, err = taskArgs(ctx, bin, cfg, args)
		return cmd, args, err
	}

	if args, err = startArgs(args); err != nil {
		return modeTicket, nil, err
	}
	args, err = taskArgs(ctx, bin, cfg, args)
	return modeTicket, args, err
}
