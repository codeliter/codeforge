# Codeforge

An AI helper for your code. It can answer questions, make changes, and review pull requests.
It runs on [Empryo](https://empryo.com).

## 1. Install it

```sh
brew install codeliter/tap/codeforge
```

You also need Git 2.54 or newer and GitHub CLI (`gh`).

## 2. Set up your project

Open a terminal in your Git project:

```sh
cd your-project
codeforge init
```

Codeforge checks what it needs and helps you sign in.
It asks before installing Empryo if Empryo is missing.

It then learns from your work in **this project** and writes:

```text
.codeforge/persona.md
```

This file tells Codeforge how to work in this project. You can edit it.
An existing, filled-in file is kept. An empty file or untouched old template can be filled in.
Your global persona is not changed.

No history yet? Choose a ready-made starting point:

```sh
codeforge init --persona
```

Use the arrow keys to pick one, then press Enter:

- **oss:** Friendly reviews, stable APIs, and good docs.
- **pragmatic:** Small, clear changes backed by tests.
- **regulated:** Careful changes with risk, audit, and rollback notes.
- **startup:** Ship quickly with small PRs and practical follow-ups.

Already know your choice? `codeforge init --persona oss` skips the menu.
Scripts must include the name because they cannot answer the menu.

Want to set rules for **all** projects instead? Ask for that explicitly:

```sh
codeforge init --global
```

Those rules go in `~/.codeforge/persona.md`.
Outside a Git project, `init` also uses this global location.

## Choose models (optional)

```sh
codeforge models configure
```

Choose **provider defaults** to pick a primary provider and optional fallback once, then choose model names for coding, expert help, review, effort judging, and persona generation.
For DevPass, choose **LLM Gateway / DevPass** first and **Copilot** as fallback. The preview shows the actual catalog IDs.
The advanced style keeps explicit `provider/model` choices; those use native Empryo fallback instead of the shared Codeforge preference.
New setups use **Sonnet 5.5** for everyday coding and **Opus 5.5** only when expert help is needed. Existing saved choices are kept.
Choose whether to save for this project or for all projects. You can also choose models for helper jobs.

Project choices live in `.codeforge/config.yaml`. Global choices live in `~/.codeforge/config.yaml`.
Approved project choices win over global choices. Your normal Empryo settings and logins are not changed.
The wizard approves the exact settings you save. For a manually edited or shared project file, inspect it and run `codeforge trust --models`.
Without approval, Codeforge ignores that project's model file instead of letting it choose its own reviewers.
Old global `config.json` files still work; saving model choices writes YAML and keeps the old JSON file.
Prefer editing a file? See [config.example.yaml](config.example.yaml).

**Normal launches only verify fallback.** With a selected backup, the same model must exist on both connected providers, and its native Empryo fallback chain must already match. Codeforge stops before model work if it does not.

```sh
codeforge models plan
```

This command is read-only. To apply the saved fallback plan, run:

```sh
codeforge models apply
```

Inside a Git repository (or a directory with a project model file), the scope prompt defaults to **this project**; elsewhere it defaults to **global**. You can override that choice with `--project` or `--global`. Codeforge shows the native fallback changes and asks for confirmation before writing through Empryo's supported settings interface, then verifies them. Unrelated settings and fallback entries are retained. Run `models plan` again or start a new task after it succeeds.

`models apply` uses your saved model choices; it no longer replaces roles with recommendations. `models recommend` remains read-only. `models configure` and normal launches never write native settings; only this explicit apply command can do so. Credentials and billing configuration are never changed. Empryo owns quota retries and failover timing.

## 3. Give it a job

```sh
codeforge chat "Where is the login code?"
codeforge work "Fix the login bug and test it"
codeforge ticket --ticket APP-123 "Fix the login bug"
codeforge review 123
codeforge respond 123
```

Or run `codeforge` and pick a mode.

**Not sure? Start with `work`.**

| Mode | Use it when… |
| --- | --- |
| `work` | You want a change built and tested, without ticket or PR steps. |
| `chat` | You want an explanation, a plan, or a small edit. |
| `ticket` | You have a ticket to take through implementation and the draft-PR workflow. |
| `review` | You want a separate, read-only review of a PR or local changes. |
| `respond` | You want to address feedback on your existing PR. |

For “make a change, review it, then fix the findings”, stay in `work` and ask for `codeforge-reviewer`.
You do not need to start another terminal.

Codeforge acknowledges a new job right away. It prepares the context, then waits for the model.
The job runs in your terminal. When it finishes, a chat screen opens for follow-up questions.
A slow or broken provider can still delay the model's reply.

A standalone review is read-only and posts nothing. Its sandbox currently needs macOS.
Use `codeforge review` with no PR number to review your local changes.

You can also ask for a review in your existing work or chat screen. No second terminal is needed.
The `codeforge-reviewer` helper returns findings and can ask the parent conversation to delegate agreed fixes to `codeforge-work`.
For another person's PR, it can draft a comment. The parent must show you the exact PR and comment and ask your approval before posting.
The reviewer itself cannot edit or post; a review request is not permission to publish.

## Something is broken?

Run this yourself in a terminal:

```sh
codeforge repair
```

`codeforge reinit` does the same thing.

This rebuilds Codeforge's startup files and safety checks.
The safety-check program stays at `~/.empryo/hooks/cf`, not in a temporary folder.
Old, broken Codeforge hooks are replaced. Your other hooks, personas, sessions, and logins are kept.
Close and reopen affected sessions after repairing.

If an old **Codeforge** hook used another name, tell repair its old path:

```sh
codeforge repair --legacy-hook /tmp/old-bin
```

Use the path shown in your error; `old-bin` is only an example. Repeat `--legacy-hook` to name more than one old path.
Only use this for old Codeforge executables you recognise. Other security hooks are kept.
If that path is not in the global hook file, repair warns you to check the project's or other agent's hook settings.

Repair does not fix a provider's quota, a failed sign-in, or a broken internet connection.

## Come back later

Use the session ID shown when you leave:

```sh
codeforge --resume SESSION_ID
```

Run it from the same project. Codeforge keeps the original mode, including review's read-only protection.

## Update it

After a new release is published:

```sh
brew update
brew upgrade codeforge
codeforge repair
```

Use Homebrew for installed updates. Do not copy local builds over the installed command.

## More help

```sh
codeforge help
codeforge models
codeforge version
```

For architecture diagrams and research citations, see [TECHNICAL.md](TECHNICAL.md).
