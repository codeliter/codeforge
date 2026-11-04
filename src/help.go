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
  work ["task"]                      Conversational tasks through implementation and verification; no ticket or PR flow
  review [PR number or URL]          Read-only review of a PR, or of your branch when no PR is given
  respond [PR] [--watch]             Address new review comments on your PR: judge, fix, push back, draft replies
  (none)                             Ask which mode; a question on the command line goes to chat

Setup:
  init [--yes]                       Set up Empryo, sign-in and checks; learn a local persona in this Git repo
  init --repo [--persona [NAME]]     Learn a local persona, or use a starter (.codeforge/persona.md)
  init --persona [NAME]             Pick a described starter persona; name it to skip the menu
  init --global [REPO...]           Set your global persona instead (~/.codeforge/persona.md)
  repair, reinit                     Rebuild managed hooks, profiles and skills; keep personas and sessions
  repair --legacy-hook PATH          Replace a named old Codeforge hook; repeat for several paths
  models configure [--project|--global]
                                     Choose providers and role/helper models interactively; save config.yaml
  models plan                        Show resolved IDs and verify existing native fallback settings
  models apply [--recommended] [--project|--global]
                                      Set Empryo's default model and backups after one confirmation; --recommended first saves recommendations
  models [recommend|set ROLE MODEL...]
                                     Show recommendations or set global Codeforge role choices
  trust                              Approve this repository's persona
  trust --models                     Approve this repository's model configuration by content hash

Review:
  gate                               Review your branch: one reviewer for simple changes, the jury for
                                     complicated or high-risk ones. Voluntary; nothing waits for it

Learning (evidence only; nothing is applied to prompts, personas or guards):
  learn correct [--session ID] TEXT  Record a correction you typed into this CLI
  learn outcome [--session ID] -- COMMAND [ARGS]
                                     Run a check (no shell, 10 min limit) and record passed, failed or incomplete
  learn list [--all]                 Show this repository's observations; --all includes reverted ones
  learn propose                      Show corrections recorded 2+ times, with their evidence
  learn revert ID                    Hide an observation from proposals; the record is kept
                                     The agent cannot run correct, outcome or revert; there is no approve yet

Resume:
  --resume ID, --session ID, -s ID    Restore the original mode without new-task preparation
  review --resume ID                Explicit mode required once for historical sessions
                                     Missing or wrong-workspace sessions fail; review stays read-only
  Storage: ~/.codeforge/sessions/sessions.db (private SQLite WAL)

Help:
  help, -h, --help                   Show this help
  version                            Show the codeforge version

Git is never blocked outside review mode. Pushing, opening PRs and merges follow your explicit requests; no mandatory gate.
Reviewer-generated PR comments require approval of the exact target and draft.

Config: ~/.codeforge/config.yaml + project .codeforge/config.yaml (legacy global JSON still readable)
Docs: https://github.com/codeliter/codeforge
`

func printHelp(w io.Writer) {
	fmt.Fprint(w, helpText)
}

func isHelp(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "--help"
}
