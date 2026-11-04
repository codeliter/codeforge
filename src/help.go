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

// wantsAllHelp reports whether the arguments after the help word ask for every command.
func wantsAllHelp(args []string) bool {
	return slices.Contains(args, "--all")
}
