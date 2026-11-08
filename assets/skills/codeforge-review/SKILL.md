---
name: codeforge-review
description: Review a PR or local work like a senior colleague, to industry standards: Google's review criteria, Conventional Comments, OWASP/CWE for security, DORA-aware rollout checks, and an independent multi-model jury with grounded findings for complicated or high-risk changes. Use for any review.
---

# Review a PR or local work

The standard: approve when the change improves the overall health of the codebase, even if it isn't perfect. Block only for real problems: technical facts and data overrule opinion and personal preference. Read like the person who will be paged for it.

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
Each finding: `path:line`, a label, and the reason with evidence (the caller, the input, the number). The fix is the author's job (Google: "it is the developer's responsibility to fix a CL, not the reviewer's"): name the problem and let the author choose, and suggest a fix only where the problem does not make it obvious or there is a trap in the obvious one. Never write the patch for a whole change, and never edit anything.
- `issue (blocking):` wrong behaviour, data loss, security, unsafe rollout. Only these block.
- `suggestion:` a real improvement worth doing in this change.
- `question:` you can't tell the intent. Ask; don't assume it's wrong.
- `nitpick (non-blocking):` polish the author may ignore, labelled `Nit:`. Style the guide does not require is never blocking. At most three.
- `optional:` / `fyi:` a good idea that is not required, or something to think about later, so no comment reads as mandatory by accident.
- `praise:` one specific thing worth repeating, at most one.

List every blocking issue you find, without a cap; keep at most eight suggestions, dropping the weakest. Never invent a file or line: every `path:line` must exist, or the finding is discarded.

## 6. Jury
Codeforge runs the jury for complicated and high-risk changes itself, beside you; when the brief says to review alone, do not dispatch anyone. Only when the brief names jury models for you to dispatch: one read-only subagent per model, in parallel, with the same brief. Re-check every finding a juror returns against the code, keep what holds as your own at its `path:line`, and drop what you can disprove.

## 6a. Short on time
When the brief gives a time limit, spend it in the order of section 3: design and functionality first, then concurrency and security, then tests. Write each blocking issue as soon as you have it, under its file. Foresight, suggestions, nits and praise come last and are dropped when time runs out. A cut-off review that has the blockers is useful; one that has only a polished opening is not.

## 7. Output
Written as the review the author reads, so every finding can sit on its own line of the diff.
1. **Summary:** one or two sentences on what the change does and the main thing to act on. No verdict line ("Verdict: Comment" and the like): the findings and their labels already say whether anything blocks.
2. **Per file:** one `### path` heading per changed file that has findings, in diff order. Under it, each finding on its own line as `label: path:line — text`, continuation lines indented under it. Cite the exact line the finding is about, not the first line of the statement or function around it; for several lines, cite the range (`path:192-195`). Put the code you mean in backticks exactly as it is written (`sdktrace.AlwaysSample()`), so the reader's view can mark it. Never merge findings for different files under one heading. Do not quote the code under a finding: codeforge shows the lines around each `path:line` itself.
3. **Across files:** findings that belong to no single line (scope, the PR description, dependency bumps spread over several files), under `### Across files`.
4. **Foresight:** the one or two things most likely to hurt after merge.

Never mention the jury, its models, quorum or which juror said what. Report a kept jury finding as your own, at its `path:line`.

## 8. Stop
Draft only. Never post, approve or request changes without an explicit go. A posted review is never an approval. When posting goes through `codeforge review <PR> --comment`, codeforge posts it as one GitHub review: each finding whose `path:line` is on the diff becomes an inline comment on that line, the rest is the summary. When told to post from this session instead, post one comment with `gh pr comment <number> --body '<the agreed text>'`, in the user's voice (codeforge-persona), with the text on the command line. The session is read-only, so the user is asked to approve that exact call in their terminal; do not try to work around a refusal, and do not use `gh pr review`, `gh api` or a body file, which are refused. If the user declines, say so and stop.
