package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/codeliter/codeforge/src"
)

func engineExitCode(err error) (int, bool) {
	var exit *src.EngineExitError
	if errors.As(err, &exit) {
		return exit.Code, true
	}
	return 0, false
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// The internal hook commands read stdin and may write state; with -h or --help they print usage instead, like every
	// other command, so help is safe to run anywhere.
	if len(os.Args) > 1 && !src.WantsHelp(os.Args[1:]) {
		var code int
		switch os.Args[1] {
		case "hook":
			code = src.Hook(ctx)
		case "pre-push":
			code = src.PrePush(ctx)
		case "gh-shim":
			code = src.GHShim(ctx, os.Args[2:])
		case "session-end":
			code = src.SessionEnd()
		case "observe":
			code = src.Observe(ctx)
		default:
			code = -1
		}
		if code >= 0 {
			stop()
			os.Exit(code)
		}
	}

	err := src.Run(ctx, os.Args[1:])
	stop()

	if err != nil {
		if code, ok := engineExitCode(err); ok {
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, "codeforge:", err)
		os.Exit(1)
	}
}
