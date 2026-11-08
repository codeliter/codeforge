# Codeforge

An AI helper for your code. It answers questions, makes changes, and reviews pull requests in your voice.
It runs on [Empryo](https://empryo.com).

## Install

```sh
brew install codeliter/tap/codeforge
```

You also need Git 2.54 or newer and GitHub CLI (`gh`).

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

## Reviews

`codeforge review` opens the finished review as a list of files. Click a file, or press Enter, to open it: each finding shows the code
around it first (with the function it is in, and the lines the comment names marked), then the comment. `a` opens
all, `q` quits, and `c` opens a chat to ask about the review. You can also ask later with `codeforge --resume ID`.

To post it on the PR, add `--comment` and press `p` in the review. It goes up as one GitHub review in your voice, never
an approval, with each finding as an inline comment on its line. The model itself can never post.
`--comment=yes` posts without asking.

`codeforge gate` reviews your branch the same way, and runs the changed packages' tests alongside. In a work or chat
session you can just ask for a review; no second terminal is needed. Review sessions are read-only, enforced by a tool
guard and a sandbox (macOS for now).

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
