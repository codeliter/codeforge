# Review policy: how codeforge reviews should work

Status: proposal. Not implemented. Written after a gate run on the review-mode guard change took ten rounds (about 3 to 5 minutes and roughly 0.1 to 0.6 USD per round) and never passed.

## What went wrong

- **Unbounded rounds.** The gate blocks on any "Blocking" finding and starts again. Every fix opened a new edge case, so the loop had no natural end.
- **Perfect as the bar.** The guard is a heuristic that parses shell text. A reviewer can always invent one more spelling (`o=observe; codeforge $o`, a line continuation, an apostrophe inside double quotes). Each was real, and none changed whether the change was well implemented.
- **No severity ceiling.** Findings that were hardening ideas were filed as Blocking, next to real defects.
- **Hard-fail on one reviewer.** One model returning "fail" stopped the other model, so a single opinion decided every round.
- **Two reviewers read the whole diff.** Both pay for the full context, and both can drift into the same narrow rabbit hole.
- **False positives reached the author.** Several blockers were wrong or contradicted an earlier requirement (a commit message containing `$(date)` flagged as a forgery). Nothing filtered them before they cost a round.

## Principles

1. A review answers one question: is this change well implemented? It does not ask whether it is perfect or whether an attacker could never get around it.
2. Blocking means the change breaks its stated purpose, breaks existing behavior, or opens a hole in the threat model the change declares. Everything else is a Should or a Note.
3. Every blocker must carry a reproduction: an input and the wrong output. A finding that cannot be reproduced is a Should at most.
4. Rounds are capped. After the cap the gate stops and a human decides. It never loops on its own.
5. Each round may only judge what changed since the last round, plus the earlier blockers. New concerns found in unchanged code are carried as notes, not blockers.

## Proposed pipeline

1. **Split.** Group the changed files into chunks by area (for example: parsing and rules, hook wiring, tests, docs). A small cheap model does the grouping from the diff stat. A change under a size threshold stays as one chunk.
2. **Review per chunk.** One reviewer per chunk sees only its chunk plus a short summary of the whole change. Each reviewer owns its chunk. This cuts context cost and keeps a reviewer from digging into code another one owns.
3. **Verify.** A small, fast model (a flash-class model) takes each Blocking finding and tries to reproduce it: run the repro, read the cited line, check it against the task statement and the user's earlier decisions. It returns confirmed, unconfirmed or contradicts-requirement. Only confirmed findings can block. The others are demoted to notes. This is the false-positive filter, and it is the cheapest place to remove wasted rounds.
4. **Gate.** Blocking findings that survived verification decide the result. One reviewer's pass does not override a confirmed blocker, and one unconfirmed fail does not block.
5. **Fix and re-review.** The author fixes confirmed blockers. The next round reviews only the new diff and the earlier blockers.

## Caps and stop rules

- Maximum 3 rounds by default. A third failing round stops the gate and prints the open blockers and the notes. Release is then a human decision.
- Cost budget per gate run, printed up front. The gate stops when the budget is spent.
- A finding that repeats the same class of issue for the third time (a new spelling of the same bypass) is reported as a design limit, not as another blocker. The fix is to document the limit or change the design, not to patch each spelling.
- Findings that contradict a decision recorded in the task or in a steering message are dropped by the verifier.

## Severity guide for reviewers

- **Blocking:** wrong behavior in the stated scope, a regression, a security hole inside the declared threat model, data loss. Needs a reproduction.
- **Should:** worth fixing, but the change is well implemented without it: edge cases outside the threat model, clarity, missing tests for non-critical paths.
- **Note:** style, naming, follow-ups, ideas.

## Reviewer prompt requirements

- State the purpose of the change and its threat model in two or three lines. Say what is out of scope.
- State what is a heuristic by design, and what a reviewer must not report as Blocking (for example "a shell parser cannot be complete; report only bypasses that a model could produce by accident or by ordinary prompting").
- Ask for a reproduction with every blocker.
- On follow-up rounds, show the previous blockers and ask: fixed or not. Do not invite new exploration.

## Open questions

- Chunk boundaries: by directory, by dependency graph, or chosen by the splitter model. Start with directory plus the genome's co-change data.
- Which model verifies. It must be cheap and fast, and it must be able to run a command. A flash-class model with tool access is enough for reproduction.
- Whether the verifier should also see the author's response, so it can drop findings the author has already rebutted.
