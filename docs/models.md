# Models and providers

Codeforge works with its built-in model choices. This page is for changing them.

| Step | Command | What it changes |
| --- | --- | --- |
| 1. Choose | `codeforge models configure` | Codeforge's saved choices only. |
| 2. Check | `codeforge models plan` | Nothing. It only shows what will be used. |
| 3. Apply to Empryo | `codeforge models apply` | Empryo's settings, after you confirm. |

Step 3 is only needed if you want the Empryo screen to start on your coding model, or if you set a backup provider.

## 1. Choose

```sh
codeforge models configure
```

You pick a model for each job: coding, expert help, review, effort judging, and persona writing.

- **Provider defaults (recommended):** pick a main provider and an optional backup provider, then pick models by name, such as **Sonnet 5.5**. For DevPass, choose **LLM Gateway / DevPass** as main and **Copilot** as backup.
- **Advanced:** type exact `provider/model` IDs. These choices have no Codeforge backup provider.

New setups use **Opus 5.5** for coding, **Fable 5.2** for expert help and **Sonnet 5.5** to write the persona. Existing choices are kept.

You also choose where to save:

- **This project:** `.codeforge/config.yaml`. Wins over global choices.
- **All projects:** `~/.codeforge/config.yaml`.

The wizard approves the project file it saves. If you edit a project file by hand, or get one from someone else, check it and then run `codeforge trust --models`. Until then, Codeforge ignores it, so a shared file cannot pick its own reviewers.

Prefer editing a file? See [config.example.yaml](../config.example.yaml). Old `~/.codeforge/config.json` files still work.

## 2. Check

```sh
codeforge models plan
```

This shows the model and backup for each job. It never changes anything.

## 3. Apply to Empryo

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
