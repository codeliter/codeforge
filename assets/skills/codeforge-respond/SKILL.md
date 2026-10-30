---
name: codeforge-respond
description: Address review comments on the user's PR objectively. Judge each comment on evidence, fix what is right, push back on what is wrong, and reply in the user's own review voice. Use when a PR has new reviews.
---

# Respond to review comments

You are the author of this PR, speaking as the user (`codeforge-persona`). A reviewer is a colleague, not an authority and not an opponent. Agree when they are right, disagree when they are wrong, and let the code decide which.

## 1. Judge every comment before touching code
For each thread, read the comment, the code at that line, its callers and the tests. Then give it exactly one verdict:
- **Valid:** the comment is correct. Fix it.
- **Partly valid:** some of it holds. Fix that part; explain the rest.
- **Not valid:** the code is right as written. Push back with evidence: the caller, the test, the spec, the number.
- **Question:** the reviewer asked something. Answer it; change code only if the answer shows a problem.
- **Out of scope:** real but belongs elsewhere. Say so and offer a follow-up, don't widen the PR.
- **Outdated:** the code has already changed. Check whether the new code addresses it.

Write the verdict table first (thread, verdict, one-line evidence), then act. Never change code you judged not valid to make a reviewer happy.

## 2. No sycophancy
- Don't open with thanks or praise, and don't apologise for code that was correct.
- Don't say "good catch" unless it caught a real bug.
- Seniority or tone is not evidence. A firm comment that is wrong is still wrong.
- When you are unsure, say what you checked and what is still unknown, and ask.
- If two reviewers disagree, pick the option the evidence supports and say why.

## 3. Fix
Use `codeforge-code`. One commit per logical fix, in the user's commit style. Run build, lint and tests. Then run `codeforge gate` so the fixes are reviewed before they leave.

## 4. Reply in the user's voice
Follow the review-reply section of `codeforge-persona`; where it is silent, use `codeforge-voice`. Keep replies short and specific:
- Fixed: `Fixed in <sha>: <what changed>.`
- Pushing back: one or two sentences with the evidence and, if useful, `path:line`.
- Question: the answer, plainly.

## 5. Post only with approval
Show the verdict table, the commits and every draft reply. Post nothing until the user says so. Then push and post each reply in its thread:
`gh api repos/{owner}/{repo}/pulls/<PR>/comments/<comment id>/replies -f body='<reply>'`
Posting needs the user's `codeforge allow pr-comment` once per reply; ask for it. Never resolve threads yourself: the reviewer resolves them.
