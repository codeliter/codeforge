---
name: codeforge-code
description: Default code bar: simple, safe, secure. codeforge-persona overrides style details. Load before writing or reviewing code.
---

# Code bar

## Simple
- Minimum change for the task. Match the package's existing patterns, naming and test style.
- Small functions. Early returns and guard clauses; invert conditions instead of nesting.
- No new dependency without a stated reason.
- Delete dead code; no leftovers, no commented-out code.

## Comments
Only for a non-obvious constraint or reason, one line. Never restate the name or narrate the change.
Good: `// MULTI/EXEC so INCR never lands without its EXPIRE`

## Go
- Constructors: `New(opts Options) (*T, error)`.
- Sentinel errors (`var ErrX = errors.New(...)`), checked with `errors.Is/As`; map domain errors to transport codes only at the boundary.
- Scoped if initializer: `if err := f(); err != nil { return fmt.Errorf("doing x: %w", err) }`.
- Pass `context.Context` first; respect cancellation and timeouts.
- Accept small interfaces at the point of use.
- Typed int enums with `iota`.
- Tests: table tests, `require`, `t.Context()`, helpers with `t.Helper()`, names like `TestAllow_ErrorFailsOpen`.

## TS / JS
- Named consts with units: `const CACHE_MAX_AGE_MS = 600_000`.
- `async`/`await`, run independent calls with `Promise.all`, time out external calls.
- Tests next to the code (vitest), mock at module boundaries.

## Safe and secure
- Validate input at system boundaries; trust internal guarantees.
- No secrets, tokens or personal data in code, logs, metrics labels or test fixtures.
- Parameterized queries only. Escape output. No shell string building from input.
- Least privilege for IAM, DB users and cache ACLs; add only the permission needed and say why.
- Decide fail-open vs fail-closed explicitly and state it in a one-line comment.
- Bound everything: timeouts, retries with limits, sizes, label cardinality.

## Done means
Build, lint and tests pass locally, and the diff contains only what the task needs.
