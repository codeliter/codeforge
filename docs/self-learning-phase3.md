# Self-learning, phase 3: learning from review findings

Status: designed, not started. Pick this up after phases 1 and 2 (v0.7 habits, v0.8 rules from corrections).

## Where things stand

| Phase | What it learns | Source of truth | Shipped |
|---|---|---|---|
| 1 | Verification commands that keep passing | Empryo `PostToolUse` results | v0.7 |
| 2 | Rules from corrections the user repeats | Empryo `UserPromptSubmit` prompts | v0.8.1 |
| 3 | What reviews should flag or stop flagging | The user's decisions on review findings | this document |

Code to read first: `src/learn_habits.go` (observer, habits), `src/learn_rules.go` and `src/learn_embed.go` (rules,
grouping, sentence identity), `src/learn_prompt.go` (prompt observer), `src/store/` (observations, rules, vectors,
migrations 001 to 005), `src/gate.go` and `src/review/` (the gate and its verdicts), `docs/v2-prototype.md` (phase 2
design history).

## Goal

`codeforge gate` and `codeforge review` produce findings. The user acts on them: fixes some, ignores or overrides
others. Learn from those decisions so that reviews flag what this repository's owner cares about and stop repeating
what they have already declined. Reviewers are other models; their findings are never evidence, only the user's
reaction to them is.

Non-goals: changing the review rubric itself, the jury size, models, personas, the guard or trust. Any lesson that
touches those stays a proposal the user approves (see "What is never learned").

## Signals (only what the user does, never what a model says)

1. **Dismissed finding.** The user marks a finding as not wanted: `codeforge learn dismiss-finding F3` (new). Records
   the finding's file pattern and kind of complaint.
2. **Accepted finding.** The finding was fixed: after a failing gate, the same finding (same file, normalised wording)
   is absent from the next gate on a tree that changed in or near its file. Inferred, so it counts as weak evidence
   and never produces a rule on its own.
3. **Repeated finding.** The same kind of finding appears in two or more gates for this repository and the user fixed
   it each time. A candidate for a "check this earlier" habit, shown to the work-mode prompt.

Provenance rules carried over from phase 2: a lesson needs two different sessions or gate runs, a person must have
issued the decision (terminal launch, `CODEFORGE_HUMAN`), and the agent cannot run the commands that record or approve
anything (extend `guard.readsLearning` only for read commands).

## Storage

Migration `006_findings.sql` (the next free number; check `src/store/migrations/` first):

- `findings(id, repository, dir, gate_run, file, line, kind, text, created_at)`: every finding a gate printed. Kept so
  decisions can reference an ID the user saw. The text is the reviewer's output, so it is data, never prompt text.
- `finding_decisions(finding_id, decision, decided_at)` with `decision IN ('dismissed','fixed')`.
- Reuse `rules` and `dismissals` for what becomes a lesson; add a `kind` column to `rules` (`convention` today,
  `review` new) so the two never mix in one prompt block.

Build on `Store.RecordWithVector` and `correction_vec` if grouping similar findings matters; `embed` is local and
deterministic, so no provider call is needed.

## Behaviour

- **Number findings.** `report()` in `src/gate.go` prints each finding with a stable ID (`F<gate-run>.<n>`), so
  `learn dismiss-finding` has something to name. Keep the current output otherwise.
- **Suppress what was declined.** Before printing, drop a finding whose normalised sentence and file pattern match a
  dismissal, and say how many were hidden (`2 findings hidden by your earlier decisions; codeforge learn findings`).
  Hiding must never apply to security or data-loss findings: a rubric tag decides, not the user's history.
- **Feed the reviewer.** Add a short "this repository's owner has declined these kinds of finding" block to the brief
  (`review.PromptLocal`), at most five lines, as guidance that never overrides the rubric.
- **Habits for work mode.** A repeated, fixed finding kind becomes a line in the work prompt ("reviews here often flag
  X; check it before finishing"), shown with an undo command like rules are.

## Safety rules (apply the lessons of phases 1 and 2)

- Compare the user's own sentences for any automatic decision. Fuzzy similarity only groups items for the user to read.
  Phase 2 spent many gate rounds on this: opposite or reordered wording ("enable X" against "disable X", "lint after
  tests" against the reverse, `<` against `>`) cannot be told apart by word overlap or embeddings.
- Decide inside one transaction (check dismissal, check limit, insert), as `AddRuleUnlessDismissed` does.
- Forgetting is permanent and also declines the source items.
- Scope everything to the directory it came from; treat unknown scope (`?`) as no scope.
- Re-validate stored text when it is loaded into a prompt (`checkRuleText`).
- Fail open: a broken vector table, hook or database never blocks a gate or a launch.

## What is never learned

Anything touching the guard, trust, permissions, secrets, providers, models or personas. Those stay user-only.
A finding about security, data loss or an unsafe rollout can be marked "seen" but never hidden.

## Open questions for the owner

1. Should dismissing a finding be per file, per directory or per kind of complaint? Per kind is most useful and most
   dangerous; start with per file pattern plus normalised sentence.
2. How should "fixed" be detected without a review of every diff? The proposal above is weak evidence only.
3. Does the gate output become noisy with IDs? A `--plain` flag may be needed for scripts.

## Test plan

Write first, then the code. Use the existing end-to-end harness in `src/learn_e2e_test.go` (real binary, fake
engine) with a fake gate reviewer that returns fixed JSON verdicts.

1. Findings are numbered and stored; the same gate on the same tree does not store them twice.
2. A dismissed finding is hidden on the next gate, counted in the summary, and shown by `learn findings`.
3. A security-tagged finding is never hidden, even if a lookalike was dismissed.
4. Opposite findings ("enable X" and "disable X") never share a dismissal.
5. The agent cannot run `dismiss-finding`; the guard test list in `src/guard/learn_test.go` covers it.
6. Forgetting a lesson declines its sources; reverting evidence cannot bring it back.
7. A lesson applies only in the directory it was learned in; unknown scope applies nowhere.
8. Migration 005 to 006 keeps every existing row.

## Process notes (hard-won)

- Run `codeforge gate` once per milestone, not after every edit. Reviewers keep finding reworded variants of the same
  hole; fix the class (exact-sentence identity, whole-group compatibility) instead of each example.
- A release is `git push origin main`, then `git tag`, then `git push origin <tag>`. Pushing the tag alone builds the
  old commit (this happened with v0.8.0).
- Dispatched sub-agents here are read-only; implement in the main session.
- Empryo's hook payloads: `tool_response` has keys `output` and `success` (no exit code); `UserPromptSubmit` carries
  `prompt`. Check `~/.codeforge/observe-shape.log` before assuming a field.
