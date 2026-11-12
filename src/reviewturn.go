package src

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/review"
)

// reviewTurn runs one more headless turn in the review's saved session with prompt, the way the review itself ran
// (argv), and returns what the reviewer wrote.
func reviewTurn(argv, env []string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, prompt string) (string, error) {
		id := flagValue(argv, "--session-id")
		if id == "" {
			id = flagValue(argv, "--session")
		}
		if id == "" || len(argv) == 0 {
			return "", errSessionMissing
		}
		// A review runs inside the read-only sandbox (sandbox-exec -p PROFILE empryo ...): the turn keeps that wrapper
		// and the engine behind it, and changes only the engine's own arguments.
		wrapper, engine := sandboxPrefix(argv)
		if len(engine) == 0 {
			return "", errSessionMissing
		}
		bin := slices.Concat(wrapper, engine[:1])
		args := withoutPrompt(dropFlag(dropFlag(engine[1:], "--session-id"), "--session"))
		reply, err := resumedTurn(ctx, slices.Concat(bin, []string{"--session", id}, args, []string{prompt}), env)
		var exit *EngineExitError
		if err == nil || ctx.Err() != nil || !errors.As(err, &exit) || exit.Code != exitUsage {
			return reply, err // done, stopped by the user, or failed for a reason a plainer call does not change
		}
		// A flag the review ran with may not suit a resumed turn: try once more with only what a turn needs, the
		// review's profile, mode and model, so a question still gets its answer.
		minimal := slices.Concat(bin, []string{"--session", id, "--headless", "--json"})
		for _, f := range []string{"--profile", "--mode", "--model"} {
			if v := flagValue(argv, f); v != "" {
				minimal = append(minimal, f, v)
			}
		}
		if reply, again := resumedTurn(ctx, append(minimal, prompt), env); again == nil {
			return reply, nil
		}
		return "", err
	}
}

// sandboxPrefix splits argv into the sandbox wrapper it runs under, if any (sandbox-exec -p PROFILE), and the command
// the wrapper runs.
func sandboxPrefix(argv []string) (wrapper, command []string) {
	const wrapperArgs = 3 // sandbox-exec, -p, the profile
	if len(argv) >= wrapperArgs && argv[0] == sandboxExec && argv[1] == "-p" {
		return argv[:wrapperArgs], argv[wrapperArgs:]
	}
	return nil, argv
}

// exitUsage is the status an engine exits with when its command line is wrong (EX_USAGE): the one failure a turn with
// fewer flags can get past.
const exitUsage = 64

// resumedTurn runs one turn of the engine, argv in full, and returns what it wrote. On a failure the error ends with
// the engine's own last line of complaint, so the user sees why, not only an exit status.
func resumedTurn(ctx context.Context, argv, env []string) (string, error) {
	// The review's own clock has run out by now; the turn gets a limit of its own from when the user sent it. Ctrl+C
	// still stops it: with a limit the engine runs in its own process group and runEngineTo forwards the interrupt.
	tctx := context.WithoutCancel(ctx)
	var out, chatter bytes.Buffer
	if err := runEngineTo(withEngineStderr(tctx, &chatter), argv, env, time.Now().Add(revisionLimit), &out); err != nil {
		if why := lastLine(chatter.String()); why != "" {
			return "", fmt.Errorf("asking the reviewer: %w (%s)", err, why)
		}
		return "", fmt.Errorf("asking the reviewer: %w", err)
	}
	reply, err := review.EngineOutput(out.Bytes())
	if err != nil {
		return "", fmt.Errorf("reading the reviewer's answer: %w", err)
	}
	return reply, nil
}

// lastLine is the last line of s that holds anything, cut to a length that fits a message.
func lastLine(s string) string {
	const maxWhy = 200
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return clip(strings.TrimSpace(lines[len(lines)-1]), maxWhy)
}

// withoutPrompt drops the review's prompt, its last positional argument, and keeps everything else as it was: every
// flag, and every other positional too, so a flag codeforge does not know takes the same value as in the review and
// the turn runs under the same settings.
func withoutPrompt(args []string) []string {
	prompt := -1
	for i := 0; i < len(args); i++ {
		key, _, equal := strings.Cut(args[i], "=")
		switch {
		case flagsWithValue[key] && !equal:
			i++ // its value, never the prompt
		case !strings.HasPrefix(args[i], "-"):
			prompt = i
		}
	}
	if prompt < 0 {
		return slices.Clone(args)
	}
	return slices.Concat(args[:prompt], args[prompt+1:])
}
