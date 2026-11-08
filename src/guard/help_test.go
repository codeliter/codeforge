package guard

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// subcommands are the registered codeforge commands (src.RegisteredCommands), written out here because guard cannot import src.
var subcommands = []string{
	"approve", "allow", "chat", "completion", "doctor", "gate", "gh-shim", "help", "hook", "init", "learn", "models", "observe",
	"pre-push", "provider", "providers", "reinit", "repair", "respond", "review", "session-end", "setup", "start",
	"ticket", "trust", "version", "work",
}

func TestHelpIsAllowedOnEveryCodeforgeCommandInReviewMode(t *testing.T) {
	repo := gitRepo(t)
	for _, sub := range subcommands {
		for _, flag := range []string{"--help", "-h"} {
			for _, cmd := range []string{
				"codeforge " + flag,
				"codeforge " + sub + " " + flag,
				"codeforge " + sub + " set x y " + flag,
				"/opt/homebrew/bin/codeforge " + sub + " " + flag,
			} {
				if sub == "gh-shim" {
					continue // gh-shim forwards its arguments to gh, so it never answers help itself
				}
				d := Decide(shell(t, repo, cmd), true)
				require.True(t, d.Allow, cmd)
				require.False(t, d.Ask, cmd)
			}
		}
	}
	// A call that is not help still has the rules it always had.
	require.False(t, Decide(shell(t, repo, "codeforge trust --help && codeforge trust"), true).Allow, "a second command is judged on its own")
}

func TestGHShimNeverCountsAsHelp(t *testing.T) {
	repo := gitRepo(t)
	for _, cmd := range []string{"codeforge gh-shim --help", "codeforge gh-shim -h", "codeforge gh-shim pr create --help"} {
		require.False(t, Decide(shell(t, repo, cmd), true).Allow, cmd+": everything after gh-shim is gh's own argument list")
	}
	require.False(t, HelpRequested([]string{"gh-shim", "--help"}))
	require.False(t, HelpRequested([]string{"gh-shim", "pr", "view", "1", "-h"}))
	require.False(t, ReadOnlyCodeforge([]string{"gh-shim", "-h"}))
	require.True(t, ReadOnlyCodeforge([]string{"models", "recommend"}))
	require.False(t, ReadOnlyCodeforge([]string{"models", "set", "review.model", "x"}))
	require.False(t, ReadOnlyCodeforge(nil))
}

func TestExpansionCannotTurnHelpIntoAStateChangingCall(t *testing.T) {
	repo := gitRepo(t)
	for _, cmd := range []string{
		"separator=-- CODEFORGE_GUARD= CODEFORGE_LAUNCH= codeforge models $separator --help", // reaches codeforge as: models -- --help
		"codeforge models recommend $separator --help",
		"codeforge provider $separator -h",
		"codeforge providers $(echo --) --help",
		"codeforge models `echo --` --help",
		"codeforge $sub --help",
		"codeforge models ${a}recommend",
		"codeforge doctor $x --help",
		"OLDPWD=--; codeforge models ~- --help", // ~- is $OLDPWD
		"PWD=--; codeforge provider ~+ -h",
		"HOME=--; codeforge models recommend ~ --help",
		"codeforge models ?? --help", // a file named -- matches
		"codeforge init ~ --help",
		"/opt/homebrew/bin/codeforge models $(echo --) --help", // a path-qualified binary is judged the same way
		"s=--; /usr/local/bin/codeforge models $s -h",
	} {
		d := Decide(shell(t, repo, cmd), true)
		require.False(t, d.Allow, cmd)
		require.False(t, d.Ask, cmd)
	}
	require.False(t, ReadOnlyCodeforge([]string{"models", "$sep", "--help"}))
	require.False(t, expandsArgs(nil))
	require.True(t, expandsArgs([]string{"a", "$b"}))
	require.True(t, expandsArgs([]string{"`b`"}))
	require.True(t, expandsArgs([]string{"~-"}))
	require.True(t, expandsArgs([]string{"~"}))
	require.False(t, expandsArgs([]string{"models", "a~b"}), "a tilde inside a word is literal")
}

func TestReadOnlyCodeforgeFormsAreAllowedInReviewMode(t *testing.T) {
	repo := gitRepo(t)
	for _, cmd := range []string{
		"codeforge models", "codeforge models recommend", "codeforge provider", "codeforge providers show", "codeforge provider list",
		"codeforge doctor", "codeforge version", "codeforge --version", "codeforge help", "codeforge help --all",
	} {
		require.True(t, Decide(shell(t, repo, cmd), true).Allow, cmd)
	}
}

func TestStateChangingCodeforgeIsStillBlockedInReviewMode(t *testing.T) {
	repo := gitRepo(t)
	for _, cmd := range []string{
		"codeforge", "codeforge init", "codeforge init --repo", "codeforge trust", "codeforge repair", "codeforge reinit", "codeforge setup",
		"codeforge models set review.model x", "codeforge models apply", "codeforge models configure", "codeforge models plan",
		"codeforge provider set litellm", "codeforge provider configure", "codeforge provider clear",
		"codeforge learn correct x", "codeforge learn accept 1", "codeforge learn forget 1", "codeforge observe",
		"codeforge approve", "codeforge allow", "codeforge unknownthing", "codeforge zsh",
		"codeforge gate", "codeforge chat fix it", "codeforge review 3", "codeforge doctor --fix",
		"codeforge $o", "codeforge `echo init`", "codeforge models > out.txt", "codeforge doctor > out.txt",
		"codeforge -- --help", "codeforge init -- -h",
	} {
		d := Decide(shell(t, repo, cmd), true)
		require.False(t, d.Allow, cmd)
		require.False(t, d.Ask, cmd)
	}
}

func TestHelpRequestedStopsAtTheDoubleDash(t *testing.T) {
	require.True(t, HelpRequested([]string{"models", "--help"}))
	require.True(t, HelpRequested([]string{"-h"}))
	require.True(t, HelpRequested([]string{"learn", "correct", "x", "-h", "tail"}))
	require.False(t, HelpRequested([]string{"chat", "--", "--help"}))
	require.False(t, HelpRequested([]string{"chat", "fix the --help text"}), "a quoted prompt is one argument, not a flag")
	require.False(t, HelpRequested(nil))
}

func TestHelpOutsideReviewModeKeepsTheLearningRules(t *testing.T) {
	repo := gitRepo(t)
	require.True(t, Decide(shell(t, repo, "codeforge observe --help"), false).Allow, "help prints usage; it records nothing")
	require.False(t, Decide(shell(t, repo, "codeforge observe"), false).Allow)
	require.False(t, Decide(shell(t, repo, "codeforge learn correct x"), false).Allow)
}
