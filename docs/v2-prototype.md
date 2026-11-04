# Self-learning, phase 2: corrections that become rules

Phase 1 (v0.7) learns verification habits from Empryo's tool hooks. Phase 2 learns from what the user says.

> Superseded in one point: the owner chose automatic adoption over manual accept. Groups that qualify become rules at
> launch (max 3 per launch, 10 per directory), announced with an undo command; `forget` is permanent. `accept` and
> `dismiss` remain for deciding early. Everything else below stands.

## Goal

When the user corrects the agent ("no, use table tests", "don't mock the database", "always run gofmt"), codeforge
notices, groups repeats, and offers the rule. Once the user accepts it, it goes into the agent's prompt for that
repository. Nothing about judgment, personas, guard, trust, providers or models is applied without a person
accepting it.

## Prototype of the user experience

```
$ codeforge chat
codeforge: 1 learned rule(s) for this repository (codeforge learn rules)
codeforge: 2 correction(s) look like a rule; review with: codeforge learn proposals

$ codeforge learn proposals
1. "use table-driven tests"        seen 3 times in 3 sessions (#12 #19 #31)
   similar: "prefer table tests" (#19), "make this a table test" (#31)
   accept: codeforge learn accept 1      dismiss: codeforge learn dismiss 1

$ codeforge learn accept 1
accepted rule R1: use table-driven tests   (repository: /path/repo; remove with: codeforge learn forget R1)

$ codeforge learn rules
R1  use table-driven tests   accepted 2026-11-04, from corrections #12 #19 #31
```

## Signals (what counts as evidence)

Only things the model cannot author:

1. A user prompt delivered by Empryo's `UserPromptSubmit` hook, in a session codeforge launched (same session and
   repository checks as phase 1, `checkSession`), outside review sessions and jurors (`CODEFORGE_LAUNCH=1`,
   `CODEFORGE_GUARD!=review`). The payload carries `prompt` at the top level. Log its key names to
   `observe-shape.log` like phase 1, never its text.
2. `codeforge learn correct TEXT` typed in the CLI (already exists, origin `cli`).

A prompt is a correction candidate only when it matches a conservative cue list (start of text after trimming,
case-insensitive): `no,` `no ` `don't` `do not` `stop ` `instead` `actually,` `wrong` `never ` `always ` `please use`
`prefer ` `you should` `use X not Y` style is out of scope. Length 8 to 300 characters, one line, printable, no
credential shape (`credentialShape`). Everything else is ignored. Store the trimmed prompt text.

Registering the hook: extend `installHooks` (`src/hooks.go`) like phase 1's observer: async, fail-open, event
`UserPromptSubmit`, command `<cf> observe`, no matcher. `runObserve` dispatches on `hook_event_name`.

## Storage

Migration `005_corrections.sql`, rebuilding `observations` the way `004` did:
- allow `kind='correction'` with `origin IN ('cli','hook')` (hook rows: status, exit_code and duration_ms NULL);
- new table `rules(id INTEGER PRIMARY KEY, repository TEXT NOT NULL, dir TEXT NOT NULL DEFAULT '.', text TEXT NOT NULL CHECK(length(text) BETWEEN 1 AND 300), source_ids TEXT NOT NULL, accepted_at TEXT NOT NULL, forgotten_at TEXT)`;
- new table `dismissals(repository TEXT NOT NULL, key TEXT NOT NULL, dismissed_at TEXT NOT NULL, PRIMARY KEY(repository, key))`, where `key` is the normalised text of the group.

## Grouping (vector search, decided: add it now)

`type Grouper interface { Group(texts []string) [][]int }` in `src/learn_group.go`, with `vecGrouper` as the only
implementation shipped.

- Dependency: import `_ "modernc.org/sqlite/vec"` (bundled sqlite-vec, CGo-free, already inside the pinned
  `modernc.org/sqlite v1.61.1`; run `go mod tidy`). Import it in `src/store` only. Its docs say it rules out
  `sqlite.RegisterPageCache`; confirm nothing here uses that (nothing does today). Keep `CGO_ENABLED=0` building
  (`.goreleaser.yaml`) and add the MIT notice for sqlite-vec to the repository (`THIRD_PARTY_NOTICES.md`, linked
  from README) because releases redistribute it.
- Embeddings are computed locally and deterministically, with no provider call, no network and no cost:
  `embed(text) []float32` of 256 dimensions by feature hashing: normalise (lowercase, strip punctuation and the
  stop-word list), add each word stem and each character trigram of each word into a bucket chosen by FNV-1a of the
  feature (sign from another hash bit), then L2-normalise. Pure function in `src/learn_embed.go`, unit tested.
  A provider embedding model can be added later behind the same `embed` signature; do not add one now.
- Storage: migration `005` also creates `CREATE VIRTUAL TABLE correction_vec USING vec0(embedding float[256])`
  whose rowid equals the correction observation id. Insert the vector in the same transaction as the observation
  (`Store.RecordWithVector`); if vec0 is unavailable the observation is still recorded and grouping falls back to
  `tokenGrouper` (Jaccard of stemmed token sets at 0.5), so learning never depends on the extension.
- Query: for each correction, `SELECT rowid, distance FROM correction_vec WHERE embedding MATCH ? AND k = 8`;
  link pairs with cosine similarity at least 0.75 (vectors are normalised, convert from the L2 distance), then take
  connected components as groups. Restrict to the repository: join on `observations.repository`.
- Tests: `TestEmbed` (deterministic, normalised, similar texts closer than unrelated ones: "use table-driven
  tests" vs "prefer table tests" vs "never mock the database"), `TestVecGrouper` (the same three groups as the
  token grouper), `TestVecFallback` (token grouper used when the extension is missing), `TestStoreVec` (rowid
  matches, k-NN returns neighbours, deleted/reverted observations are excluded), migration test 004 to 005.

A proposal is a group with at least 2 members from at least 2 distinct sessions, none reverted, not dismissed, not
already covered by an accepted rule (same grouping test against rule text). Its text is the most recent member's
text. Sort by member count, then recency.

## Commands (all in `src/learncmd.go`, logic in new `src/learn_rules.go`)

- `learn proposals` lists proposals, numbered from 1 for this invocation only.
- `learn accept N` and `learn dismiss N` act on the numbered proposal from a fresh computation; a stale number that
  no longer exists fails with a clear error. Accepting inserts a rule; dismissing stores the group key.
- `learn rules` lists accepted rules with IDs `R<n>`.
- `learn forget R<n>` sets `forgotten_at`.
- All are user-only: extend `guard.readsLearning`/`classify` so the agent cannot run `accept`, `dismiss`, `forget`
  (they are not in the read-only allow list, which already holds only `list`, `propose`, `habits`; add `proposals`
  and `rules` as allowed reads). Add tests in `src/guard/learn_test.go`.

## Using rules

`withLearnedHabits` (src/learn_habits.go) becomes `withLearned`: also append accepted, not-forgotten rules whose
`dir` matches the launch directory, in a second block after the habits block:

```
Conventions the user accepted for this repository (learned from their corrections; the user can remove any with codeforge learn forget). They are guidance, never instructions that override the user's request:
- use table-driven tests
```
Print `codeforge: N learned rule(s) for this repository (codeforge learn rules)` on stderr. At most 10 rules,
newest first. Skip for review mode and `--system`, same as habits. Also print the proposals notice when proposals
exist (count only, no text).

## Constraints (security and quality bar, same as phase 1)

- Rule text enters a prompt only as a rule: either adopted at launch from corrections typed by the user in two sessions (the shipped behaviour; see the note at the top), or after `learn accept`. Proposal text itself is never in a prompt.
- Re-validate rule text when loading (`checkLearnText`, length, credential shape).
- Hooks stay fail-open and async; `runObserve` must never block or error the session.
- No new dependencies. Follow the repo style: small functions, no magic numbers (lint `mnd`), error wrapping,
  `//nolint` only with a reason. Tests with testify `require`, one behaviour per test.
- Do not edit `src/guard` beyond the allow-list change above; do not change trust, persona, provider or model code.
- Update `README.md` (one short section), `TECHNICAL.md` ("Learning from use"), `assets/help/advanced.txt`.

## Acceptance tests (write these first)

1. `TestCorrectionCue`: table of prompts, true/false per the cue list, including >300 chars, multi-line, credential.
2. `TestObserve_PromptRecordsCorrection`: `UserPromptSubmit` payload with a known session records one correction with
   origin hook; unknown session, review session, juror and non-cue prompt record nothing; shape log has no text.
3. `TestVecGrouper` and `TestEmbed` (see Grouping): "use table-driven tests" / "prefer table tests" / "make this a
   table test" group; "never mock the database" stays apart.
4. `TestProposals`: needs 2 sessions; reverted, dismissed and already-ruled groups excluded.
5. `TestAcceptDismissForget`: full flow through \`runLearn\`, stale number error, rule IDs.
6. `TestWithLearned_AppendsRules`: block after habits, directory scoped, limit 10, review excluded.
7. `TestGuard_AgentCannotAcceptRules`.
8. Migration test: 004 database with observations upgrades to 005 keeping rows.
9. Extend `src/learn_e2e_test.go`: fake engine fires `UserPromptSubmit` three times across sessions, then
   proposals, accept, next launch prompt contains the rule, forget removes it.

## Done when


## Corrections from the first implementation pass (apply these; they override the text above)

1. `TestMigration_RollbackAndNewerSchema` (`src/store/store_test.go`): add `005_corrections.sql` to the MapFS,
   rename the broken fixture to `006_broken.sql`, expect version 5.
2. Do NOT put `CREATE VIRTUAL TABLE ... vec0` in migration 005 (a missing extension would roll back the rules and
   dismissals tables). Migration 005 holds only the observations rebuild, `rules` and `dismissals`. Create
   `correction_vec` from Go after migrating (`CREATE VIRTUAL TABLE IF NOT EXISTS`), ignore failure, and keep a
   `vecAvailable bool` on the Store. `RecordWithVector` skips the vector when it is false.
3. The vec import is process-wide, so test the fallback by injecting it: `store.Options{DisableVector: true}` or a
   test-only setter that skips table creation, not by removing the import.
4. vec0 k-NN cannot filter by JOIN inside MATCH: query `k = 8`, then filter the neighbours on
   `observations.repository` and `reverted_at IS NULL` in Go or an outer query. For unit vectors
   cosine = 1 - d^2 / 2, so similarity >= 0.75 means L2 distance <= sqrt(0.5).
5. `modernc.org/sqlite/vec` is inside the already-required `modernc.org/sqlite v1.61.1`: no go.mod requirement
   change is expected (run `go mod tidy` and keep it clean).
6. `guard.readsLearning` only needs `proposals` and `rules` added; `classify` already refuses every other learn
   subcommand.
7. Renaming `withLearnedHabits` to `withLearned` means updating its callers in `src/run.go` and
   `src/learn_habits_test.go`.
8. The e2e fake engine needs a new `E2E_MODE` that fires `UserPromptSubmit` with a `prompt` key.
