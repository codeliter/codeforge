---
name: codeforge-review
description: Review a PR or local work like a senior colleague, to industry standards: Google's review criteria, Conventional Comments, OWASP/CWE for security, DORA-aware rollout checks, and an independent multi-model jury with grounded findings. Use for any review.
---

# Review a PR or local work

The standard: approve when the change improves the overall health of the codebase, even if it isn't perfect. Block only for real problems. Read like the person who will be paged for it.

## 1. Intent before the diff
- Read the title, description, ticket and commits (for local work: branch and commits). Write one sentence: what this solves and how.
- Check it solves that problem and nothing else. Unclear intent is your first comment.
- Read earlier reviews. Don't repeat a raised point; say whether it was addressed.

## 2. The system, not the hunks
- Open the full changed files (PR head, or the working tree for local work).
- Find callers and dependents of every changed exported symbol with the Genome. A safe change in one file can break a caller in another.
- Compare with the existing pattern in the package. Divergence needs a reason.

## 3. Criteria, in this order
Google's engineering practices order, from most to least important. Skip what does not apply.
1. **Design:** right place, right abstraction, fits the system, not over-engineered for a future that isn't here.
2. **Functionality:** does what the author intended, and what users need. Edge cases, empty and huge input, error paths, partial failure, retries, timeouts.
3. **Concurrency:** shared state, goroutine and context lifetime, ordering, deadlocks, races.
4. **Security:** map each finding to OWASP Top 10 or a CWE id (e.g. CWE-89 SQL injection, CWE-200 information exposure, CWE-862 missing authorization). Input validation at boundaries, authz on new paths, secrets and personal data in logs, least privilege.
5. **Complexity:** can a reader understand it quickly? Simpler alternative?
6. **Tests:** prove the new behaviour, fail without the change, cover the failure paths, aren't flaky.
7. **Naming, comments, style:** clear names; comments explain why, not what; follows `codeforge-persona` and the repo persona.
8. **Docs and operability:** README, runbooks, metrics, logs, alerts, dashboards. Can on-call tell it is broken?

## 4. Foresight: the change after merge
- **Rollout:** backwards compatibility of APIs, protos, schemas and config; deploy order across services and infra; feature flag needed; safe rollback.
- **Scale:** behaviour at 10x traffic or data; unbounded loops, N+1 calls, memory growth, metric label cardinality.
- **Change-failure risk:** what is the blast radius if this is wrong, and how fast would we know? Large, risky diffs should be split.

## 5. Write findings as Conventional Comments
Each finding: `path:line`, a label, the reason with evidence (the caller, the input, the number), and a concrete fix (a short suggestion block when useful).
- `issue (blocking):` wrong behaviour, data loss, security, unsafe rollout. Only these block.
- `suggestion:` a real improvement worth doing in this change.
- `question:` you can't tell the intent. Ask; don't assume it's wrong.
- `nitpick (non-blocking):` style only. At most three.
- `praise:` one specific thing worth repeating, at most one.

Keep at most eight findings; drop the weakest. Never invent a file or line: every `path:line` must exist, or the finding is discarded.

## 6. Jury
Always get independent second opinions. The brief names the models; dispatch one read-only subagent per model, in parallel, with the same brief, and ask each for its top three risks with `path:line`. If none are named, use a model from a different family than yours. Re-check every finding they return against the code; keep what holds, drop what you can disprove, and say which model raised each kept finding.

## 7. Output
1. **Verdict:** Approve, Comment or Request changes, with the main reason, in two or three sentences.
2. **Findings:** grouped by file, as Conventional Comments.
3. **Foresight:** the one or two things most likely to hurt after merge.

## 8. Stop
Draft only. Never post, approve or request changes without an explicit go. When told to post, use `gh pr review` with the agreed verdict and inline comments via `gh api`.
