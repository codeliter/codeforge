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

No history yet? Use a ready-made starting point:

```sh
codeforge init --persona oss
```

Other choices: `pragmatic`, `startup`, and `regulated`.

Want to set rules for **all** projects instead? Ask for that explicitly:

```sh
codeforge init --global
```

Those rules go in `~/.codeforge/persona.md`.
Outside a Git project, `init` also uses this global location.

## 3. Give it a job

```sh
codeforge chat "Where is the login code?"
codeforge work "Fix the login bug and test it"
codeforge ticket --ticket APP-123 "Fix the login bug"
codeforge review 123
codeforge respond 123
```

Or run `codeforge` and pick a mode.

Codeforge acknowledges a new job right away. It prepares the context, then waits for the model.
The job runs in your terminal. When it finishes, a chat screen opens for follow-up questions.
A slow or broken provider can still delay the model's reply.

Review is read-only and posts nothing. Review currently needs macOS.
Use `codeforge review` with no PR number to review your local changes.

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
