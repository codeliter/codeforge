# Review that finds everything in one pass

Status: design. Replaces the round-cap proposal, which hid late findings instead of finding them.

## Goal

A gate run should surface every issue it is ever going to surface. A second run should only confirm fixes. The measure is **late-finding rate**: the share of all findings on a branch that first appear after round 1. Today it is close to 100% on risky changes (the guard change took ten rounds and each round found something new). Target: under 10%.

No review can prove a change has no bugs. What we can do is make the search exhaustive inside a bounded space, check it with real execution instead of opinion, and measure how often we still miss.

## Why the gate trickles findings today

Read from the code and from the ten failed runs:

1. **Stop-early cuts the search.** `StopEarly: true` (src/gate.go:102) cancels every other reviewer as soon as one grounded blocker appears. Each round therefore shows one reviewer's partial list; the second reviewer, with a different blind spot, never finishes. Ten runs printed "stopped early" for one of the two models.
2. **Reviewers are told to stop.** The skill keeps "at most eight findings", the verdict prompt says "keep the prose short", and each reviewer has a step budget. A reviewer reports its top few issues, not all of them.
3. **Open-ended exploration, no contract.** Reviewers get a diff and no statement of purpose, invariants or threat model, so each invents its own attack and the space never closes. Every round found "one more spelling".
4. **Delta-only follow-ups.** Round 2+ reviews only files changed since the last round. Issues in unchanged callers, and issues the fix introduced elsewhere, are outside the search.
5. **Opinion, not evidence.** Reviewers are read-only and cannot run code. The only executable check is the repository's own tests. A claim "this input gets through" is argued, not demonstrated, so wrong findings cost a round and real ones are found one at a time.
6. **No memory of what a finding class covers.** Fixing `$(x)` leaves `\`x\``, variables and continuations for the next round.

## The pipeline

```
diff + task ──► 1 Contract ──► 2 Lens fan-out ──► 3 Probe ──► 4 Verify ──► 5 Saturate ──► 6 Report
                  (spec)        (all, no cancel)  (execute)    (dedupe)    (until dry)     (one list)
```

### 1. Contract (spec first)
A cheap model reads the task, commit messages, docs and diff and writes a short contract: purpose, invariants ("outside review nothing is blocked except learning writes"), declared threat model, explicit non-goals, and the public behaviour that must not change. The contract goes to every reviewer. A finding must name the invariant it violates or the behaviour it regresses; anything else is a Should. This closes the search space, which is what lets it be exhaustive.

### 2. Lens fan-out, nothing cancelled
Reviewers differ by **lens**, not by file: correctness and edge cases, adversarial bypass, concurrency and lifetime, compatibility and rollout, test adequacy, and **removed behaviour** (what the old code did that the new code no longer does). Lenses run in parallel on different model families. Each lens keeps a checklist and is told to enumerate: no finding cap, no "top three". Nothing is cancelled when another reviewer blocks; the gate waits for all of them and merges. Cost rises per round, but rounds fall from ten to one or two.

### 3. Probe: find counterexamples by running code
The largest gain. For code that classifies input (parsers, guards, validators, routers, serialisers), a probe agent builds an **oracle** and runs the real function against many inputs:
- **Differential test against ground truth.** For the guard, the oracle is the real shell: run each candidate command in a sandbox with a stub `codeforge` that records whether `observe` was invoked, then compare with what `Decide` allows. Generate inputs from a grammar of shell spellings (quotes, escapes, continuations, variables, substitution, wrappers, interpreters, pipes) plus mutation of every string the tests already use. A few thousand cases take seconds and find every spelling in one go, which ten LLM rounds did not.
- **Property tests** for the rest: round trip, idempotence, monotonicity, "allowed set outside review is a subset of allowed set before".
- **Regression oracle:** run the old and new implementation on the same corpus and report every input whose result changed. This is how the false positives (`git commit -m "... $(date)"`, `/usr/bin/curl`) show up in round 1.

A probe finding is a concrete input with expected and actual output. It needs no further argument. Every reproduced counterexample is written to a test file in the change, so it can never regress.

### 4. Verify and dedupe
Findings from lenses and the probe are clustered by root cause (same file, symbol and failure class). A small fast model with tool access tries to reproduce each cluster that came from a lens rather than a probe. Output per cluster: **confirmed** (reproduced), **refuted**, or **contradicts the contract or an earlier user decision**. Only confirmed clusters block. Refuted ones are dropped and shown as dropped, so the author sees them and the reviewer's miss rate is recorded.

### 5. Saturate inside the run
After the first merge, run another pass with the full finding list in the prompt: "what is still missing?" Repeat until two consecutive passes add no new confirmed cluster, or the budget is spent. The author never sees the intermediate passes. Report a coverage estimate from the overlap between independent reviewers (capture-recapture: if two lenses found 9 and 10 issues and 8 are shared, the estimate of the total is about 11). A low estimate with high overlap means the search is done; low overlap means it is not, and the report says so instead of declaring a pass.

### 6. Report once
One list, grouped by invariant, each item with a reproducing input, the fix, and a regression test already written. Blocking = confirmed breach of the contract. Should and Notes are separate and never block. The report ends with the coverage estimate and the budget used.

### Fix round
The next run executes the stored probes and regression tests first (seconds, deterministic). Then it reviews the fix diff **plus callers and dependents of every changed symbol** (the genome has this), not just changed files. New blockers are labelled *introduced by the fix* or *missed earlier*, which feeds the metric.

## Why this converges

- The contract bounds what counts as a finding.
- Probes search the input space by execution, so the "one more spelling" game ends in the first pass.
- Verification removes the false positives that used to cost rounds.
- Saturation and the overlap estimate tell us when to stop, instead of a round cap deciding it.
- Regression tests make every found issue stay fixed.

## Cost

Per risky change, roughly: contract (flash), 4 to 6 lenses (frontier, parallel), probe (one agent plus local execution), verification (flash per cluster), 1 to 2 saturation passes. Expect 2 to 3 times today's cost for one run, against ten runs today. Simple changes keep the single-reviewer path: no probe, no saturation.

## Build order

1. **Remove stop-early** in the gate and merge all verdicts. One line, immediate effect on completeness.
2. **Contract step** and the invariant requirement in the verdict format (`invariant` field on each blocker).
3. **Lens reviewers** in place of identical reviewers (src/review/jury.go takes a lens per reviewer).
4. **Findings ledger** with stable ids, so rounds report fixed, still open or new, and "introduced by fix" versus "missed earlier".
5. **Probe agent** with a sandboxed execute tool and the differential harness. Start with the guard as the first oracle (real shell plus stub binary).
6. **Verifier** and clustering.
7. **Saturation loop** and overlap estimate.
8. **Eval set:** replay this branch's ten rounds as a benchmark. Score = findings caught in round 1 divided by findings caught by the end. Run it on every change to prompts, lenses or models.

## What this does not claim

It does not prove absence of bugs, and it does not catch what neither the lenses nor the oracle can see (wrong requirements, missing contract items). It makes misses rare, measurable and cheap to repair.
