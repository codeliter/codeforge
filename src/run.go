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

// Run dispatches a codeforge command and, for launches, replaces the process with Empryo.
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

	// Enforcement, review collection, persona and sandbox all follow the directory Empryo will work in.
	if dir := flagValue(args, "--cwd"); dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("changing to --cwd %s: %w", dir, err)
		}
		args = dropFlag(args, "--cwd")
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

	cfg, err := loadConfig(home)
	if err != nil {
		return err
	}

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
	if err := installHooks(empryoDir, self); err != nil {
		return fmt.Errorf("installing guard: %w", err)
	}
	if err := installGHShim(empryoDir, self); err != nil {
		return fmt.Errorf("installing gh guard: %w", err)
	}

	bin, err := findEmpryo(empryoDir)
	if err != nil {
		return err
	}

	cmd, rest := "", args
	if len(args) > 0 {
		cmd, rest = args[0], args[1:]
	}

	// Push and PR rules are enforced by git and the gh shim, so codeforge never starts without them.
	if err := ensureEnforced(ctx, self); err != nil {
		return err
	}

	switch cmd {
	case "init":
		return runInit(ctx, home, bin, cfg, rest)
	case "models":
		return runModels(ctx, home, bin, rest)
	case "trust":
		return runTrust(ctx, home)
	case "gate":
		return runGate(ctx, home, bin, cfg, os.Stdout)
	case "allow":
		return runAllow(ctx, home, rest)
	case "respond":
		var done bool
		if args, done, err = respondArgs(ctx, rest); err != nil || done {
			return err
		}
		if cfg.Model != "" && !hasFlag(args, "--model") {
			args = append([]string{"--model", cfg.Model}, args...)
		}
		return launch(ctx, home, bin, modeTicket, args)
	case modeTicket, "start", modeChat, modeReview:
	default:
		if cmd, err = pickMode(cfg, args); err != nil {
			return err
		}
		rest = args
	}

	mode, args, err := modeArgs(ctx, bin, cfg, cmd, rest)
	if err != nil {
		return err
	}
	return launch(ctx, home, bin, mode, args)
}

// launch adds the repository persona and replaces this process with Empryo in the given mode.
func launch(ctx context.Context, home, bin, mode string, args []string) error {
	var err error
	if args, err = withRepoPersona(ctx, home, args); err != nil {
		return err
	}

	env := append(os.Environ(), "EMPRYO_HOOK_ENV=CODEFORGE_*")
	argv := append([]string{bin, "--profile", profileFor(mode)}, args...)
	if mode == modeReview {
		env = append(env, "CODEFORGE_GUARD=review")
		argv = append([]string{bin, "--profile", profileFor(mode), "--mode", "plan"}, args...)
		if argv, err = readOnlyRepo(ctx, argv); err != nil {
			return err
		}
	}

	if err := syscall.Exec(argv[0], argv, env); err != nil {
		return fmt.Errorf("starting empryo: %w", err)
	}
	return nil
}

func modeArgs(ctx context.Context, bin string, cfg Config, cmd string, args []string) (string, []string, error) {
	// Resumes keep their saved task and settings instead of preparing a new task.
	for _, flag := range []string{"--resume", "--session", "-s"} {
		if hasFlag(args, flag) {
			if err := checkEffort(args); err != nil {
				return cmd, nil, err
			}
			if cmd == "start" {
				cmd = modeTicket
			}
			return cmd, args, nil
		}
	}
	var err error
	switch cmd {
	case modeReview:
		args, err = reviewArgs(ctx, cfg.Review, args)
		return modeReview, args, err
	case modeChat:
		if promptOf(args) == "" && !hasEffort(args) {
			args = append([]string{"--effort", "low"}, args...)
		}
		args, err = taskArgs(ctx, bin, cfg, args)
		return modeChat, args, err
	}

	if args, err = startArgs(args); err != nil {
		return modeTicket, nil, err
	}
	args, err = taskArgs(ctx, bin, cfg, args)
	return modeTicket, args, err
}
