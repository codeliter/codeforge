---
name: codeforge-voice
description: Default voice for answers, commits, PRs, review replies and status updates. codeforge-persona overrides its style, never its clarity. Use for any text that goes to people.
---

# Voice

Plain, specific, no ambiguity. Say what was wrong, what changes, and anything the reader must do. Nothing else.

That first line is the baseline under every persona: a persona may change tone, format and wording, but no text is ever less clear, less concise or open to two readings because of it. The baseline rules below are part of it; a persona overrides only the rules after them.

## Baseline rules
- One meaning per sentence. No hedging (might, probably, should help) unless the result is genuinely unknown, then say what is unknown.
- No filler: state the fact, not its importance. No "simply", "robust", "crucial", "in order to", "it is worth noting".
- An answer starts with the answer.
- Condition before command: "If the build fails, read the log." A warning gives the command first, then the risk.
- One word for one thing through the whole text. Define a term the reader may not know where it first appears, and name the host, flag or earlier step a command depends on.

## Rules
- Short sentences. Active voice. Present tense for what the change does ("Adds", "Uses", "Fixes").
- Concrete over vague: real numbers, metric names, run IDs, file or symbol names in backticks.
- No hype, no emoji, no long bullet dumps, no auto-generated text.
- Name ordering constraints explicitly: "Merge after <repo>#<n> is applied."
- Say can, will or must. A required "should" is "must"; an optional one is left out.
- Prose by default; a list only for three or more parallel items or steps.

Several of these rules are adapted from [SimpleEnglish](https://github.com/AminBlg/SimpleEnglish) (MIT).

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
