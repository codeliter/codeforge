---
name: codeforge-voice
description: Default voice for commits, PRs, review replies and status updates. codeforge-persona overrides it. Use for any text that goes to people.
---

# Voice

Plain, specific, no ambiguity. Say what was wrong, what changes, and anything the reader must do. Nothing else.

## Rules
- Short sentences. Active voice. Present tense for what the change does ("Adds", "Uses", "Fixes").
- Concrete over vague: real numbers, metric names, run IDs, file or symbol names in backticks.
- One meaning per sentence. No hedging (might, probably, should help) unless the result is genuinely unknown, then say what is unknown.
- No filler, no hype, no emoji, no long bullet dumps, no auto-generated text.
- Name ordering constraints explicitly: "Merge after <repo>#<n> is applied."

## Commits
Imperative, sentence case, one line, no trailing period. Follow the repository's convention (for example Conventional Commits) when it has one.
- `Reuse the signing key set across requests`
- `Drop the unreachable error check in Key`

## PRs
Title: short and specific; include the ticket if the team uses one. Never the branch name.
Body (default, unless the repo has a PR template):
```
## Ticket
[TICKET]

## Description
<prose>
```
Description: 1-3 short paragraphs. Problem with evidence → what this changes → constraints or follow-ups.

Example (invented):
> The export job held 400 req/s but dropped about 12% of writes because the client opened a new connection per batch. This reuses one pooled connection per worker and adds `export_write_failures_total` so we can see drops directly. Merge after infra#120 raises the pool limit.

## Review replies
One line. `Done.` or `Fixed in <sha>: <what changed>.` Explain only when declining, in one or two sentences.

## Status updates
What is done, what is next, what is blocked and on whom. Three lines at most.
