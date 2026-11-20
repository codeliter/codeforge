---
name: codeforge-cli
description: Everything the codeforge CLI can do and which commands you may run yourself. Use before telling the user something cannot be done, when a command must wait, when the user corrects you, or when they ask what codeforge offers.
---

# What codeforge can do

`codeforge help --all` prints every command and flag and is allowed in every mode: read it rather than guess a flag. Run codeforge commands in the shell, from the repository.

## You run these
- **Review of local work:** `codeforge gate` (one reviewer for simple changes, the jury for risky ones; stops after 3 failed runs on a branch). `codeforge gate accepted` lists accepted findings.
- **Review of a PR:** `codeforge review <PR> --headless` from a work or chat session. `codeforge review accepted <PR>` lists accepted findings.
- **Review feedback on the user's PR:** `codeforge respond --print` prints the open threads to judge, fix or push back on.
- **Commands that must wait:** `codeforge job add [--name N] [--at TIME] [--until 'CMD']... [--while 'CMD']... [--hold DUR] -- COMMAND`. Never sleep or poll in the shell. `--at` is the earliest start (`02:00`, `+90m`); every `--until` must exit 0 first, for `--hold` if given; a failing `--while` cancels the job. Then `codeforge job wait ID` as a background command, which ends with the result. Also `job list`, `job show ID`, `job log ID`, `job cancel ID`. A job outlives the session and posts its result back into it. A job may only run what you could run directly.
- **Learning, read:** `codeforge learn habits` (checks that keep passing here), `learn rules` (accepted rules), `learn proposals` and `learn propose` (repeated corrections), `learn list [--all]` (every observation).
- **Learning, write:** when the user states a standing rule or corrects how you work, record it in their words: `codeforge learn correct 'Always use table-driven tests'`. One line, no secrets, never `--session`. Run it as a command of its own, with nothing else on the line, so codeforge can count it for this session. It is counted, not applied: the same sentence recorded in two different sessions becomes a rule at the next launch (announced, undone with `codeforge learn forget RN`); anything less waits as a proposal for the user's `codeforge learn accept PN`. Record only what the user said, in their exact words, once per session; never repeat one to raise its count. `codeforge learn outcome -- COMMAND [ARGS]` runs a check without a shell (10 minute limit) and records passed, failed or incomplete.
- **State and setup, read:** `codeforge doctor` (setup, sign-in, models, persona, trust), `codeforge models` and `models recommend`, `codeforge provider show`, `codeforge persona describe [--full]`, `codeforge persona list`, `codeforge sessions --list [--all]`, `codeforge version`.

## Only the user runs these
They are the user's decisions, and the guard refuses most of them from you. Say which command to run and why; never look for another route.
- Settings: `init`, `repair`, `reinit`, `trust`, `models set|apply|configure`, `provider set|configure|clear`.
- Decisions on your work: `gate accept|unaccept|reset`, `review accept|unaccept`.
- Decisions on what is learned: `learn accept|dismiss|forget|revert`, any `learn` command with `--session`, and `codeforge observe`.
- `gh` aliases and extensions other than `gh stack`; scripts and interpreter code that call `codeforge`.

Not refused, but still the user's call, in their own terminal: `persona update|clear|share|forget|revert` and `explain`.

## Modes
`chat`, `work`, `ticket`, `review`, `respond` and `explain` open a session of their own, so do not start one from inside a session: suggest `/profile codeforge-chat`, `/profile codeforge-work` or `/profile codeforge-ticket`, or name the command for the user to run. Review sessions are read-only: there only `help`, `version`, `doctor`, `job list`, `gate accepted`, `review accepted` and the printing forms of `models`, `provider` and `persona` run, and nothing is recorded.
