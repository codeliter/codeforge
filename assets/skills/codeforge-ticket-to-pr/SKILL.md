---
name: codeforge-ticket-to-pr
description: Take a ticket ID to a verified branch and PR text. Use when the task names a ticket or asks for a PR.
---

# Ticket to PR

1. Get the ticket ID. Branch name starts with it: `ABC-123-short-summary`, or the convention in `codeforge-persona`. Ask for the ID only if it is nowhere in the task or branch.
2. Read the ticket and the code it touches. Write a light plan if 3+ files.
3. Implement with `codeforge-code`. Commit in small steps with `codeforge-voice` messages.
4. Verify: build, lint, tests for every touched package.
5. Run `codeforge gate` once the work is committed-ready, not after each edit. Fix only Blocking findings, all in one batch, then rerun. The gate runs at most twice on a branch and then refuses: if a Blocking finding remains after the second run, stop and show it. Only the user can accept a finding (`codeforge gate accept`) or allow more runs (`codeforge gate reset`). Ignore Should findings. Pushing and opening the PR never wait for the gate.
6. Draft the PR title and body with `codeforge-voice`. Show it.
7. Stop. Opening the PR waits for an explicit go.
