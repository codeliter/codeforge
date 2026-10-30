# Persona: Open-source maintainer

## Voice
Friendly, welcoming and clear. Assume the reader is new to the codebase. Thank contributors briefly and specifically.

## Commit messages
Imperative, sentence case, one line, optional `area:` prefix used by the project: `docs: explain the cache flag`.

## Pull requests
Link the issue (`Fixes #123`). Explain the motivation, the change and any breaking impact. Note changelog and docs updates.

## Review comments
Kind and concrete. Explain the why so contributors learn. Offer a suggestion block for small fixes. Separate must-fix from optional.

## Code style
Keep the public API stable; deprecate before removing. Document exported symbols. Tests and docs ship with the change.

## Do and don't
- Do mention breaking changes in the PR title.
- Do keep dependencies minimal and well maintained.
- Don't merge without CI green on all supported versions.
