package src

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/modelcatalog"
	"github.com/codeliter/codeforge/src/review"

	"github.com/codeliter/codeforge/src/modelconfig"
)

// runMode runs a mode or a command that launches Empryo: init, gate, respond and the four modes.
func runMode(ctx context.Context, p prepared, args []string) error {
	cfg, err := config.Load(p.home, p.project, modelconfig.TrustApprover())
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
	bin, err := findEmpryo(p.empryoDir)
	if err != nil {
		return err
	}
	plan, args, catalog, err := planModels(ctx, p, bin, cfg, args)
	if err != nil {
		return err
	}
	cfg = plan.Config
	if base, err := config.ResolvePlan(p.globalCfg, catalog); err == nil {
		if err := syncAssets(p.empryoDir, base.Config); err != nil {
			return err
		}
	}
	if err := syncProjectAgents(p.workspace, cfg); err != nil {
		return fmt.Errorf("installing project profiles: %w", err)
	}

	// Removes the retired pre-push hook from repositories that still register it.
	if err := ensureEnforced(ctx, p.self); err != nil {
		return err
	}

	cmd, rest := first(args), args
	if cmd != "" {
		rest = args[1:]
	}
	switch cmd {
	case "init":
		return runInit(ctx, p.home, bin, cfg, rest)
	case "persona":
		return runPersonaUpdate(ctx, p.home, bin, cfg, rest[1:])
	case "gate":
		return runGate(ctx, p.home, bin, cfg, os.Stdout)
	case "respond":
		return respondLaunch(ctx, p.home, bin, cfg, rest)
	case modeTicket, "start", modeChat, modeWork, modeReview:
		staleNotice(ctx, p.home, os.Stderr)
	default:
		cmd, rest = "", args
	}
	return launchMode(ctx, p.home, bin, cfg, picked, cmd, rest)
}

// planModels names the models of this launch from the saved model catalog. A saved catalog can be stale after a
// sign-in, a sign-out or a models apply, so a plan it rejects is tried once more against a fresh one.
func planModels(ctx context.Context, p prepared, bin string, cfg config.Config, args []string) (plan config.ModelPlan, resolved, catalog []string, err error) {
	cached := false
	if config.NeedsCatalog(cfg) || argumentsNeedCatalog(args) {
		if catalog, cached, err = modelcatalog.Launch(ctx, p.home, bin); err != nil {
			return plan, nil, nil, err
		}
	}
	plan, resolved, err = resolveModels(ctx, bin, p.workspace, cfg, catalog, args)
	if err != nil && cached {
		fmt.Fprintln(os.Stderr, "codeforge: refreshing the saved model list...")
		if catalog, err = modelcatalog.Refresh(ctx, p.home, bin); err != nil {
			return plan, nil, nil, err
		}
		plan, resolved, err = resolveModels(ctx, bin, p.workspace, cfg, catalog, args)
	}
	return plan, resolved, catalog, err
}

// resolveModels names the models for cfg and the model arguments, and checks the native fallback settings against them.
func resolveModels(ctx context.Context, bin, workspace string, cfg config.Config, catalog, args []string) (config.ModelPlan, []string, error) {
	plan, err := config.ResolvePlan(cfg, catalog)
	if err != nil {
		return plan, nil, err
	}
	resolved, overrides, err := resolveModelArguments(args, cfg.Providers, catalog)
	if err != nil {
		return plan, nil, err
	}
	plan.Routes = append(plan.Routes, overrides...)
	if len(plan.FallbackChains()) > 0 {
		fmt.Fprintln(os.Stderr, "codeforge: checking existing native fallback settings (read-only)...")
		if err := modelconfig.VerifyPlan(ctx, bin, workspace, plan); err != nil {
			return plan, nil, err
		}
	}
	return plan, resolved, nil
}

// respondLaunch answers review feedback on a pull request in a ticket session.
func respondLaunch(ctx context.Context, home, bin string, cfg config.Config, args []string) error {
	if _, resume, err := sessionID(args); err != nil {
		return err
	} else if resume {
		return errSessionArgs
	}
	acknowledgeTask(os.Stderr, "respond", args)
	args, fb, done, err := respondArgs(ctx, args)
	if err != nil || done {
		return err
	}
	if cfg.Model != "" && !hasFlag(args, "--model") {
		args = append([]string{"--model", cfg.Model}, args...)
	}
	// The first pass's answers are shown against this feedback before anything is pushed or posted.
	return launch(review.WithRespond(ctx, fb), home, bin, modeTicket, args)
}

// launchMode settles the mode (restored from a session, picked earlier, or asked for now), prepares its task and
// launches Empryo. A new review gets its clock here.
func launchMode(ctx context.Context, home, bin string, cfg config.Config, picked, cmd string, rest []string) error {
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
	if resumed && (hasFlag(rest, "--comment") || hasFlag(rest, "--comment=yes")) {
		return errCommentResume
	}
	if resumed {
		// What a scheduled job did while nobody had the session open is said before it opens.
		if id, _, err := sessionID(rest); err == nil {
			if wd, err := os.Getwd(); err == nil {
				if full, err := resolveResumeID(wd, id); err == nil {
					// A review launch is read-only: it starts nothing of another session's.
					if cmd != modeReview {
						reviveJobs(ctx, jobStore(home), full)
					}
					printJobNotice(os.Stderr, jobStore(home), wd, full, time.Now(), cmd == modeReview)
				}
			}
		}
	}
	if !resumed {
		acknowledgeTask(os.Stderr, cmd, rest)
	}
	// The review's clock starts here, after the setup above (repair, model catalog, enforcement, mode choice), so setup
	// time is never charged to it. It covers everything the review itself does: collecting the change, classifying it,
	// the jurors and the engine.
	engine := ctx
	if cmd == modeReview && !resumed && !review.HasDeadline(ctx) {
		var cancel context.CancelFunc
		ctx, engine, cancel = startReviewClock(ctx)
		defer cancel()
	}
	mode, args, err := modeArgs(ctx, bin, cfg, cmd, rest)
	if err != nil {
		return err
	}
	if mode == modeTicket && !resumed && !hasFlag(args, "--headless") {
		// Ticket work is reviewed by other models before the TUI opens; blocking findings go back to the agent.
		return launchWith(engine, home, bin, mode, args, ticketAfterTask(home, bin, cfg))
	}
	return launch(engine, home, bin, mode, args)
}

// startReviewClock gives a new review its hard limit and the state shared across it: the jurors running beside the
// reviewer and the request to post the result. work is cancelled at the limit and is for the review's own steps; engine
// knows the limit but is never cancelled, so that the supervised engine can still save its session when time is up.
func startReviewClock(ctx context.Context) (work, engine context.Context, cancel context.CancelFunc) {
	end := time.Now().Add(review.HardLimit)
	pipe, post := &review.Pipeline{}, &review.PostRequest{}
	engine = review.WithDisplay(review.WithPost(review.WithPipeline(review.WithDeadline(ctx, end), pipe), post), &review.Display{})
	engine = review.WithFollowUp(engine, &review.FollowUp{})
	work, cancel = context.WithDeadline(engine, end)
	return work, engine, cancel
}

// isModeWord reports whether the first argument already names the mode or command Run dispatches on, so no pick is due.
func isModeWord(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "init", "gate", "respond", "persona", modeTicket, "start", modeChat, modeWork, modeReview:
		return true
	}
	return false
}
