# Codeforge

An AI helper for your code. It answers questions, makes changes, and reviews pull requests in your voice.
It runs on [Empryo](https://empryo.com).

## Install

```sh
brew install codeliter/tap/codeforge
```

You also need Git 2.54 or newer and GitHub CLI (`gh`).

Homebrew installs tab completion for zsh, bash and fish. Built from source? Add one line to your shell's startup file:
`source <(codeforge completion zsh)` in `~/.zshrc`, `source <(codeforge completion bash)` in `~/.bashrc`, or
`codeforge completion fish | source` in `~/.config/fish/config.fish`.

From source, use `make install`: it installs both `codeforge` and `codeforge-forms`, its menus and review view.

```sh
git clone https://github.com/codeliter/codeforge && cd codeforge && make install
```

## Set up a project

```sh
cd your-project
codeforge init
```

Codeforge checks what it needs, helps you sign in, and offers to install Empryo if it is missing. It then learns how
you work in this project and writes `.codeforge/persona.md`, which you can edit. An existing, filled-in file is kept.

No history yet? `codeforge init --persona` lets you start from **oss**, **pragmatic**, **regulated** or **startup**.
For rules that apply to every project, use `codeforge init --global` (`~/.codeforge/persona.md`).

## Use it

```sh
codeforge work "Fix the login bug and test it"
codeforge chat "Where is the login code?"
codeforge ticket --ticket APP-123 "Fix the login bug"
codeforge review 123
codeforge respond 123
```

Or run `codeforge` and pick a mode. **Not sure? Start with `work`.**

| Mode | Use it when you want… |
| --- | --- |
| `work` | a change built and tested, without ticket or PR steps. |
| `chat` | an explanation, a plan, or a small edit. |
| `ticket` | a ticket taken to a verified change and a draft PR. Other models review it first and blocking findings go back to the agent (up to 3 rounds). |
| `review` | a read-only review of a PR, or of your local changes with no number. |
| `respond` | to address the feedback on your PR. |

The job runs in your terminal; when it finishes, a chat screen opens for follow-up questions.

To pick up earlier work, run `codeforge sessions`: it lists this directory's sessions, newest first, with when, mode,
title, turns, time spent, cost and model, under a search box; type to narrow the list, Enter opens the session in its
own mode. `--all` lists every project's sessions (those started at its root) and opens the one you pick in its project; `--list` prints them
instead. Empryo's own session list does not know codeforge's modes, so use this one.

## Reviews

`codeforge review` shows the review by file: each finding with its code, then a final comment and a suggested
decision. Enter or a click opens a file, `c` chats about it, `q` quits.

| To | Use |
| --- | --- |
| Post it as a GitHub review | `p`, then `1` Approve, `2` Comment or `3` Request changes |
| Post comments you can delete later | `p`, then `4` |
| Post the suggested decision without asking | `--comment=yes` |
| Post from a script or without the view | `--comment`, then answer the question |
| Follow up on your earlier review | `codeforge review 98`, or `--follow-up` to stop if there is none |
| Review your branch and run its tests | `codeforge gate` |

On a PR you reviewed before, codeforge follows up: a verdict on each of your open threads, replies and resolves
the settled ones (never a blocker on a promise), then reviews only the new commits. `--fresh` starts over.
When the author explains why a non-blocking point is not a problem and the reason holds, the reviewer agrees in your
voice, resolves the thread, drops the point and suggests the decision that now fits, Approve when nothing blocks.

```sh
codeforge review 98               # follow up on your earlier review of PR 98, after the author replied or pushed fixes
codeforge review 98 --follow-up   # the same, but stop if you never reviewed it
codeforge review 98 --fresh       # a full review again instead
```

A follow-up offers the decisions under `p` without `--comment`; nothing is posted until you pick one.

Reviews are read-only and the model never posts; you do.

## Answering reviews

`codeforge respond` works through the open review threads on your branch's PR: it judges each comment on evidence,
commits the fixes that hold, and drafts a reply in your voice. It then shows the threads in the same view, by file:
the code the reviewer commented on, their comment, the verdict, the fix's diff and the draft reply. `p` pushes the
fixes and posts the replies, `c` continues in a chat to change any of them, and `q` leaves with nothing pushed or posted.

## It learns

- **How to check your work:** the test, lint and build commands that pass here are suggested to the agent next time.
  See them with `codeforge learn habits`.
- **Your corrections:** a correction you type in two sessions ("always use table-driven tests") becomes a rule for
  that directory. `codeforge learn rules` lists them, `codeforge learn forget R1` removes one.

Review sessions learn nothing, and only your own prompts count.

## Models and providers

Codeforge works out of the box. To pick models per job or set a provider order with backups:

```sh
codeforge models configure   # choose
codeforge models plan        # check, changes nothing
codeforge models apply       # write to Empryo, after you confirm
codeforge provider configure # primary provider and backups
```

Details in [docs/models.md](docs/models.md).

## Theme

Codeforge's screens use its own theme, **Codeforge Steel** (dark or light, to match your terminal), and put your own
Empryo theme back when they close. Set `CODEFORGE_THEME=off` to keep yours.

## Resume, update, help

```sh
codeforge --resume SESSION_ID          # from the same project; the mode is kept
brew update && brew upgrade codeforge  # Codeforge updates its own setup on the next run
codeforge help
```

How it works, with diagrams and research citations: [TECHNICAL.md](TECHNICAL.md).
