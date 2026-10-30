package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/codeliter/codeforge/src"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if len(os.Args) > 1 {
		var code int
		switch os.Args[1] {
		case "hook":
			code = src.Hook(ctx)
		case "pre-push":
			code = src.PrePush(ctx)
		case "gh-shim":
			code = src.GHShim(ctx, os.Args[2:])
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
		fmt.Fprintln(os.Stderr, "codeforge:", err)
		os.Exit(1)
	}
}
