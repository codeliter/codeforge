package src

import (
	"fmt"
	"io"
	"slices"

	"github.com/codeliter/codeforge/assets"
)

// helpText is the full reference; the default `help` shows only the everyday commands. The text lives in
// assets/help and is embedded at build time.
var (
	helpText  = assets.HelpCommon + assets.HelpAdvanced + assets.HelpFooter
	helpShort = assets.HelpCommon + assets.HelpMore + assets.HelpFooter
)

func printHelp(w io.Writer, all bool) {
	if all {
		fmt.Fprint(w, helpText)
		return
	}
	fmt.Fprint(w, helpShort)
}

func isHelp(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "--help"
}

// WantsHelp reports whether args ask for help: a standalone -h or --help anywhere before a "--". It is the one rule the
// review-mode guard (guard.HelpRequested) and Run both use, so what the guard lets through as help is exactly what Run
// answers with usage text, before it reads or writes anything. A prompt that needs those words is quoted as one argument.
//
// gh-shim is the exception: every argument after it is gh's own (`gh --help` arrives as `gh-shim --help`), so it never
// asks codeforge for help. It is a retired passthrough that the next repair removes.
func WantsHelp(args []string) bool {
	if len(args) > 0 && args[0] == "gh-shim" {
		return false
	}
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "-h", "--help":
			return true
		}
	}
	return false
}

// wantsAllHelp reports whether the arguments after the help word ask for every command.
func wantsAllHelp(args []string) bool {
	return slices.Contains(args, "--all")
}
