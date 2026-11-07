package src

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/codeliter/codeforge/src/review"
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
		printHelp(os.Stdout, wantsAllHelp(args[1:]))
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

	base := ctx

	// A mistyped mode such as `codeforge zsh` stops here, before any repair, model lookup or paid judge call.
	if err := unknownCommand(args); err != nil {
		return err
	}

	// doctor only reads state, so it runs before automatic repair, project, config and Empryo preparation.
	if len(args) > 0 && args[0] == "doctor" {
		if len(args) > 1 {
			return errDoctorUsage
		}
		return runDoctor(ctx, home, os.Stdout)
	}

	repair, err := parseRepairArgs(args)
	if err != nil {
		return err
	}
	// After an upgrade, refresh managed hooks and profiles once, before any mode or command runs.
	// Repair runs in every mode, review included, and before a review's clock starts, so it never costs review time.
	if !repair.enabled {
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
	if len(args) > 0 && (args[0] == "setup" || args[0] == "init" && !slices.Contains(args, "--repo")) {
		if err := runSetup(ctx, home, args[1:], os.Stdout); err != nil {
			return err
		}
		if args[0] == "setup" {
			return nil
		}
		args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--yes" || a == "-y" })
	}

	if len(args) > 0 && (args[0] == "models" || args[0] == "provider" || args[0] == "providers") {
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
	if err := removeGHShim(empryoDir); err != nil {
		fmt.Fprintf(os.Stderr, "codeforge: could not remove the old gh shim (%v); delete ~/.empryo/bin/gh yourself\n", err)
	}

	if repair.enabled {
		if err := syncProjectAgents(workspace, maintenanceProfileConfig(globalCfg)); err != nil {
			return err
		}
		if Version != "dev" {
			if err := writeFile(configDir(home), repairStampFile, []byte(Version+"\n"), privateFile); err != nil {
				return fmt.Errorf("recording repaired version: %w", err)
			}
		}
		fmt.Fprintf(os.Stdout, "codeforge: repaired profiles, skills and hooks, and removed the old gh shim. Hook executable: %s\n", hookBin)
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
	// A person choosing a mode must never wait on subprocess preparation, and their thinking time is not review time:
	// the picker runs first, and a review chosen there starts its clock at the choice.
	picked := ""
	if _, resume, serr := sessionID(args); serr == nil && !resume && !isModeWord(args) {
		if picked, err = pickMode(cfg, args); err != nil {
			return err
		}
	}
	bin, err := findEmpryo(empryoDir)
	if err != nil {
		return err
	}
	var catalog []string
	cachedCatalog := false
	if needsModelCatalog(cfg) || argumentsNeedCatalog(args) {
		if catalog, cachedCatalog, err = launchCatalog(ctx, home, bin); err != nil {
			return err
		}
	}
	// resolve names the models and checks the native fallback settings against them.
	resolve := func() (ModelPlan, []string, error) {
		plan, err := resolveModelPlan(cfg, catalog)
		if err != nil {
			return plan, nil, err
		}
		resolved, overrides, err := resolveModelArguments(args, cfg.Providers, catalog)
		if err != nil {
			return plan, nil, err
		}
		plan.Routes = append(plan.Routes, overrides...)
		if len(plan.fallbackChains()) > 0 {
			fmt.Fprintln(os.Stderr, "codeforge: checking existing native fallback settings (read-only)...")
			if err := verifyModelPlan(ctx, bin, workspace, plan); err != nil {
				return plan, nil, err
			}
		}
		return plan, resolved, nil
	}
	plan, resolvedArgs, err := resolve()
	if err != nil && cachedCatalog {
		// A saved catalog can be stale after a sign-in, a sign-out or a models apply: fetch fresh once before
		// rejecting the plan.
		fmt.Fprintln(os.Stderr, "codeforge: refreshing the saved model list...")
		if catalog, err = freshLaunchCatalog(ctx, home, bin, sortedCopy(readyProviders(ctx, bin))); err != nil {
			return err
		}
		plan, resolvedArgs, err = resolve()
	}
	if err != nil {
		return err
	}
	args = resolvedArgs
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
		if cmd = picked; cmd == "" {
			if cmd, err = pickMode(cfg, rest); err != nil {
				return err
			}
		}
		if mode, reason := suggestMode(rest); mode == cmd {
			fmt.Fprintln(os.Stderr, "codeforge:", reason)
		}
	}
	if !resumed {
		acknowledgeTask(os.Stderr, cmd, rest)
	}
	// The review's clock starts here, after the setup above (repair, model catalog, enforcement, mode choice), so setup
	// time is never charged to it. It covers everything the review itself does: collecting the change, classifying it,
	// the jurors and the engine.
	if cmd == modeReview && !resumed && !review.HasDeadline(ctx) {
		end := time.Now().Add(review.HardLimit)
		pipe := &review.Pipeline{}
		base = review.WithPipeline(review.WithDeadline(base, end), pipe)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(review.WithPipeline(review.WithDeadline(ctx, end), pipe), end)
		defer cancel()
	}
	mode, args, err := modeArgs(ctx, bin, cfg, cmd, rest)
	if err != nil {
		return err
	}
	if mode == modeTicket && !resumed && !hasFlag(args, "--headless") {
		// Ticket work is reviewed by other models before the TUI opens; blocking findings go back to the agent.
		return launchWith(base, home, bin, mode, args, ticketAfterTask(home, bin, cfg))
	}
	return launch(base, home, bin, mode, args)
}

// isModeWord reports whether the first argument already names the mode or command Run dispatches on, so no pick is due.
func isModeWord(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "init", "gate", "respond", modeTicket, "start", modeChat, modeWork, modeReview:
		return true
	}
	return false
}

// launch supervises the terminal UI so the resume hint appears after it closes.
func launch(ctx context.Context, home, bin, mode string, args []string) error {
	return launchWith(ctx, home, bin, mode, args, nil)
}

// launchWith runs after between the first headless turn and the TUI, e.g. ticket mode's review gate.
func launchWith(ctx context.Context, home, bin, mode string, args []string, after func(context.Context, string)) error {
	// Preparing the first review launch runs git and opens storage; it is under the review's real deadline, while the
	// engine itself is supervised by runEngine and keeps only the clock so that it can still save its session.
	prepCtx := ctx
	if mode == modeReview && review.HasDeadline(ctx) {
		var cancel context.CancelFunc
		prepCtx, cancel = context.WithDeadline(ctx, review.Deadline(ctx))
		defer cancel()
	}
	argv, env, err := launchCommand(prepCtx, home, bin, mode, args)
	if err != nil {
		return err
	}
	// A review has a hard limit that only a supervising parent can enforce, so it is never handed over with exec.
	if mode == modeReview && review.HasDeadline(ctx) && hasFlag(args, "--headless") {
		err := runEngine(ctx, argv, env, review.AgentEnd(ctx))
		finishLeads(ctx, err)
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
			var limit time.Time
			if mode == modeReview && review.HasDeadline(ctx) {
				limit = review.AgentEnd(ctx)
			}
			err := runEngine(ctx, initial, env, limit)
			finishLeads(ctx, err)
			if err != nil {
				return err
			}
			id := flagValue(argv, "--session-id")
			if after != nil {
				after(ctx, id)
			}
			argv, env, err = launchCommand(ctx, home, bin, mode, []string{"--session", id})
			if err != nil {
				return err
			}
		}
		restore := showModeTitle(os.Stdout, mode)
		defer restore()
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
	userSystem := hasFlag(args, "--system")
	if args, err = withRepoPersona(ctx, home, args); err != nil {
		return nil, nil, err
	}
	if mode == modeReview && !resume {
		args = withSystemNote(args, review.LimitNote())
	}
	if !userSystem && !resume && mode != modeReview {
		args = withLearnedHabits(ctx, home, args)
	}
	// The human marker is decided here, never inherited: an agent cannot claim it by exporting it first.
	nested := os.Getenv(launchMarker) == "1"
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, humanLaunchMarker+"=") })
	env = append(env, "EMPRYO_HOOK_ENV=CODEFORGE_*", launchMarker+"=1", "CODEFORGE_RESUME_HINT=launcher")
	if humanLaunch(interactive(), nested) {
		env = append(env, humanLaunchMarker+"=1")
	}
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
		args, err = reviewArgs(ctx, bin, cfg.Review, judgesFor(bin, cfg.Judges, nil), jurorsFor(bin), args)
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

	description := ""
	if args, description, err = startArgs(args); err != nil {
		return modeTicket, nil, err
	}
	// The judges score the description alone, not the skill and ticket lines added around it.
	if !hasEffort(args) {
		if err := checkEffort(args); err != nil {
			return modeTicket, nil, err
		}
		args = append([]string{"--effort", classify(ctx, bin, description, cfg.Judges).String()}, args...)
	}
	args, err = taskArgs(ctx, bin, cfg, args)
	return modeTicket, args, err
}

// finishLeads ends the jurors started beside a review: after a successful run it prints what they found, after a failed
// one it only stops them, so the engine's own error comes back at once and nothing is left running.
func finishLeads(ctx context.Context, runErr error) {
	if runErr == nil {
		printLeads(ctx)
		return
	}
	if pipe := review.PipelineOf(ctx); pipe != nil {
		pipe.Stop()
	}
}

// printLeads prints what the second-opinion jurors found, after the review they ran alongside. It waits for them at
// most review.JurorGrace, then stops them.
func printLeads(ctx context.Context) {
	if pipe := review.PipelineOf(ctx); pipe != nil {
		if leads := pipe.Leads(ctx); leads != "" {
			// Standard error, so a --json run keeps a machine-readable standard output.
			fmt.Fprint(os.Stderr, "\n"+leads)
		}
	}
}
