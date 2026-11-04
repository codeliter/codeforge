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

Codeforge works with its built-in model choices. Change them in three steps:

| Step | Command | What it changes |
| --- | --- | --- |
| 1. Choose | `codeforge models configure` | Codeforge's saved choices only. |
| 2. Check | `codeforge models plan` | Nothing. It only shows what will be used. |
| 3. Apply to Empryo | `codeforge models apply` | Empryo's settings, after you confirm. |

Step 3 is only needed if you want the Empryo screen to start on your coding model, or if you set a backup provider.

### 1. Choose

```sh
codeforge models configure
```

You pick a model for each job: coding, expert help, review, effort judging, and persona writing.

- **Provider defaults (recommended):** pick a main provider and an optional backup provider, then pick models by name, such as **Sonnet 5.5**. For DevPass, choose **LLM Gateway / DevPass** as main and **Copilot** as backup.
- **Advanced:** type exact `provider/model` IDs. These choices have no Codeforge backup provider.

New setups use **Sonnet 5.5** for coding and **Opus 5.5** for expert help. Existing choices are kept.

You also choose where to save:

- **This project:** `.codeforge/config.yaml`. Wins over global choices.
- **All projects:** `~/.codeforge/config.yaml`.

The wizard approves the project file it saves. If you edit a project file by hand, or get one from someone else, check it and then run `codeforge trust --models`. Until then, Codeforge ignores it, so a shared file cannot pick its own reviewers.

Prefer editing a file? See [config.example.yaml](config.example.yaml). Old `~/.codeforge/config.json` files still work.

### 2. Check

```sh
codeforge models plan
```

This shows the model and backup for each job. It never changes anything.

### 3. Apply to Empryo

```sh
codeforge models apply
```

This changes two Empryo settings:

- **`defaultModel`:** the model the Empryo screen selects when it opens. It is set to your coding model.
- **`modelFallback`:** the backup for each model. Only changed if you chose a backup provider.

Here's what happens:

1. You choose where to apply: **this project** or **global**. Inside a Git project, **this project** is preselected. Skip the question with `--project` or `--global`.
2. Codeforge shows every change as `old -> new` and asks once.
3. If you say no, nothing is written.
4. If you say yes, Codeforge writes the changes and reads them back to check them.

Nothing else is changed: other settings, logins, and billing are kept.

**Applied globally, but Empryo still opens on another model?** That project has its own Empryo `defaultModel`, and project settings win. Codeforge warns you when this happens. Run `codeforge models apply --project` in that project.

**Want the recommended models instead of your saved ones?** Run `codeforge models apply --recommended`. It shows the role changes and saves them after you confirm, then applies them as above. `codeforge models recommend` only shows recommendations.

**When does Codeforge change Empryo's settings?** Only during `models apply`. `models configure` and normal launches never do.

**Using a backup provider?** Each launch checks that Empryo's backup settings match your choices. If they don't, Codeforge stops before using any model and asks you to run `models apply`. Empryo handles retries and switching to the backup.

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
