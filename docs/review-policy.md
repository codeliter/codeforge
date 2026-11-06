# Review that finds everything in one pass

Status: design, revised after review by Opus 5.5 and Kimi K3. Replaces the round-cap proposal, which hid late findings instead of finding them.

Shipped so far: stop-early disabled at the gate (src/gate.go:108); no finding cap and an `invariant` field in the verdict format; follow-up rounds run the tests of every changed package and tell reviewers to check callers when a finding depends on them (dependents are not yet in the pack). Everything below is still to build.

## Goal

A gate run should surface every issue it is ever going to surface. A second run should only confirm fixes. The measure is **late-finding rate**: the share of all findings on a branch that first appear after round 1. Today it is close to 100% on risky changes (the guard change took ten rounds and each round found something new). Target: under 10%.

No review can prove a change has no bugs. What we can do is make the search exhaustive inside a bounded space, check it with real execution instead of opinion, and measure how often we still miss.

## Why the gate trickles findings today

Read from the code and from the ten failed runs:

1. **Stop-early cut the search (fixed).** `StopEarly: true` cancelled every other reviewer as soon as one grounded blocker appeared, so each round showed one reviewer's partial list. It is now `false` at src/gate.go:108. `JuryOptions.StopEarly` still exists, so nothing but that literal pins the choice.
2. **Reviewers were told to stop (fixed).** The skill capped findings ("at most eight", "top three") and the verdict prompt asked for short output. Both now ask for every blocking issue; the prompt still asks for short prose, qualified by "list EVERY blocking issue". The step budget still limits how much a reviewer can explore; standard and risky tiers now get 20 and 30 steps.
3. **Open-ended exploration, no contract.** Reviewers get a diff and no statement of purpose, invariants or threat model, so each invents its own attack and the space never closes. Every round found "one more spelling".
4. **Delta-only follow-ups.** Round 2+ reviews only files changed since the last round. Issues in unchanged callers, and issues the fix introduced elsewhere, are outside the search.
5. **Opinion, not evidence.** Reviewers are read-only and cannot run code. The only executable check is the repository's own tests. A claim "this input gets through" is argued, not demonstrated, so wrong findings cost a round and real ones are found one at a time.
6. **No memory of what a finding class covers.** Fixing `$(x)` leaves `\`x\``, variables and continuations for the next round.

## The pipeline

```
diff + task ──► 1 Contract ──► 2 Lens fan-out ──► 3 Probe ──► 4 Verify ──► 5 Saturate ──► 6 Report
                (author-OK)    (all, no cancel)   (execute)    (dedupe)    (until dry)     (one list)
```

### 1. Contract (spec first)
A cheap model drafts, and **the author confirms**, a short contract: purpose, invariants ("outside review nothing is blocked except learning writes"), declared threat model, explicit non-goals, and the public behaviour that must not change. The contract goes to every reviewer. A finding should name the invariant it violates or the behaviour it regresses. This closes the search space, which is what lets it be exhaustive.

Safeguards from the Opus and Kimi review:
- A contract drafted from the diff alone restates what the code does, bugs included. The author must confirm it.
- A concrete input with a wrong result stays Blocking even when no invariant names it (as `VerdictInstructions` says). "No invariant" never downgrades such a finding.
- The contract is hashed into `rubric` (src/gate.go:98) so cached verdicts expire with it, and stored per branch next to the round.

### 2. Lens fan-out, nothing cancelled
Reviewers differ by **lens**, not by file: correctness and edge cases, adversarial bypass, concurrency and lifetime, compatibility and rollout, test adequacy, and **removed behaviour** (what the old code did that the new code no longer does). Lenses run in parallel on different model families. Each lens keeps a checklist and is told to enumerate: no finding cap. Nothing is cancelled when another reviewer blocks; the gate waits for all of them and merges.

Required changes:
- The lens goes into `CacheKey(tree, model, rubric)` (jury.go), or two lenses on one model share one verdict.
- Every lens is required. `quorum` is `min(n,2)`, so four of six lenses could fail and the run would still pass.
- A larger step budget for risky tiers (triage.go). "Enumerate everything" under 20 steps produces more `ErrNoVerdict`, which gets only one retry.

### 3. Probe: find counterexamples by running code
The largest gain. For code that classifies input (parsers, guards, validators, routers, serialisers), build an **oracle** and run the real function against many inputs:
- **Differential test against ground truth.** For the guard, the oracle is the real shell: run each candidate command in a sandbox with a stub `codeforge` that records whether `observe` was invoked, then compare with what `Decide` allows. Generate inputs from a grammar of shell spellings (quotes, escapes, continuations, variables, substitution, wrappers, interpreters, pipes) plus mutation of every string the tests already use.
- **Property tests** for the rest: round trip, idempotence, monotonicity, "allowed set outside review is a subset of allowed set before".
- **Regression oracle:** run the old and new implementation on the same corpus and report every input whose result changed. This is how the false positives (`git commit -m "... $(date)"`, `/usr/bin/curl`) show up in round 1.

A probe finding is a concrete input with expected and actual output. Constraints:
- A grammar only covers the spellings someone put in it. The corpus grows from every confirmed finding.
- The oracle runs in a sandbox with no network and a throwaway filesystem, with the shell pinned (version, PATH, wrappers), so a drifting oracle cannot manufacture blockers.
- Counterexamples are emitted as a **patch** for the author to apply, never written during the run: `runGate` re-checks `TreeID` after the review (the `TreeID` re-checks after the Jury in src/gate.go), so writing files would void the pass.
- For the guard the first deliverable is a checked-in Go table or fuzz test: deterministic, free per run, not an agent.

### 4. Verify and dedupe
Findings from lenses and the probe are clustered by root cause (same file, symbol and failure class). `grounded` (jury.go:187) only checks that the file and line exist; this step checks truth. A small fast model with tool access tries to reproduce each cluster that came from a lens rather than a probe. Output per cluster: **confirmed** (reproduced), **refuted**, or **contradicts the contract or an earlier user decision**. Only confirmed clusters block. Refuted ones are shown as dropped, so the author sees them and the reviewer's miss rate is recorded. If the verifier fails or times out, the cluster stays Blocking and is marked unverified, never silently dropped.

### 5. Saturate inside the run
After the first merge, run another pass with the full finding list in the prompt: "what is still missing?" Each pass must bypass the verdict cache, or it replays the same verdict and "converges" instantly and falsely. Repeat until two consecutive passes add no new confirmed cluster, or the budget is spent. The author never sees the intermediate passes.

No coverage estimate. Capture-recapture needs independent samples; lenses are built to find different issue classes and share one diff and rubric, so low overlap is expected and the number would turn "we do not know" into false confidence. The report shows lens overlap descriptively. Model nondeterminism (same tree, different findings) is not fixed by saturation, so the late-finding metric is measured over N samples. All stages must fit the review clock (`review.HardLimit`, 180 seconds, with at most `review.AgentLimit`, 120 seconds, for any one reviewer or juror, src/review/clock.go): the panel ends at `WorkDeadline`, and the reserve after it is for the tree checks and recording the pass.

### 6. Report once
One list, grouped by invariant, each item with a reproducing input and the fix. Blocking = confirmed breach of the contract or a confirmed wrong result. Should and Notes are separate and never block. The report ends with the budget used.

### Fix round
The next run executes the stored probes first (seconds, deterministic) and the tests of **every** changed package, not only the files changed since the last round. Then it reviews the fix diff **plus callers and dependents of every changed symbol** (the genome has this). `DeltaNote` must not say unlisted files "were already reviewed": a round whose reviewers did not all finish did not review them. New blockers are labelled *introduced by the fix* (the finding lies on a line the fix diff touched, computed mechanically) or *missed earlier*, which feeds the metric.

## Why this converges

- The contract bounds what counts as a finding.
- Probes search the input space by execution, so the "one more spelling" game ends in the first pass.
- Verification removes the false positives that used to cost rounds.
- Saturation stops on "two passes with no new confirmed cluster", not on a round cap.
- Regression tests make every found issue stay fixed.

## Cost

Per risky change, roughly: contract (flash), 4 to 6 lenses (frontier, parallel), probe (local execution), verification (flash per cluster), 1 to 2 saturation passes. Expect 2 to 3 times today's cost for one run, against ten runs today. Simple changes keep the single-reviewer path: no probe, no saturation.

## Build order

Ordered by value over effort, after the Opus and Kimi review:

1. **Done:** stop-early removed (src/gate.go:108); no finding cap and an `invariant` field in the verdict format; follow-up rounds verify every changed package.
2. **Follow-up scope:** add callers and dependents of changed symbols to the follow-up pack (src/gate.go:65-71).
3. **Guard differential harness** as a checked-in Go test (section 3).
4. **Replay benchmark** from the ten rounds, so every later change can be scored. Needs the ledger first.
5. **Findings ledger** with stable ids. Today `Round` keeps only `Blocking` and is overwritten each run (rounds.go:22, 6h TTL) and `gateRecord` stores counts, so neither the late-finding rate nor the benchmark can be computed.
6. **Contract step**, author-confirmed, hashed into `rubric`.
7. **Lens reviewers** with lens in the cache key and every lens required.
8. **Verifier** that reproduces claims, extending `grounded`.
9. **Saturation loop**, cache-bypassing, within `review.HardLimit`.

## Speed and cost backlog

From Opus 5.5, ranked by value over effort. Items 1 to 3 are built; the rest are not.

1. **Built:** `Verify` runs concurrently with the Jury (src/gate.go). A jury error cancels it. Not built: a separate `go build` and `go vet` step, because `go test` already builds and runs the vet checks that matter.
2. **Built:** `Classify` returns at once when the file kinds already set `TierRisky`. Not built: building the pack while Classify runs.
3. **Built:** `Verify` no longer passes `-count=1`, so go's test cache serves unchanged packages. Set `CODEFORGE_VERIFY_NOCACHE=1` to force a fresh run. Go keys its cache on the files and variables a test opens, but a test that shells out to a child process can read things go does not track, so set the variable when a gate must not trust a replayed pass.
4. Pack the enclosing function or type of each changed hunk plus other signatures, whole files only under about 4 KB (src/review/pack.go:49-59). Files over 20 KB are dropped today.
5. Stable prompt prefix first, diff last, to raise the cached share above 84% (src/review/empryo.go:45, local.go:203-227).
6. A cheap no-verdict retry: resume the session or rerun with about 3 steps, and reserve the last 2 steps for the verdict in `stepBudget`.
7. Carry per-file findings across rounds, re-checked with `grounded()`, so every outstanding finding shows in one pass.
8. Record per-stage timings in `gateRecord` (src/gateusage.go) to measure each gain.

Do not: re-enable `StopEarly`; raise the time-boxed caps in `Tier.Timed` (8 steps, medium effort) without raising `HardLimit`, since every review must end within it; use the light model for small or standard code; skip Verify when reviewers pass or reviewers when tests fail; drop second opinions on risky changes; reuse whole-tree verdicts per file (reuse findings, never a pass).

## What this does not claim

It does not prove absence of bugs, and it does not catch what neither the lenses nor the oracle can see (wrong requirements, missing contract items). It makes misses rare, measurable and cheap to repair.
