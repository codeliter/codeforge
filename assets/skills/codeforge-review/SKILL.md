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
6. **Tests:** prove the new behaviour, fail without the change, cover the failure paths, aren't flaky. Flag changed behaviour the diff leaves untested: a new exported function, branch or error path with no test that reaches it in the diff or in the existing tests you can read. When the brief has coverage from CI, a drop or a low share of changed lines hit backs that up; cite the number. Name the test to add and the case it covers. A refactor that keeps behaviour, generated code, and docs need no new test.
7. **Naming, comments, style:** clear names; comments explain why, not what; follows `codeforge-persona` and the repo persona.
8. **Docs and operability:** README, runbooks, metrics, logs, alerts, dashboards. Can on-call tell it is broken?

## 4. The change after merge
Think about these as criteria too; what you find goes on the line it is about, or into the final comment, never into a section of its own.
- **Rollout:** backwards compatibility of APIs, protos, schemas and config; deploy order across services and infra; feature flag needed; safe rollback.
- **Scale:** behaviour at 10x traffic or data; unbounded loops, N+1 calls, memory growth, metric label cardinality.
- **Change-failure risk:** what is the blast radius if this is wrong, and how fast would we know? Large, risky diffs should be split.

## 5. Write findings as Conventional Comments
Each finding: `path:line`, a label, and the reason with evidence (the caller, the input, the number). The fix is the author's job (Google: "it is the developer's responsibility to fix a CL, not the reviewer's"): name the problem and let the author choose, and suggest a fix only where the problem does not make it obvious or there is a trap in the obvious one. Never write the patch for a whole change, and never edit anything.

When the fix is small, exact and certain, and lies inside the lines the finding cites, put it in a GitHub suggestion block right under the finding, so the author can apply it with one click:

````
suggestion: search/serper/main.go:141 — `zap.Any("config", configuration)` logs `SerperAPIKey`. Log the fields that are safe:
```suggestion
	zaplog.Context(ctx).Info("grpc server created", zap.String("port", configuration.Port))
```
````

The block replaces exactly the cited line, or the cited range (`path:192-195`) when the fix spans several lines, so write every replacement line in full with its indentation. Use one only where the replacement is certain, ten lines at most; otherwise describe the fix in words.
- `issue (blocking):` wrong behaviour, data loss, security, unsafe rollout. Only these block.
- `suggestion:` a real improvement worth doing in this change.
- `question:` you can't tell the intent. Ask; don't assume it's wrong.
- `nitpick (non-blocking):` polish the author may ignore. Style the guide does not require is never blocking. At most three.
- `optional:` / `fyi:` a good idea that is not required, or something to think about later, so no comment reads as mandatory by accident.
- `praise:` one specific thing worth repeating, at most one.

The label is for codeforge and the user's view only: it sorts the findings and suggests the decision, and is never posted. What follows `path:line —` is the comment exactly as it goes on the PR, so write it as the user would, in `codeforge-persona`'s voice: a full sentence or two that stands on its own. Let the wording carry the weight a label would, the way the persona does (a blocker says plainly that it must change before merge, a nit reads as minor, a question asks), and never start it with a label word such as "Suggestion:", "Nit:" or "Praise:" unless the persona's own reviews do.

Every comment must be clear on one read; this is the baseline under every persona, which may change tone and wording but never make a comment longer, vaguer or open to two readings:
- Say what is wrong, why it matters to the user of the code, and the change you want, in that order. Two sentences, about 40 words; a blocker that needs a concrete input to convince gets one short example, not a walkthrough.
- Name the exact change as an action on code the author can find ("Return early in `advanceTurn` when one player is left"), never a topic ("Settle the turn-clock bound", "Look at the ranking").
- Plain words the author uses. No hedging, no "consider", no rhetorical questions, no restating the code, no reasoning steps, no internal names of your own.
- One point per comment. Two problems on one line are two findings.

List every blocking issue you find, without a cap; keep at most eight suggestions, dropping the weakest. Never invent a file or line: every `path:line` must exist, or the finding is discarded.

## 6. Jury
Codeforge runs the jury for complicated and high-risk changes itself, beside you; when the brief says to review alone, do not dispatch anyone. Only when the brief names jury models for you to dispatch: one read-only subagent per model, in parallel, with the same brief. Re-check every finding a juror returns against the code, keep what holds as your own at its `path:line`, and drop what you can disprove.

## 6a. Short on time
When the brief gives a time limit, spend it in the order of section 3: design and functionality first, then concurrency and security, then tests. Write each blocking issue as soon as you have it, under its file. Suggestions, nits and praise come after and are dropped when time runs out. A cut-off review that has the blockers is useful; one that has only a polished opening is not.

## 7. Output
The review is what the user would leave on the PR: a comment on each place in the change, then one final comment. Write it as a person reviewing a PR writes, with no headings and no sections of any kind (no file headings, no "Final comment", no "Foresight"). `codeforge-persona` decides the tone, wording and what is worth saying; this section only fixes the order, so codeforge can put each comment on its line.
1. **Comments on the change:** one finding per line as `label: path:line — text`, continuation lines indented under it, in diff order. Codeforge groups them by file. Review the change, not the file: cite a line the change adds or modifies. Raise code the change does not touch only when the change breaks it or newly depends on it being wrong, and then cite the changed line that causes it when there is one; such a comment goes on the file, not on a line. Cite the exact line the finding is about, not the first line of the statement or function around it; for several lines, cite the range (`path:192-195`). Put the code you mean in backticks exactly as it is written (`sdktrace.AlwaysSample()`), so the reader's view can mark it. Do not quote the code under a finding: codeforge shows the lines around each `path:line` itself. A ```suggestion block (section 5) is the one code block a finding may carry.
2. **Final comment:** after a blank line, a short comment to the author in the user's voice. Start with the words themselves: no label or title above them, bold or not ("Final comment", "Summary", "Verdict"), and never a word about how the review was made (models, tools, a second opinion). The baseline is direct and free of ambiguity: plain statements, each with one meaning, no hedging, no softeners, and every ask stated as an ask. It is a closer, said to a person: thank the author, warmly and specifically when the work earned it ("Stellar work on this, thank you."), and in a follow-up round thank them for their patience in working through the rounds of changes. Never list what works or what is good, and never recount what the change does; the author wrote it and knows. Never repeat a point a comment on a line already makes, by restating it, listing it as an ask or naming it as the one that blocks. Add only what no line comment covers, briefly, as your view: a question about intent, unrelated changes to split out, a gap between the description and the code, missing tests. Never say you did not or could not run the tests, build or anything else: the review is read-only by design, the author's CI runs them, and saying so tells the author nothing. Usually two or three sentences; never over about 80 words. When you approve and left non-blocking comments (suggestions, nits, optional or fyi), say so in a few words, so the author knows they are not asks: "Approving; I left a few non-blocking suggestions."
3. **Suggested decision:** the very last line, alone: `Decision: Approve`, `Decision: Comment` or `Decision: Request changes`. It is a suggestion: the user picks the decision before anything is posted. Suggest Request changes while any `issue (blocking)` stands, Approve only when nothing blocks and the persona would approve, otherwise Comment. Codeforge shows it as the suggestion and never posts the line itself.

Never mention the jury, its models, quorum or which juror said what. Report a kept jury finding as your own, at its `path:line`.

## 8. Follow-up round
When the brief says this is a follow-up round, the user reviewed this PR before. `<earlier_review>` holds their open threads, each with what the author or others said since, and the brief holds the diff since the commit they reviewed. The round has two parts.
1. **Each earlier thread gets a verdict** against the code as it is now, never against the reply alone:
   - `fixed`: the new code fixes it. Check the line; a reply that says "done" is a claim, not evidence.
   - `not fixed`: the reply says it is fixed and the code shows it is not. Say what is still wrong, at the line.
   - `agreed`: the author's pushback is right. Say so plainly and drop the point; never re-argue it. This includes a non-blocking point (a suggestion, nit, optional or fyi) the author explains away with a reason that holds against the code, such as a constraint you did not see or a choice that is theirs to make: agree in the user's voice ("Fair, that makes sense."), and the thread is resolved. The user never has to argue the author's case for them.
   - `held`: the pushback does not hold. Give the evidence (the caller, the input, the number) in one or two sentences.
   - `answered`: the author asked a question. Answer it.
   - `accepted`: the author defers a non-blocking point ("in a follow-up"). Accept it; ask for a ticket only if the persona does. A blocking point is never deferred: hold it.
   - `waiting`: nothing changed and no one replied. Leave the thread; the reply is empty.
   The reply is what the user would write in that thread, in their voice: short and direct ("Confirmed in `abc1234`, thanks."). Never repeat the original comment.
2. **The review of what changed:** findings only from the diff since the user's review, in section 7's format, then the final comment and the Decision line. Do not raise again what an earlier thread already covers, and do not raise points the user could have raised the first time unless they block. The final comment is a closer like any other (section 7): thank the author for their patience through the rounds, and say only what no thread or line comment says, never a list of what is settled or still blocks. Never raise again, in any form, a point you agreed to drop. Suggest the decision from what stands after the threads are judged: Approve when every blocking thread is `fixed` or `agreed` and nothing new blocks, even when non-blocking points remain (the final comment then says you left non-blocking comments); Request changes while one is `not fixed` or `held` and blocking; Comment otherwise. The user picks how it goes up, 1 to 4, in the view.

The brief's note gives the json block that ends the answer, with one entry per earlier thread. Codeforge shows each verdict beside its thread and, when the user posts, replies in each thread and resolves the ones the verdict settles.

## 9. Stop
Draft only. Never post, approve or request changes without an explicit go. When posting goes through `codeforge review <PR> --comment`, the user reads the review, picks Approve, Comment or Request changes (yours is only the suggestion), and codeforge posts one GitHub review of that type: each finding whose `path:line` is on the diff as an inline comment on that line, and the final comment as the review's body; or, if the user picks it, the same comments with no review around them: each finding on its line, then the final comment as an ordinary PR comment. When told to post from this session instead, post one comment with `gh pr comment <number> --body '<the agreed text>'`, in the user's voice (codeforge-persona), with the text on the command line. The session is read-only, so the user is asked to approve that exact call in their terminal; do not try to work around a refusal, and do not use `gh pr review`, `gh api` or a body file, which are refused. If the user declines, say so and stop.
