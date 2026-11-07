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

- **`defaultModel`:** the model the Empryo screen selects when it opens. It is set to your coding model. With a model rule, that is the provider the rule picks: a `claude-*` rule on `subscriptions` makes Empryo open on `subscriptions/claude-sonnet-5-5`.
- **`modelFallback`:** the backups for each model, in order. Only changed if you set backup providers. Each model gets the backups of the order that applies to it, so Claude models can fall back to one provider and GPT models to another.

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

## Provider order and backups

Connected to more than one provider? Choose which one Codeforge uses first, and which ones Empryo switches to when it fails or runs out of quota. The easiest way is the menu:

```sh
codeforge provider configure
```

It asks where to save (this project or all projects), then for the primary provider, then for backups one at a time in order. Pick **Done** when you have enough. Each provider shows which stand-in it would use for a model it doesn't have, so you know what you get before you choose. It shows the final order, saves it when you confirm, and offers to run `models apply` straight away.

Prefer one line? This does the same without the menu:

```sh
codeforge provider set devpass copilot openrouter
codeforge models apply
```

- The first name is the **primary**. Every model name, such as **Sonnet 5.5**, runs there.
- The rest are **backups**, tried in the order you list them. You can list as many as you have connected.
- `models apply` writes the order into Empryo and asks first. Until then, nothing changes in Empryo.

A backup uses the same model as the primary when it has it. When it doesn't, Codeforge picks that provider's closest model instead of dropping the backup:

1. Another version of the same model, for example `claude-sonnet-5` for **Sonnet 5.5** (the nearest older one, else the nearest newer one).
2. Otherwise a model of the same level from the same maker, then from any maker, newest first. Levels are top (Opus, GPT Sol, Kimi), everyday (Sonnet) and fast (Haiku, Luna, Flash, Nano, Mini).

`codeforge models plan` marks these backups `(closest)`. Only a provider with nothing at the same level is refused; `provider set` then names the model and saves nothing.

| Command | What it does |
| --- | --- |
| `codeforge provider` | Shows the order and which providers are connected. |
| `codeforge provider configure` | Picks the order from a menu. |
| `codeforge provider set [PRIMARY [BACKUP...]] [--for MODEL-GLOB PROVIDER[,PROVIDER...]] [--no-rules]` | Saves a new order, optionally with a separate order for some models. |
| `codeforge provider clear` | Removes the order. |

### Different providers for different models

A model family can have its own order. This runs Claude models on Anthropic (backup: OpenRouter) and everything else on the default order:

```sh
codeforge provider set devpass copilot --for 'claude-*' anthropic,openrouter
```

- A pattern is a model name where only `*` and `?` are wildcards; a provider prefix, `[ ]` and `\` are rejected.
- `--for` takes a model pattern and a comma-separated order, and can be repeated. `Sonnet-*` means `claude-sonnet-*`. The first matching rule wins.
- A rule's order replaces the default order for those models. Its backups never switch to another family, so a Claude rule never falls back to a GPT model.
- `provider set` changes only what you give it: a chain keeps the saved rules, `--for` replaces the saved rules and keeps the chain, and `--no-rules` removes the rules. `provider clear` removes everything.
- A project's `providers` block replaces the global one, rules included.

Inside a Git project the order is saved for that project; add `--global` to save it for all projects, or `--project` to be explicit. Use `/login` in Empryo to connect another provider first.

**Use the same scope for `provider set` and `models apply`.** `models apply` reads the order from the scope you choose. If you saved it with `provider set` in a project but apply globally, `models apply` shows your old global order and says `nothing was written`. Check with `codeforge provider`, then apply the same scope:

```sh
codeforge provider set litellm-llmgateway --for 'claude-*' subscriptions,litellm-llmgateway --global
codeforge models plan
codeforge models apply --global
```

`models plan` shows each model on its own order, and the backups it prints are what `models apply` writes to `modelFallback`. Models outside every rule keep the default order, with no backup from another family.

When Empryo switches, the chat says `Switched to fallback model: …`. It retries the same model a few times before switching, and tries a model that failed recently later than the others. See Empryo's [model fallback](https://empryo.com/docs/recipes/model-fallback) page for which errors cause a switch.

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

`codeforge ticket` with no description asks for two things: the ticket ID (optional) and a description of the work. The description alone sets the effort. After the agent's first pass, Codeforge reviews the change with models that didn't write it, sends any blocking findings back to the agent (up to 3 rounds), then opens the session.

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
The reviewer itself cannot edit or post; a review request is not permission to publish. Review sessions are enforced with a tool guard and a read-only sandbox; other modes only protect codeforge's own settings and trust.

## It learns how to check your work

Codeforge notices which test, lint and build commands pass in your project and tells the agent to use them next time.
`codeforge learn habits` shows what it learned, and `codeforge learn revert ID...` removes any of it.
Only plain commands the tool reported as passing count; nothing is learned from review sessions.

## It learns your corrections

When you type the same correction in two separate sessions ("always use table-driven tests"), codeforge learns it as a
rule for that directory by itself at the next launch and says so, with how to undo it. You do not have to approve
anything. Corrections worded differently are offered by `codeforge learn proposals` for you to accept.
`codeforge learn rules` lists the rules and `codeforge learn forget R1` removes one for good (it is not learned again).
`codeforge learn proposals` shows what is about to be learned, and `learn accept P1` or `learn dismiss P1` decide early.
Only your own prompts count, never the model's; opposite corrections never merge; and review sessions learn nothing.

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
```

The next time you run `codeforge`, it updates its own setup before starting. Nothing else is needed.

Use Homebrew for installed updates. Do not copy local builds over the installed command.

## More help

```sh
codeforge help
codeforge models
codeforge version
```

For architecture diagrams and research citations, see [TECHNICAL.md](TECHNICAL.md).
