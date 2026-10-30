package src

import (
	"fmt"
	"io"
)

const helpText = `codeforge: a coding agent that works like you, on the Empryo TUI.

Usage:
  codeforge [mode] [task] [empryo flags]

Modes:
  ticket [--ticket ID] ["summary"]   Take a ticket to a verified change and draft PR
  chat ["question"]                  Questions, explanations and small edits
  review [PR number or URL]          Read-only review of a PR, or of your branch when no PR is given
  respond [PR] [--watch]             Address new review comments on your PR: judge, fix, push back, draft replies
  (none)                             Ask which mode; a question on the command line goes to chat

Setup:
  init [--yes]                       Set up everything: Empryo (installed only if missing), sign-in, checks,
                                     models, your persona and the push guard in this repository
  init --repo [--persona NAME]       Add a repository persona (.codeforge/persona.md)
  init --persona NAME | init REPO... Use a starter persona, or learn from other repositories
  models [recommend|apply|set ROLE MODEL...]
                                     See, apply or set the models for each role
  trust                              Approve this repository's persona

Review and approvals:
  gate                               Review your branch with the jury; a pass unlocks push and PR
  allow RULE                         Approve one blocked action for 10 minutes (you, not the agent)

Help:
  help, -h, --help                   Show this help
  version                            Show the codeforge version

Rules for allow: force-push, no-verify, merge, pr-comment, infra, whole-tree, delete-branch, push, open-pr

Config: ~/.codeforge/config.json   Docs: https://github.com/codeliter/codeforge
`

func printHelp(w io.Writer) {
	fmt.Fprint(w, helpText)
}

func isHelp(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "--help"
}
