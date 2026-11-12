package src

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/review"
)

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
		_, err := runReviewEngine(ctx, argv, append(env, noAskEnv+"=1"), review.AgentEnd(ctx))
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
			if mode != modeReview {
				fmt.Fprintln(os.Stderr, "codeforge: context ready; waiting for the model. The TUI opens when the task finishes.")
			}
			initial := append(append([]string(nil), argv...), "--headless", "--save-session")
			var limit time.Time
			if mode == modeReview && review.HasDeadline(ctx) {
				limit = review.AgentEnd(ctx)
			}
			// This run is headless, so nobody can approve a call it asks about; the TUI that follows can.
			chat, err := runReviewEngine(ctx, initial, append(slices.Clone(env), noAskEnv+"=1"), limit)
			finishLeads(ctx, err)
			id := flagValue(argv, "--session-id")
			if err != nil {
				return err
			}
			if !chat {
				// A review is done when its view closes; the chat opens only when asked for (c), and stays reachable.
				fmt.Fprintf(os.Stderr, "codeforge: to ask about this review later: codeforge --resume %s\n", id)
				return nil
			}
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
		return runThemed(ctx, home, argv, env)
	}
	if err := syscall.Exec(argv[0], argv, env); err != nil {
		return fmt.Errorf("starting empryo: %w", err)
	}
	return nil
}

func launchCommand(ctx context.Context, home, bin, mode string, args []string) (command []string, environment []string, result error) {
	if mode == "" || config.ValidMode(mode) != nil {
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
	if args, err = withRepoPersonaFor(ctx, home, mode, args); err != nil {
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

func modeArgs(ctx context.Context, bin string, cfg config.Config, cmd string, args []string) (string, []string, error) {
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
		args, err = taskArgs(cfg, args)
		return modeWork, args, err
	case modeChat:
		if promptOf(args) == "" && !hasEffort(args) {
			args = append([]string{"--effort", "low"}, args...)
		}
		args, err = taskArgs(cfg, args)
		return cmd, args, err
	}

	if args, err = startArgs(args); err != nil {
		return modeTicket, nil, err
	}
	args, err = taskArgs(cfg, args)
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
