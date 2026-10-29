---
name: workflow-builder
description: Build a bot or agent that runs on a schedule or event (cron, webhook, twice a day, daily digest, monitor, triage); also multi-agent Reliant workflows. Use to create, edit, test or debug any workflow.
compatibility: reliant
metadata:
  category: workflow
  owner: reliant
---
# Workflow Builder

You build Reliant workflows. Your goal: create working workflows that solve the user's problem.

Threading, context, loops, parallelization, inputs, prompts, error handling and composition
have their own do's-and-don'ts reference: `skill(action="load", path="workflow-builder/design-patterns")`.
Load it before designing a workflow's control flow.

## When to use

- Building an agent or automation that runs on its own: on a schedule ("every morning", "twice a day", cron), on a webhook or an app event — a bot, monitor, digest or triage job, even when it sounds like "just a script"
- Creating a new Reliant workflow from scratch
- Editing or extending an existing workflow
- Debugging workflow execution issues
- Writing scenario tests for workflows
- Understanding workflow patterns and best practices

## Approach

Every workflow tool that targets an existing workflow (`get_workflow`, `edit_workflow`, `write_workflow`, and the scenario tools) requires an explicit
`id` — a UUID, slug, or name. Nothing binds this chat to a workflow, so there is no "current workflow"; any chat can work on any workflow.

Starting from scratch? Call `create_workflow` first — it returns the new draft's `id`, `name`, and `slug`. Pass that `id` to every
subsequent call. Omit both `name` and `content` to get a random name and the default agent template; pass `content`
with complete workflow YAML to start from a specific design.

Editing an existing workflow? Find it with `list_workflows` and pass its id or slug. If the user's message names a
workflow (e.g. "Workflow `swift-fox-a1b2`: ..."), use that as the `id`.

Follow this process:

1. **Setup** — New workflow: call `create_workflow`. Existing workflow: call `get_workflow` with its `id` to see current content.
2. **Understand** — Ask clarifying questions about the user's goal
3. **Learn** — Use `list_workflows` to see examples and patterns, `list_presets`/`get_preset` to see what
   agent presets exist before inventing a system prompt from scratch. Calling an external service?
   `search_integrations` finds the action ref and `get_integration_schema` its params before you write the node
4. **Explore** — Read the user's codebase (test commands, code patterns). Note: references to "workflows" and "nodes" in user code are unlikely to be Reliant-specific.
5. **Build** — Use `edit_workflow` for small changes, `write_workflow` for larger rewrites
6. **Test** — Create and run scenarios (aim for 3+ covering positive, negative, and edge cases). Try to break your workflow. It's frustrating for users to run a workflow for an hour and hit a bug at the end—scenarios catch this early.

Working in a chat that isn't running this preset? These tools are still reachable:
`load_tool(name="tag:workflow")` loads all of them on demand.

## Key concepts

### Workflows are DAGs

Every workflow must be a directed acyclic graph. You cannot create cycles in the graph structure. Use **loop nodes** (with `while` conditions) for iterative behavior.

### CEL expressions

Two syntax modes:

- **`condition` / `while` fields**: Pure CEL — no `{{}}` wrapping
  ```yaml
  condition: "nodes.llm.stop_reason == 'tool_use'"
  while: "outputs.stop_reason in ['tool_use', 'incomplete', 'truncated', 'interrupted'] && iter.iteration < 50"
  ```
- **All other fields**: Template interpolation with `{{}}`
  ```yaml
  model: "{{inputs.model}}"
  system_prompt: "You are helping with {{inputs.task}}"
  ```

Always use `has()` or safe navigation (`object.?field`) before accessing optional fields.

**Template rules.** A `{{ }}` is read as CEL, so a brace inside a string literal is data
(`{{ x.startsWith('{') }}` works) and a newline inside it is whitespace (a folded `>-` scalar is fine). A `{{`
that never closes, an empty `{{ }}` and an expression that does not parse are validation errors with the
field path and line/column, never literal text; write a literal `{{` as `{{ '{{' }}`. Raw-CEL fields
(`condition`, `while`, a trigger `filter`) take no `{{ }}`. Inside an inline `type: workflow` body, templates are
evaluated in the BODY's scope when it runs (its own `inputs` and `nodes`), not the parent's.

**Deterministic logic belongs in the workflow, not a script.** Use a `type: compute` node (below) for
labels, size buckets, lease expiry, id validation and parsing a node's JSON stdout. Expressions have
Strings, Lists, Sets, Math, Encoders, Bindings, Regex, optional types, timestamps and durations, and
`json.decode(s)` (tolerant: an optional, `.?status.orValue('error')`) beside the strict `parseJson(s)`. Evaluation is
cost-bounded (a runaway expression fails with `cost limit exceeded`). The CEL reference has a table of
worked helper expressions. **Test an expression with `cel_eval` before writing it into a workflow.**

### Compute node

`type: compute` computes named outputs from CEL in the workflow: no model, no shell, no worker, no mock needed in a
scenario. `outputs:` maps a name to a value; a value that is exactly one `{{ }}` keeps its type, text around it makes a
string, a non-string is a literal. Read them as `nodes.<id>.<name>` (validation checks the names; values are dyn, so compare a boolean output in a `condition`: `nodes.facts.ready == true`). Outputs cannot
read each other: chain two compute nodes. A failing expression fails the node.

```yaml
- id: facts
  type: compute
  outputs:
    size: "{{ inputs.adds + inputs.dels < 60 ? 'size:S' : 'size:M' }}"
    ready: "{{ 'agent:ready' in inputs.labels }}"
    status: "{{ json.decode(nodes.start.stdout).?status.orValue('error') }}"
```

### Publishing a branch

`type: git_publish` commits a worktree's changes and pushes them to a feature branch with no shell ever holding a
credential. Pair it with `credentials: none` on the editing shell.

```yaml
- id: publish
  type: git_publish
  args:
    worktree: "{{ nodes.wt.path }}"
    repo: "{{ inputs.repo }}"                      # owner/name
    branch: "autopilot/issue-{{ inputs.issue }}"   # or pr_head_of: "{{ inputs.pr }}"
    allow: ["autopilot/*"]                         # optional
    message: "{{ nodes.edit.commit_message }}"
```

Refused (the node completes with `refused: true` and a `refusal`, it does not fail): the default branch, protected
branches, `main`, `master`, `release/*`, names that are not plain branches, branches outside `allow`, a pull
request head in a fork. Never forces: a diverged remote is refused. No changes means `pushed: false` and no error.
Outputs: `pushed`, `sha`, `branch`, `base_sha`, `files_changed`, `insertions`, `deletions`, `refused`, `refusal`. Route on
them: `condition: nodes.publish.pushed == true`. The push is recorded in the write ledger with its sha, so its events
carry `trigger.origin`. In a scenario, mock the node's outputs like any activity.

### Shells without credentials

`credentials: none` removes GitHub, git, ssh and cloud credentials from a command's environment: no
`GH_TOKEN`/`GITHUB_TOKEN`, no ssh agent, no cloud variables, no kubeconfig, git's credential helpers reset,
`GIT_TERMINAL_PROMPT=0`, an empty `GH_CONFIG_DIR`. A `git push` or `gh pr merge` from it fails to
authenticate at once and never prompts. Set it for an agent's shell with `tools_config.tools.shell` (bound, so
the model is never shown it) and on a `run` node with `credentials: none`.

```yaml
tools_config:
  tools:
    shell: {credentials: none}
```

On Linux (every Reliant cloud machine) the command also runs in a sandbox where the daemon's credential files are
absent (`~/.reliant/daemon.json`, `~/.config/forge`, `~/.git-credentials`, `~/.ssh`, `~/.config/gh`, kubeconfig and cloud files, the
container engine's socket), sees only its own processes, and has no capabilities. It shares the network and can
write its working directory, and on macOS and Windows only the environment is scrubbed. Never tell an agent to look
elsewhere for a token; publish with a `git_publish` node.

### Threading

Modes: `inherit` (default), `new`, `fork`

- **inherit**: Reuse parent thread (default)
- **new**: Create a fresh thread
- **fork**: Copy parent thread into a new one

Key rules:
- Use `memo` in loops to reuse the same thread across iterations
- Use `inject` to prepend a message when entering a sub-workflow
- **Never** run parallel agents on the same thread simultaneously

### Edge routing

- Multiple `cases` on one edge = exactly 1 executes (first match wins)
- All cases require a `condition` — use `default` for the fallback
- For parallelism: create multiple edges from the same source node, OR use `default: [node-a, node-b]`

## Syntactic sugar

These shorthands compile to standard nodes/edges at parse time. They are **not** in the proto — purely YAML convenience.

### `sequence:` sugar

A top-level workflow field (alongside `name:`, `nodes:`, `edges:`) that replaces the combination of `entry:`, `nodes:`, and sequential `edges:` for linear chains.

Rules:
- Cannot coexist with `entry:` (it implies entry from the first node)
- CAN coexist with `nodes:` and `edges:` for mixed patterns (e.g., linear main flow with extra branches)
- Compiles to standard `entry:` + `nodes:` + `edges:` at parse time

Before (explicit):
```yaml
entry: [research]
nodes:
  - id: research
    type: workflow
    ref: builtin://agent
  - id: implement
    type: workflow
    ref: builtin://agent
  - id: review
    type: workflow
    ref: builtin://structured-agent
edges:
  - from: research
    default: implement
  - from: implement
    default: review
```

After (sugar):
```yaml
sequence:
  - id: research
    type: workflow
    ref: builtin://agent
  - id: implement
    type: workflow
    ref: builtin://agent
  - id: review
    type: workflow
    ref: builtin://structured-agent
```

### `type: parallel` sugar

A node type used inside `nodes:` that desugars into branch nodes + a join node + fan-out/fan-in edges.

Rules:
- Must have `id:` and `branches:` fields
- `branches:` is a list of regular node definitions
- The parallel node's `id` becomes the join node's id (so downstream edges referencing it still work)
- Join uses `condition: all` (all branches must complete)
- Incoming edges targeting the parallel node get rewritten to fan-out to all branches

Before (explicit):
```yaml
nodes:
  - id: research
    type: workflow
    ref: builtin://agent
  - id: design
    type: workflow
    ref: builtin://agent
  - id: explore
    type: join
    condition: all
edges:
  - from: trigger
    default: [research, design]
  - from: research
    default: explore
  - from: design
    default: explore
```

After (sugar):
```yaml
nodes:
  - id: explore
    type: parallel
    branches:
      - id: research
        type: workflow
        ref: builtin://agent
      - id: design
        type: workflow
        ref: builtin://agent
edges:
  - from: trigger
    default: explore
```

## Triggers: when a workflow runs

A workflow says WHEN it runs in a top-level `triggers:` list. Declaring a trigger fires nothing.
In an interactive chat, use create_trigger_activation, update_trigger_activation, enable_trigger or pause_trigger
when asked to manage an activation. Each change raises an approval card; nothing changes until the owner approves.
Unattended runs cannot manage activations. Do not bypass approval through the CLI, HTTP or a script. Enabling says AS WHOM and WHERE:
their project, the machine whose tools the runs use, and for an integration trigger which of their connected
accounts it listens through. One declaration can have many activations (one per user or project).

```yaml
name: triage-new-issues
inputs:
  repos: {type: github.repository, multi: true, required: true, min_items: 1}   # typed by the integration; the user picks them when enabling
  issue_number: {type: integer, default: 0}   # required inputs must be mapped by every trigger, or set by the user when enabling
triggers:
  - name: new-issue                 # unique within the workflow: lowercase, digits, - and _
    description: A new issue
    integration:                    # exactly ONE source: schedule | webhook | integration | workflow_event
      integration: github
      events: [issues.opened]                    # "issues.*" or "*" also match
      match: {repository: {input: repos}}        # scope by an input: only the repositories the user picked. A plain value (repository: acme/app) must equal the attribute
    filter: "!('wontfix' in trigger.payload.data.issue.labels)"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
    prompt: "Triage issue #{{ trigger.payload.data.issue.number }}: label it, ask for a repro if one is missing."
  - name: nightly
    schedule: {cron: ["0 9 * * 1-5"], timezone: America/New_York}   # 5-field cron, or interval: 1h
  - name: deploy-hook
    webhook: {}                     # POST <api>/hooks/<trigger id>; add hmac: {...} to verify signatures
  - name: alertmanager
    webhook: {source: alerting}     # hosted: Enable connects the org's alerts, no token to copy; self-hosted: a plain webhook
    dedupe_key: "{{ trigger.payload.body.groupKey }}:{{ trigger.payload.body.status }}"   # see below
    dedupe_window: 4h
  - name: after-review-fails
    workflow_event: {workflows: [code-review], outcomes: [failed]}  # finished | failed | blocked
entry: [triage]
nodes: ...
```

**`filter`** is a raw CEL bool (no `{{ }}`) and **`inputs`** values are `{{ }}` templates. Both read ONLY
`trigger` — the run, its inputs and nodes do not exist yet. A schedule has no event, so no filter.
An event whose payload a filter or input cannot read is recorded as a FAILED firing: guard optional
fields with `has()` or a ternary. Each `inputs` key must be a declared workflow input; a value set there wins
over the user's params (enabling drops a param it would override).

**`prompt`** is the message every run the trigger starts begins from, a `{{ }}` template over `trigger` like
`inputs`. Write it so enabling needs no prompt: the prompt the user writes when enabling overrides it, and with
neither, enabling is refused. The event is still attached after it as untrusted data.

**`trigger.activation`** is the activation the event fires for, readable in a `filter`, `inputs`, `prompt` and `dedupe_key` (not in nodes, which read `inputs`):
`trigger.activation.params.<input>`, `.id`, `.owner.user_id`, and `.owner.github_id` (the owner's GitHub user id, a string: the connection it listens through if it names one, else the owner's linked GitHub identity; empty when the owner has neither, in which case a filter on it never matches the owner and the skip reason says so). One declaration serves many activations, each with its own inputs. `params` holds the workflow's inputs except those this trigger's `inputs` map from the event, with defaults applied and declared types.
Validation types it from the workflow's inputs: `trigger.activation.params.repo` for a `repos` input is an error when you save.

**Typed inputs: let the integration say what a value is.** An integration defines value types, and an input of that type is a picker in the Enable and Run dialogs, not a text box.
`get_integration_schema github` lists them; GitHub has `github.repository` (`owner/name`, from the repositories the Reliant GitHub App is installed on that the user can see), `github.label` (scoped to a repository), `github.user` (a numeric account id, stored as a string, never a login) and `github.installation`.

```yaml
inputs:
  repos:       {type: github.repository, multi: true, required: true, min_items: 1, description: The repositories to review.}
  trusted_ids: {type: github.user, multi: true, default: [], description: People who may ask for a re-review, besides you.}
  skip_label:  {type: github.label, scope: {repository: repos}, default: skip-review}   # offers the labels of the picked repositories
triggers:
  - name: pr-opened
    integration:
      integration: github
      events: [pull_request.opened]
      match: {repository: {input: repos}}
```

- **Never `type: array` (or `string`) for something an integration has a type for.** A repository list typed `array` is a comma-separated text box with nothing checking it, and the trigger cannot tell it is a repository scope. If you cannot find a type, run `get_integration_schema <integration>`; validation names the types the integration does declare.
- **Values stay plain JSON**, strings for every GitHub type, so `inputs.repos.exists(r, r == inputs.repo)` and `trigger.sender.id in trigger.activation.params.trusted_ids` keep working. `multi: true` makes it a list (`min_items`/`max_items` bound it); `scope: {repository: repos}` binds the type's scope key to another input.
- **`required: true`** is explicit: the user cannot turn the automation on until the input has a value, and an existing activation missing one shows "Needs setup" rather than going quiet. A required input takes no `default` (validation says so). An input with no default is required too; give a `default` (`[]` for a list) only when it is truly optional, like `trusted_ids`.
- **`match: {attribute: {input: name}}` scopes a trigger by an input.** The event is routed only when its attribute is one of the input's values, so an event for any other repository is dropped before a firing is recorded. The attribute and the input must have the same type (`repository` is a `github.repository`; validation checks it), the input must not be one the trigger maps from the event, and it should be `required`. It combines with literal `match:` entries and runs before `filter`. Keep `filter` for what a scope cannot say: drafts, labels, trust. Do not write `trigger.payload.attributes.repository in trigger.activation.params.repos` in a filter; validation warns and points at `match:`.
- **Trust by id**: `trigger.sender.id` is the GitHub numeric id, a string. `trigger.sender.verified && (trigger.sender.id == trigger.activation.owner.github_id || trigger.sender.id in trigger.activation.params.trusted_ids)` admits you and the people in a `github.user` input. The event attribute `sender_id` (a `github.user`) is the same id: `match: {sender_id: {input: allowed}}` scopes a trigger to the people a `github.user` input lists, on the events that declare `sender_id`. Use the filter, as above, when the owner is trusted without being listed.

**`dedupe_key`** (webhook triggers only) is a `{{ }}` template over `trigger` that names the event a delivery is about. Use
it for a sender with no idempotency header, such as Alertmanager, which re-sends a group with a changed body: the
default key (an idempotency header, else the body hash within one minute) would make every repeat a new run. Deliveries
whose keys render the same within one `dedupe_window` are ONE event (the first starts a run; the rest are recorded as
duplicates). The window is a fixed UTC-aligned clock bucket, default 1h, at most 24h, so it is the longest a repeat is
suppressed, not a sliding guarantee. A key that cannot render for a delivery falls back to the default key. It needs a
`{{ }}`, and `dedupe_window` needs a `dedupe_key`.

**Test a trigger before handing it over.** `cel_eval` with `integration` + `event` generates `trigger.payload` from the payload that
event declares (add `trigger_payload` to override only the fields your expression reads), and `scope: trigger` gives a
filter's, inputs mapping's or prompt's view (only `trigger`, activation included; give it `activation_params` and `owner_github_id`). It returns the value and its type, or the error with its
phase and position, plus `findings` for reads the declared payload rules out. Do this instead of waiting for a real
event: evaluation does not require enabling the trigger or waiting for a real event. `reliant workflow eval` is the same on the command line, and `reliant workflow validate --trigger-payload`
dry-runs every declared trigger (filter, inputs, prompt, dedupe key) against a sample event, with `--activation-params` and `--owner-github-id` for the sample activation.

**Chat is a trigger too, on by default.** Set top-level `automation_only: true` for a workflow only its
`triggers:` should start: chat pickers stop offering it and starting it from a chat is refused
(a builder test run still works). Leave it unset for anything a person starts by chatting. A helper that only
other workflows `ref:` sets `hidden: true` as well: hidden keeps it out of every picker and listing, and
the pair marks it as an automation's internal step.

**Where an automation's runs go.** A run a trigger fires is a chat, but not one in the chat list: it is listed under
Workflows → Runs, named by its automation. Once it finishes successfully it archives itself (still in Runs, marked
Archived). A failed run, a completion into a declared `outcome: failure`, a cancelled run and one awaiting input stay
unarchived so they surface. So route a blocked or punted path to a terminal node with `outcome: failure` when a person
should look at it. An automation with "notify me when it finishes" keeps its successful runs too.

What `trigger.payload` holds, by source:

| Source | trigger.payload |
|--------|-----------------|
| schedule | `scheduled_for`, `fired_at`, `trigger_name`, `manual` (also `trigger.scheduled_for`) |
| webhook | `body` (parsed JSON or text), `headers`, `query`, `content_type` |
| integration | `integration`, `event`, `account`, `delivery_id`, `attributes`, `data` (the provider's payload) |
| workflow_event | `run_id`, `chat_id`, `workflow_name`, `outcome`, `summary`, `error` |

The payload is untrusted data from outside. Read it in templates; never paste it into a system prompt.

An integration's payload is declared (`get_integration_schema` shows it), and validation type-checks the filter, inputs and
prompt against it. Lists of names are strings, not objects: GitHub's `issue.labels` and `issue.assignees`, and
`data.label` on `issues.labeled`. Write `'wontfix' in trigger.payload.data.issue.labels`, never `l.name`.

GitHub is the fullest integration for automations. Besides `issues.opened` and the like it delivers `issues.labeled`/`unlabeled`/`reopened`,
`pull_request.labeled`/`unlabeled`/`reopened` (`match: {label: ...}`), `pull_request_review_comment.created` (inline diff comments; `match` `is_reply`
and `mentions_reliant`), and `check_run.completed` / `check_suite.completed` / `workflow_run.completed` (`conclusion`, `branch`, `app`).
Its `type: action` nodes cover comments (`issue.comment`, `.update`, `.list`), atomic `issue.labels.add`/`remove` (prefer them to `issue.update`,
which replaces the whole label list and races), `issue.search`, `labels.create` (idempotent, for pack setup) / `labels.list`, `pr.create`/`update`, `pr.reviews.list`, `pr.review_comments.list` and
`pr.review_comment.reply`. There is no merge action, on purpose. Actions that change GitHub are withheld from an unattended run unless the
step names them in its `tools`. A trigger on a label event also fires for a label a run itself adds, so guard it.

**`trigger.sender`** is who sent the event, set by the receiver from what the source authenticated, never from the
payload: `kind` (slack, github, email, sms, webhook, workflow, schedule, user), `id`, `display_name`, `verified`.
Slack's `id` is the user id, GitHub's the numeric user id as a string (`"583231"`; the login is `display_name`, and
since a login can be renamed and then registered by someone else, never allowlist logins), email's the lowercased address (verified only when
Gmail's DMARC, or DKIM aligned with the From domain, passed), sms the From number (never verified). To let only
certain people start runs, filter on it and require `verified`:
`trigger.sender.verified && trigger.sender.id in ["U123", "U456"]`.

**`trigger.origin`** says whether Reliant itself caused the event, so a workflow can tell its own echoes from other people's. Every
key is always present (an unattributed event reads `caused_by_reliant: false, confidence: 'none'`), in `filter`, `inputs` and `prompt`:

| Key | Meaning |
|-----|---------|
| `caused_by_reliant` | Reliant wrote the thing the event reports (any confidence) |
| `confidence` | `exact` (the event's own object id is one a run's write produced), `intent` (matches a recent write on target and content), `app` (from Reliant's GitHub App, run unknown), `claimed` (a `Reliant-Run:` commit trailer; anyone can write one, so it is only ever a reason to ignore), `none` |
| `run_id`, `workflow`, `trigger_name` | The run that wrote, its workflow, and the declared trigger that launched it. Empty unless it is YOUR run |
| `same_workflow` | That run is a run of this trigger's own workflow: the event is its own echo |
| `actor` | Who the write was made as: `app`, `owner` or `unknown` |
| `lineage_depth` | How many event-driven hops led to the run that wrote (a chain stops at 5) |

A trigger's **`origin_filter`** is a raw CEL bool evaluated BEFORE `filter`. Its default ignores a workflow's own echoes:
`!(trigger.origin.same_workflow && trigger.origin.confidence in ['exact', 'intent'])`, so a reviewer does not re-run on its own
review. `origin_filter: "true"` receives everything; `!trigger.origin.same_workflow || trigger.origin.trigger_name == 'responder-push'`
allows one deliberate re-trigger. A skipped event is recorded with the reason (`origin_filter rejected: caused by run X of this
workflow`). The lineage depth cap (5) is a hard loop guard no expression overrides, so two workflows that re-trigger each other
still stop. Only writes made through integration actions (`type: action` nodes, integration tools) are attributed
exactly; an agent's own shell `gh`/`git` writes look like the person who owns the machine.

**The loop for an integration trigger:** `search_integrations(kind: trigger)` → `get_integration_schema`
(its events, the `match` attributes, the payload schema, and a ready `triggers:` block) → write it with
`edit_workflow` (validation reports bad cron, filters, inputs, events and match keys like any other error) →
test its filter and inputs with `cel_eval` and its graph with `run_scenario` → tell the user the workflow is ready (below).

**When the workflow is ready**, say it is saved and validated. If asked to activate it in an interactive chat,
use create_trigger_activation with its declared trigger name, typed params, project_id, explicit machine_id (or
no_machine) and connection_id. The approval card shows the resolved workflow, identity, machine and privileges.
Use update_trigger_activation to re-point an existing activation, enable_trigger to resume, and pause_trigger
to pause. Never claim success before approval and a successful tool result. list_triggers shows the result and health.
The app remains an alternative: **Workflows → Library → open the workflow → Enable** turns on all declared triggers.
Unattended runs are refused: they cannot grant themselves standing privileges.

**Editing the YAML changes every activation.** An activation reads its declaration from the workflow
each time it fires, so a new cron, filter or input mapping applies on the next event. Renaming or
removing a declaration, or changing its source kind or integration, leaves its activations BROKEN:
they fire nothing, report why in `list_triggers`, and recover when the declaration is restored (or are
enabled again by the user). Runs a trigger starts are unattended — nobody answers questions or approvals.

**Who the run writes as.** New GitHub integration activations default to `acting_identity: app` (GitHub comments,
reviews and pull requests appear as the Reliant bot, `reliant-labs[bot]`). Other sources, including schedules,
webhooks and workflow events, still default to `owner` (writes appear as the user); a scheduled GitHub writer needs an
explicit app choice. Existing activations retain their stored identity, and explicit owner choices stay owner.
Only GitHub supports app identity; do not promise a bot identity for another integration. It is the
ACTIVATION's choice, made in the app, not the workflow's and not yours: nothing in the YAML, an `action` node's `with:` or a prompt can
switch it, and a person typing in the run's chat acts as themselves. It covers every integration action the run takes, an
`action` node and an agent's integration tools alike, and only the writes: reads use the owner's access. Everything the bot
authors ends with a footer naming the automation and its owner, so do not write your own signature. The bot only writes to a
repository the owner can push to, scoped to that one repository; if the owner cannot push, the Reliant GitHub App is not
installed there, the App lacks a permission, the deployment has no App key, or minting fails, the write FAILS with the reason (it never falls back to the owner's name or to a saved connection; a write that names a saved connection on a bot run fails too, and a git_publish push is refused the same way),
so a workflow that must keep going should handle the action's error output. When you hand a workflow to the user, say that
new GitHub triggers default to the bot; scheduled GitHub writers need the user to choose it explicitly. A deployment
without a control plane refuses the bot identity on save; the user must configure the App or explicitly choose owner. Comments written by Reliant's own bot never fire
`issue_comment.created` or `pull_request_review_comment.created`, so a run that comments cannot trigger itself; other bots' comments do.

### When a run fails: `on_failure:`

A workflow-level `on_failure:` is a finally-style handler: an inline graph (`entry`, `nodes`, `edges`, or `sequence:`) that runs ONCE when a run
FAILS, so the people waiting on it are told instead of finding a silent dead end.

```yaml
on_failure:
  sequence:
    - id: note
      type: save_message
      args:
        role: assistant
        display_style: warning
        content: "Failed at {{ failure.node_id }} ({{ failure.error_code }}): {{ failure.error }}. prep: {{ nodes.?prep.n.orValue('-') }}."
```

- **It runs when** a node's error was not handled (a config that will not evaluate, a sub-workflow that failed, an agent that never produced its response and ended the step), or the graph reached a node that declares `outcome: failure`. It does not run when the run completes, pauses (a step that exhausts its retries pauses the run, it does not fail it), or is cancelled.
- **`failure`** exists only inside the handler: `failure.node_id`, `failure.node_path` (dotted, through loops and sub-workflows), `failure.error` (one line), `failure.error_code` (`node_error`, `outcome` or `panic`), `failure.attempt` (the loop iteration, 1 if none). `inputs` are the run's. `nodes` are the outputs the run had produced when it failed, as a read-only copy.
- **Guard every `nodes.<main node>` read** with `has()` or `.?`: whichever node failed, the ones after it never ran, and validation rejects an unguarded read. A handler node may not reuse a main-graph node id, and `on_failure` itself is a reserved id.
- **It cannot mask the failure.** The run still ends FAILED. An error inside the handler (or a handler that outlives its 5-minute budget) is logged and recorded, never handled and never recursive; it has no `outputs`, `inputs` or nested `on_failure`. It can contain actions, `save_message`, sub-workflows and agents, but no `approval` or `ask_question`: nobody is answering.
- **Make a failure visible.** A run that ends on a path labelled "blocked", "punted" or "no result" but finishes normally is a success to the platform: it is auto-archived and never reaches the handler. Route those paths to a terminal node with `outcome: failure`.
- **Tell a person once.** The Autopilot pack's `builtin://autopilot-failure-note` posts one comment per pull request head commit (or issue): it searches the thread for its own marker first, posts nothing if the thread cannot be read, and writes as whoever the run acts as.
- **Test it.** A scenario names the handler's nodes `on_failure.<node>` in `events`, `reached`, `completed` and `messages`, and asserts `expect.on_failure: {ran, node_id, node_path, error_code, attempt, error_contains, handler_failed}`.

## Important rules

### Parameters
- Set model as a param via a **tag** (flagship, moderate, cheap, etc.) — not a hardcoded model ID
- **NEVER** assume which models exist. Your training data may be stale, and users have different API keys.
- A workflow always runs in the context of a user thread. You never need to create input to consume the user's request.

### Agents and LLMs
- `CallLLM` is a single LLM call — it doesn't execute tools or loop
- Common patterns:
  - **Augmented agents**: Do additional work inside the agentic loop (e.g., auditing)
  - **Parallelization**: Multiple edges from the same source (not switch/case logic)
  - **Combining agents**: Multiple specialized agents with distinct tools/instructions
  - **Structured output**: Use response tools to produce output for conditional routing
- Combine patterns to create powerful workflows

### Loops
- `while` is **do-while**: the body always runs at least once
- `iter.iteration` is 0-indexed inside the loop body
- `outputs.*` in the `while` condition references the **current iteration's** outputs

### Response tools
- Force structured LLM output for routing/classification
- `builtin://agent` returns when no tool calls remain (use `ask: true` to prompt for user feedback first)
- `builtin://structured-agent` returns when the response tool is called (use `ask: true` to prompt for user feedback first)
- Access structured output via `nodes.<execute_tools_id>.response_data.<tool_name>`

### Conditions on nodes
- Skipped nodes forward execution to the next edge target
- You **cannot** access outputs of skipped nodes
- Join nodes handle skipped inputs correctly

## Available tools

Granted by `tag:workflow` — no `load_tool` needed in a workflow-building chat. All take the
required `id` (UUID/slug/name) described above except `create_workflow`, which mints one, and
the discovery/reference tools (`list_workflows`, `list_presets`, `get_preset`, `get_schema`, `get_cel_reference`, `cel_eval`, `get_workflow_suggestions`).

| Tool | Purpose |
|------|---------|
| `create_workflow` | Create a new workflow draft (returns id, name, slug) |
| `get_workflow` | Read current workflow content |
| `edit_workflow` | Make targeted edits to a workflow |
| `write_workflow` | Full workflow rewrite |
| `get_schema` | Get full field documentation for any node/input/shared type |
| `get_cel_reference` | Authoritative CEL reference (namespaces, functions, types) |
| `cel_eval` | Evaluate a CEL expression or template against a sample trigger (generated from an integration event's declared payload), inputs and node outputs: value and type, or a precise error. Test a filter in one call |
| `search_integrations` | Find an integration action ref (GitHub, Slack, HTTP, ...) for a `type: action` node's `uses:` |
| `get_integration_schema` | Read one integration action's params (`with:`) and output (`nodes.<id>.data`) schemas |
| `list_triggers` | List the triggers the user has enabled (optionally one workflow's) with health; read-only. BROKEN means the declaration it activates is gone or changed |
| `fire_trigger` | Manually fire an existing activation once, including a paused one; never creates, enables or changes it. Orchestrator-tier; returns an asynchronous fire_workflow_id, not a successful run |
| `list_trigger_events` | Read firing history without source payloads. Match fire_workflow_id exactly, then inspect the launched chat_id with get_run; paginate with next_page_token |
| `list_workflows` | Browse existing workflows for examples and patterns |
| `list_presets` | Discover available agent presets |
| `get_preset` | View a preset's full configuration |
| `write_scenario` | Create a test scenario |
| `run_scenario` | Execute a scenario and check results |
| `list_scenarios` | List existing scenarios for a workflow |
| `view_scenario` | Read a scenario's content |
| `edit_scenario` | Modify an existing scenario |
| `delete_scenario` | Remove a scenario |
| `get_workflow_suggestions` | Get AI-powered suggestions for workflow improvements |

Outside a workflow-building chat, these tools aren't preloaded, but `load_tool(name="tag:workflow")`
is always available (it's in `tag:coding:default`) and loads the full set on demand. Every tool that targets an
existing workflow requires its `id` (returned by `create_workflow`, or listed by `list_workflows`).

## CEL Reference

> Auto-generated from `internal/workflow/reference`. Use `get_cel_reference` for the complete authoritative reference.

### Syntax rules

| Context | Syntax | Example |
|---------|--------|--------|
| `condition`, `while` | Pure CEL (no `{{}}`) | `condition: "nodes.llm.stop_reason == 'tool_use'"` |
| All other fields | Template interpolation `{{}}` | `model: "{{inputs.model}}"` |

### Namespaces

| Namespace | Description | Fields |
|-----------|-------------|--------|
| `failure.*` | What failed, inside a workflow's on_failure handler only (failure.node_id, failure.node_path, failure.error, failure.error_code, failure.attempt). Undeclared everywhere else | workflow-specific |
| `inputs.*` | Workflow input values passed at invocation | workflow-specific |
| `iter.*` | Loop iteration context (iteration count, first/last flags) | `iteration` |
| `nodes.*` | Output from completed nodes (nodes.<id>.<field>) | workflow-specific |
| `output.*` | Current activity output (for save_message context) | workflow-specific |
| `outputs.*` | Loop iteration outputs for while condition evaluation | workflow-specific |
| `thread.*` | Current thread context (token_count, message_count) | workflow-specific |
| `trigger.*` | The event that started this run, fixed at launch (trigger.kind, trigger.name, trigger.scheduled_for, trigger.payload.<x>, trigger.sender.{kind,id,display_name,verified}, trigger.origin.{caused_by_reliant,confidence,run_id,workflow,trigger_name,same_workflow,actor,lineage_depth}). Interactive chats have kind chat.start. A trigger's filter, inputs and prompt can also read trigger.activation.params.<input>, trigger.activation.id and trigger.activation.owner.{user_id,github_id}; nodes cannot | workflow-specific |
| `workflow.*` | Workflow execution context (id, name, path, etc.) | `id`, `name`, `run_id`, `path`, `worktree_path`, `branch`, `mode` |

#### `iter` fields

| Field | Type | Description |
|-------|------|-------------|
| `iter.iteration` | `int` | Current loop iteration (0-indexed) |

#### `workflow` fields

| Field | Type | Description |
|-------|------|-------------|
| `workflow.branch` | `string` | That worktree's git branch (empty in the project's main checkout, whose branch is not tracked; test with has(workflow.branch)) |
| `workflow.id` | `string` | Workflow execution ID (unique per run) |
| `workflow.mode` | `string` | Execution mode (auto, manual, plan), from inputs.mode |
| `workflow.name` | `string` | Workflow definition name |
| `workflow.path` | `string` | Absolute working directory of the current scope: the chat's worktree or project, or a sub-workflow's project.path. Reading it in a run with no project directory is an error, not ""; test with has(workflow.path) |
| `workflow.run_id` | `string` | Workflow run ID (Temporal run ID) |
| `workflow.worktree_path` | `string` | The chat's worktree, when the current scope runs in it (empty in the project's main checkout) |

### Key functions

| Function | Description | Example |
|----------|-------------|--------|
| `base64.decode(string) -> bytes` | Decode base64 to bytes; wrap in string() for text | `string(base64.decode(nodes.file.data.content))` |
| `base64.encode(bytes) -> string` | Base64-encode bytes | `base64.encode(bytes(inputs.text))` |
| `cel.bind(name, value, body) -> dyn` | Name an intermediate value once and use it in body | `cel.bind(held, inputs.leases.filter(l, l.state == 'held'), size(held) > 0)` |
| `coalesce(dyn, dyn) -> dyn` | Return first non-null/non-empty argument | `coalesce(inputs.name, "default")` |
| `list.distinct() -> list` | Remove duplicates, keeping the first of each | `nodes.files.names.distinct()` |
| `duration(string) -> duration` | Parse a duration such as '45m' or '90s'; timestamp + duration is a timestamp, and .getMinutes()/.getSeconds() read one | `string(timestamp(now()) + duration('45m'))` |
| `list.flatten() -> list` | Flatten a list of lists one level | `[[1], [2, 3]].flatten()` |
| `string.format(list) -> string` | Format a string with positional arguments | `"Hello %s, you have %d items".format([inputs.name, inputs.count])` |
| `getOrDefault(map, key, default) -> dyn` | Safely access a map key with a fallback default value | `getOrDefault(inputs, "mode", "auto")` |
| `list.join(string) -> string` | Join list elements with separator | `["a", "b"].join(", ")` |
| `json.decode(string) -> optional(dyn)` | Parse JSON without failing: optional.none() when the text is empty or not exactly one JSON value. Integers stay integers. Use parseJson when bad input should fail the step | `json.decode(nodes.start.stdout).?status.orValue('error')` |
| `lists.range(int) -> list(int)` | [0, 1, ..., n-1], at most 100000 long | `lists.range(3)` |
| `string.lowerAscii() -> string` | Lower-case the ASCII letters of a string (also upperAscii, trim, indexOf, substring, padLeft, padRight) | `inputs.service.lowerAscii()` |
| `string.matches(regex) -> bool` | True when the string matches the RE2 regular expression anywhere; anchor with ^ and $ to validate a whole value | `inputs.id.matches('^[1-9][0-9]{0,9}$')` |
| `math.abs(number) -> number` | Absolute value (also math.ceil, math.floor, math.round, math.sign) | `math.abs(-3)` |
| `math.greatest(a, b, ...) \| math.greatest(list) -> number` | The largest of the arguments or list elements | `math.greatest(inputs.adds, inputs.dels)` |
| `math.least(a, b, ...) \| math.least(list) -> number` | The smallest of the arguments or list elements | `math.least(inputs.timeouts)` |
| `merge(map, map) -> map` | Shallow-merge two maps; keys in the second map win (e.g. pin one setting on a model value) | `merge(inputs.model, {"thinking_level": "high"})` |
| `now() -> string` | Return current time as RFC3339 string | `now()` |
| `parseDuration(string) -> double` | Parse a Go duration string and return seconds as a number | `parseDuration("5m") == 300.0` |
| `parseJson(string) -> dyn` | Parse a JSON string into a dynamic value | `parseJson(nodes.run.stdout)` |
| `regex.extract(string, regex) -> optional(string)` | The first match (or its one capture group), or optional.none() when there is none | `regex.extract(inputs.branch, 'issue-(\\d+)').orValue('')` |
| `regex.extractAll(string, regex) -> list(string)` | Every match of the regex | `regex.extractAll(inputs.text, '#\\d+')` |
| `regex.replace(string, regex, replacement) -> string` | Replace every match; \1 refers to a capture group | `regex.replace(inputs.title, '\\s+', ' ')` |
| `string.replace(string, string) -> string` | Replace all occurrences of old with new | `nodes.llm.response_text.replace("\n", " ")` |
| `sets.contains(list, list) -> bool` | True when the first list contains every element of the second | `sets.contains(inputs.labels, ['agent:ready'])` |
| `sets.equivalent(list, list) -> bool` | True when both lists hold the same elements, ignoring order and repeats | `sets.equivalent(inputs.a, inputs.b)` |
| `sets.intersects(list, list) -> bool` | True when the lists share at least one element | `sets.intersects(inputs.labels, ['skip-review', 'wip'])` |
| `list.slice(start, end) -> list` | The elements from start up to (not including) end | `inputs.items.slice(0, 5)` |
| `list.sort() -> list` | Sort a list of numbers, strings, timestamps or durations | `[3, 1, 2].sort()` |
| `list.sortBy(x, key) -> list` | Sort a list by a key computed per element (stable) | `inputs.leases.sortBy(l, l.created_at)` |
| `spawn(string, list) -> string` | Generate a spawn directive for a child workflow with presets | `spawn("builtin://agent", ["general", "researcher"])` |
| `string.split(string) -> list(string)` | Split string by separator | `nodes.run.stdout.split("\n")` |
| `timestamp(string \| int) -> timestamp` | Parse an RFC3339 string (or epoch seconds); add a duration, subtract two timestamps for a duration, compare with < > | `timestamp(inputs.expires) > timestamp(now())` |
| `toJson(dyn) -> string` | Serialize a value to a JSON string | `toJson(nodes.llm.tool_calls)` |
| `string.trimPrefix(string) -> string` | Remove prefix from string | `nodes.run.stdout.trimPrefix("Error: ")` |
| `string.trimSuffix(string) -> string` | Remove suffix from string | `nodes.run.stdout.trimSuffix("\n")` |
| `untrustedJson(dyn[, int]) -> string` | Outside data as compact JSON that is safe to quote in a prompt: every string treated as untrustedText, keys sorted, lists and nesting bounded, and <, > and & escaped so a value cannot close the tag it sits in | `untrustedJson(inputs.alerts[0].labels)` |
| `untrustedText(string[, int]) -> string` | Text an outside party can write (an error message, an alert label, a webhook body) as one plain line: control and invisible characters dropped, backticks neutralized, at most 400 runes (or the given length). Use it on every such value before it goes into a prompt | `untrustedText(trigger.payload.body.title, 120)` |

### Common patterns

```yaml
# Check if LLM wants to use tools
condition: "nodes.llm.stop_reason == 'tool_use'"

# Agentic loop — keep going while the model has more to do. stop_reason is a
# closed set: tool_use | incomplete | truncated | interrupted | refused | error | done
while: "outputs.stop_reason in ['tool_use', 'incomplete', 'truncated', 'interrupted'] && iter.iteration < 50"

# Dynamic model from input
model: "{{inputs.model}}"

# Safe field access with has()
condition: "has(nodes.classify) && nodes.classify.response_data.route.category == 'urgent'"

# Safe navigation with ?.
condition: "nodes.result.?error_message == null"

# Parse JSON from command output in an interpolated field
result: "{{parseJson(nodes.run.stdout)}}"

# Coalesce to provide defaults
name: "{{coalesce(inputs.name, 'anonymous')}}"
```

### Null safety

Always guard optional field access:

```yaml
# Good — use has()
condition: "has(nodes.check) && nodes.check.exit_code == 0"

# Good — safe navigation
condition: "nodes.result.?data != null"

# Bad — errors if node was skipped or field doesn't exist
condition: "nodes.check.exit_code == 0"
```

## Builtin Workflows

> Auto-generated from `internal/workflow/builtin/*.yaml`. Reference via `builtin://<name>` in workflow nodes.

| Name | Description |
|------|-------------|
| `agent` | Standard interactive agent. Loops while LLM returns tool calls, returns when LLM responds with no tool calls. Will always start on a thread that is seeded with the user's message. User interaction occurs outside of the workflow. Uses inline save_message for frontend activity association. |
| `autopilot-agent` | The one agent loop every Autopilot workflow runs. It is a building block, not something to start. Structural limits: - The agent is offered EXACTLY the tools its caller names in `tools`, and nothing is loadable (loadable_tools: []), so it can never reach pr_review_create, issue_update, git_publish or any tool the caller did not name. - `tool_params` binds tool parameters the model can neither see nor override. The machine workflows bind the shell to `credentials: none`. - Tier `mutating`; no ask or approval nodes, so an unattended run never waits on a human. It loops until the response tool is called, at most max_turns turns. |
| `autopilot-ensure-labels` | Makes sure the labels an Autopilot workflow uses exist in a repository, with a colour and a description. Building block of the Autopilot pack; not meant to be started on its own. |
| `autopilot-failure-note` | Tells the people on a pull request or issue that an Autopilot run could not finish, once. Building block of the Autopilot pack's on_failure handlers; not meant to be started on its own. |
| `autopilot-issue-worker` | Works an issue that someone you trust opted in, by labelling it agent:ready or by mentioning @reliant in a comment. It takes a lease on the issue, makes a worktree from the default branch, lets a restricted agent (a shell with no credentials, nothing loadable) write a failing test and then the fix, runs the repo's fast gate, and then git_publish pushes an autopilot/ branch (never the default branch, never a force) and a PR is opened that closes the issue. If it cannot be done the issue is labelled agent:punted or agent:blocked with exactly what is missing. Never merges, never approves. Part of the Autopilot pack. |
| `autopilot-lease-acquire` | The lease Autopilot's workflows take on an issue or pull request so two runs do not work it at once. Built-in building block of the Autopilot pack; not meant to be started on its own. |
| `autopilot-lease-release` | Releases the lease autopilot-lease-acquire took, then looks at what arrived while it was held. Building block of the Autopilot pack; not meant to be started on its own. |
| `autopilot-oncall` | Your on-call first responder. Every alert that pages, and every new, regressed or spiking error, becomes a GitHub issue in the repository its service maps to (see service_repos), investigated read-only and written up with severity, evidence, suspected cause and a proposed fix. |
| `autopilot-pr-review` | An independent reviewer for every non-draft pull request in the repositories you enable it for. It reads the diff and the surrounding code through GitHub, then posts ONE review: a COMMENT, or REQUEST_CHANGES when it found a defect that must be fixed before merge. It NEVER approves and never merges: the review event is computed in one place (node `decide`) by an expression that can only yield those two values, and the reviewer holds no tool that can post a review itself. |
| `autopilot-review-responder` | Addresses review feedback on pull requests an agent wrote (labelled agent:authored, or agent:respond for any PR you want it to look after). It takes a lease on the PR, checks the PR branch out into a worktree, lets a restricted agent (a shell with no credentials, nothing loadable) change the code, runs the repo's fast gate, then git_publish pushes to THAT PR branch only (never the default branch, never a force), and the run replies to each thread, posts a round summary, releases the lease and re-checks for feedback that arrived meanwhile. It stops after max_review_rounds rounds and labels the PR agent:blocked. |
| `autopilot-security-review` | A read-only security reviewer for the pull requests in the repositories you enable it for. It threat-models the diff (authentication and authorization on new endpoints, injection, SSRF, secrets, unsafe deserialization, path traversal, dependency changes and their advisories, multi-tenant data scoping, CSRF/CORS) and posts ONE review comment: findings grouped by severity, each with file:line evidence, an exploit sketch, confirmed or suspected, and a fix. The review is always a COMMENT: it never approves, never requests changes and never merges. |
| `autopilot-set-labels` | Adds and removes labels on an issue or pull request, one atomic request per label. Building block of the Autopilot pack; not meant to be started on its own. |
| `build-workflow` | Build Workflow — create a custom Reliant workflow from a conversation. |
| `forge-migrate` | Migrate an existing codebase into a Forge project. Distinct from `migrate` (which imports Claude Code/Cursor/Codex config into Reliant) and from `forge-one-shot` (which builds a greenfield app from a conversation). |
| `forge-one-shot` | Build a production Forge app from a conversation, in ONE get-it-right loop: SCOPE → SCAFFOLD → VERIFY GREEN → FAN OUT → SYNTHESIZE → VERIFY. |
| `get-it-right` | Get It Right — for complex brownfield codebases where LLMs paper-mache code on top. The insight: sometimes you need to try and fail to truly understand the codebase. |
| `landing-page` | Build a polished landing page by chaining two get-it-right review loops and ending in a plain handoff agent that serves the page and hands the user a URL. |
| `migrate` | Guided migration workflow for importing useful configuration from Claude Code, Cursor, Codex, or Windsurf into Reliant. |
| `parallel-compete` | 3 agents implement in parallel worktrees, reviewer picks winner or synthesizes. Thread mode: new (isolated context). Each worktree is independent. Apply path: use_winner applies the winner's changes to the project with git apply, synthesize merges best parts. |
| `pitch-deck` | Generate an investor pitch deck from a company website with competitive research and founder interview. Includes parallel per-slide write+review pipeline and visual review via puppeteer screenshots + image attachments. |
| `scope-conversation` | Reusable scoping conversation sub-workflow. |
| `structured-agent` | Agent that requires structured output via response tool. Unlike builtin://agent which returns once the model is done (stop_reason), this loops until the response tool is called. If LLM responds without tools, a reminder is injected. Access output via output.response (structured data) and output.completed (boolean). |


## Node Types

> Auto-generated from `reference.ListNodeTypes()` / `reference.GetNodeType()`. Use `get_schema(name="<type>")` for full field documentation.

### Common Fields (all nodes)

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Node type (required) |
| `id` | string | Unique node ID (required) |
| `condition` | CEL | Skip if false; on join nodes: `all` or `any` |
| `thread` | ThreadConfig | Thread mode: `inherit`, `new`, `fork` |
| `timeout` | string | Override timeout (e.g., `5m`) |
| `save_message` | SaveMessageConfig | Auto-save message after completion |

### Available Types

| Type | Description |
|------|-------------|
| `action` | Run an integration action (for example an HTTP request) |
| `approval` | Pause workflow execution for user approval |
| `ask_question` | Pause workflow execution to ask the user a question |
| `call_llm` | Send a prompt to a language model and get a response |
| `compact` | Conversation context to reduce token usage |
| `compute` | Named values from CEL expressions: no model, no shell, no worker round trip. Each output is {{ expr }} (or a literal); a value that is exactly one {{ }} keeps its type. Read them as nodes.<id>.<name>. Outputs cannot read each other: chain two compute nodes when one feeds the next. A failing expression fails the node. Use it for deterministic logic (labels, sizes, expiry, parsing a node's JSON stdout) instead of a script |
| `create_worktree` | Create a git worktree for isolated development |
| `execute_tools` | Run the tool calls an upstream Call LLM step returned |
| `git_publish` | Commit a worktree's changes and push them to a feature branch. Refuses the default branch, protected branches, main, master and release/*, and any branch outside the allow list; refuses a fork head; never force-pushes. Nothing to commit is not an error (pushed is false). The credential is used for this one push and never reaches a shell, a file or the environment. Pair it with shell credentials: none |
| `invoke_tool` | Run one tool you pick, with parameters you set; no LLM involved |
| `join` | Wait for parallel branches to complete before continuing |
| `loop` | Execute a sub-workflow in a loop with conditions |
| `router` | Route to a workflow or node based on LLM classification |
| `run` | Execute a shell command |
| `save_message` | Save a message to the conversation thread |
| `workflow` | Invoke an agent or sub-workflow |


## Input Types

> Auto-generated from `reference.ListInputTypes()` / `reference.GetInputType()`. Use `get_schema(name="<type>")` for full documentation.

| Type | Description |
|------|-------------|
| `any` | Generic input accepting any JSON value |
| `array` | Generic array input |
| `attachments` | File attachment input |
| `boolean` | Boolean toggle input |
| `enum` | Dropdown with predefined values |
| `group` | Input group for organizing related inputs with preset matching |
| `integer` | Whole number input with optional min/max |
| `integration` | Value an integration defines, written type: <integration>.<type> (for example github.repository); multi makes it a list |
| `message` | Primary user message/prompt input |
| `model` | Model selector dropdown |
| `number` | Decimal number input with optional min/max |
| `object` | Structured object with JSON Schema validation |
| `preset` | Dynamic preset picker filtered by tags |
| `string` | Text string input with optional validation |
| `tools` | Tool selector input |


## Workflow Structure

> Auto-generated from `generated/docs-source/reference/workflow-schema.md`.

## Workflow

Defines a complete workflow with nodes, edges, inputs, and outputs.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | - |
| `nodes` | Node[] | No | *Use get_schema(name="<type>") for node type details (e.g. "call_llm", "router").* |
| `edges` | Edge[] | No | *See Edge type below.* |
| `description` | string | No | - |
| `inputs` | map[string]Input | No | *Use get_schema(name="<type>") for input type details (e.g. "string", "enum", "model").* |
| `outputs` | map[string]string | No | *CEL expressions mapping output names to values. Use get_cel_reference for CEL syntax.* |
| `presets` | PresetsConfig | No | - |
| `entry` | string[] | No | - |
| `api_version` | string | No | - |
| `daemon` | CelDaemonSelector | No | - |
| `resume_node` | string | No | - |
| `transition_to` | string | No | - |
| `triggers` | WorkflowTrigger[] | No | *WHEN the workflow runs: a list of {name, description, one source (schedule \| webhook \| integration \| workflow_event), filter (raw CEL over `trigger`), inputs (templates over `trigger`), prompt (template over `trigger`: the message each run starts from)}. Declaring one fires nothing; the user enables it in the app. See the workflow-builder skill's Triggers section.* |
| `title` | string | No | - |
| `hidden` | boolean | No | *A building block other workflows ref: true keeps it out of every picker and default listing while it stays resolvable by ref. With automation_only too, it is an automation's internal step and is listed nowhere. Unset means the workflow is listed.* |
| `automation_only` | boolean | No | *The Chat trigger turned off: true keeps the workflow out of chat pickers and refuses a chat start of it, so only its triggers (or a builder test run) start it. Unset means a chat can start it.* |
| `concurrency` | WorkflowConcurrency | No | *Bounds trigger-launched runs: {max_running: N} (later firings wait, FIFO), optionally waiting: latest (only the newest waiting firing per key survives; older ones are recorded skipped, "superseded", never failed) and key (a template over `trigger` grouping the firings that replace each other). See WorkflowConcurrency below.* |
| `on_failure` | Workflow | No | *A finally-style handler: an inline graph ({entry, nodes, edges}) that runs once when a run FAILS (an unhandled node error, an agent that never produced its required response, a node that declares outcome: failure). Never when the run completes, pauses or is cancelled. Its nodes read `failure.{node_id, node_path, error, error_code, attempt}`, `inputs`, and the `nodes` outputs the run produced (guard them with `.?`). The run still ends FAILED; an error in the handler is logged and never recursive.* |

## Edge

Connects a source node to destination(s) with conditional routing.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `from` | string | Yes | - |
| `cases` | EdgeCase[] | No | - |
| `default` | string[] | No | - |

## EdgeCase

Defines one conditional routing path from an edge.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `to` | string[] | No | - |
| `condition` | string | No | - |
| `label` | string | No | - |

## Scenario Testing

Test workflows by simulating LLM and tool responses without making real API calls.

> Auto-generated schema details from `generated/docs-source/reference/scenario-schema.md`.

### Scenario fields

Scenario defines a complete test case for a workflow.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Name uniquely identifies this scenario. Required. |
| `apiVersion` | string | No | ApiVersion specifies the schema version. Optional, for future compatibility. |
| `description` | string | No | Description documents what this scenario tests. |
| `events` | SimulatedEvent[] | Yes | Events lists the simulated events in execution order. Required. |
| `expect` | Expectation | No | Expect defines the expected outcome and assertions. Optional. |
| `inputs` | object | No | Inputs overrides workflow inputs for this scenario. Optional. |
| `start_at` | string | No | StartAt begins execution at a specific node instead of the entry point. Optional. |
| `state` | map[string]object | No | State pre-populates node outputs before execution. Optional. |

### Event fields

SimulatedEvent represents a single mocked event in a test scenario.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `node` | string | No | Node targets a specific node using dot-notation for qualified IDs. |
| `output` | object | No | Output is the raw mock output (mutually exclusive with Type). |
| `type` | string | No | Type specifies the event type for automatic conversion. |
| `text` | string | No | Text is the LLM response text (for type: llm_response). |
| `tool_calls` | SimToolCall[] | No | ToolCalls are tool invocations from the LLM (for type: llm_response). |
| `tool` | string | No | Tool is the tool name (for type: tool_result). |
| `tool_output` | object | No | ToolOutput is the tool execution result (for type: tool_result). |
| `is_error` | boolean | No | IsError marks a tool_result as failed (for type: tool_result). |
| `black_box` | boolean | No | BlackBox mocks a loop or workflow node AS A UNIT: its body is not |

### Expectation fields

Expectation defines what to verify after a scenario runs.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `outcome` | string | No | Outcome specifies whether the workflow should complete or error. |
| `reached` | string[] | No | Reached lists nodes that must be scheduled during the scenario (completed, skipped, or errored). |
| `not_reached` | string[] | No | NotReached lists nodes that must NOT be scheduled during the scenario. |
| `completed` | string[] | No | Completed lists nodes that must have executed successfully. |
| `skipped` | string[] | No | Skipped lists nodes that must have been skipped due to a false condition. |
| `error_contains` | string | No | ErrorContains specifies a substring that should appear in the error message. |
| `error_node` | string | No | ErrorNode specifies which node should produce the error. |
| `node_outputs` | map[string]object | No | NodeOutputs specifies expected output values for specific nodes. |
| `outputs` | object | No | Outputs specifies expected values for the workflow's declared outputs. |
| `messages` | object | No | Messages asserts on the messages the run saved to the thread, keyed by |
| `on_failure` | OnFailureExpectation | No | OnFailure asserts on the workflow's `on_failure` handler: whether it ran |

### Targeting Nodes

The `node` field on events targets specific nodes by ID:

- **Top-level nodes**: `node: "call_llm"`
- **Inner loop nodes**: `node: "loop_id.inner_node_id"` (dot-separated)
- **Inner workflow nodes**: `node: "workflow_id.inner_node_id"` (for `type: workflow` with `inline:`)
- **Nested structures**: `node: "outer.inner.node_id"`

Scenarios run on the real workflow runtime; only activities (LLM calls, tool execution, …) are mocked. Inline loops and inline workflow nodes always execute their inner nodes, evaluating conditions and tracking skipped nodes by qualified ID.
A ref-based node (`ref: builtin://agent`) is black-boxed by default: an event on the node itself (`node: "review"`) supplies its whole output. If the scenario targets qualified inner nodes (`node: "review.agent_loop.call_llm"`), the referenced workflow executes for real instead. A loop node can be black-boxed the same way with `black_box: true` — the event then supplies the loop's aggregate output (`_results`, `_iterations`, declared outputs).
Events are always keyed by node path, never by ref URL: `node: "builtin://agent"` is a validation error.

**Event matching:**
- Events with a `node` field are matched to that specific node
- Events without a `node` field are consumed sequentially in order
- Multiple events with the same node are consumed in order per-node (use for multi-iteration loops)

### Event Types

| Type | Description | Required Fields |
|------|-------------|-----------------|
| `llm_response` | Simulate a Call LLM node returning text and/or tool_calls | `text` or `tool_calls` |
| `tool_result` | Simulate an Execute Tools node returning one tool's result; add `is_error: true` for a failed call | `tool`, `tool_output` (`output` is accepted as a shorthand) |
| (none) | Any other node: `output` is the node's whole output, checked against its output schema (an approval's `status`, an ask_question's `response`, a run's `exit_code`) | `output` |

### Use the raw `output:` form, not `type:`

Every event needs either `output:` (a raw activity output map) or `type:` (a convenience wrapper
like `llm_response`/`tool_result` that gets converted into one). **Write `output:` directly.** It is
the form nearly every builtin scenario (`internal/workflow/builtin/<workflow>/scenarios/`, which CI runs) uses, and it is
the only form that exposes real activity field names — `response_data`, `thread_token_count`,
`has_feedback`, `tool_results` — which you need for routing and compaction scenarios and which the
`type:` wrapper hides.

### `completed:` proves a node ran — `reached:` does not

`reached:` passes for anything **scheduled**, including a node skipped by a false condition — per
the field doc above, it covers nodes "completed, skipped, or errored." So `reached: [handler]`
is satisfied even when `handler` never actually executed. **If a scenario is meant to prove a
branch executed, assert `completed:`, not `reached:`.** This is the single most common way a
scenario passes without testing what its author intended.

A node can legitimately land in both `completed:` and `skipped:` across one run — e.g. a
loop-body node that runs on iteration 0 and is skipped on iteration 1. Real example, from
`internal/workflow/builtin/get-it-right/scenarios/stuck_feedback_rereviews_without_reimplementing.yaml`:

```yaml
expect:
  outcome: completed
  reached:
    - attempt.implement   # scheduled at least once — true either way, proves nothing about iteration 1
  skipped:
    - attempt.implement   # the assertion that actually proves iteration 1 skipped it
```

Node state is a set of facts about the whole run, not a single verdict per node.

### Example scenario

Verbatim from `internal/workflow/builtin/agent/scenarios/compaction_at_default_threshold.yaml`, run against `builtin://agent`.
It exercises a tool call whose output crosses the compaction token threshold, so the `compact`
node runs before the loop's next iteration:

```yaml
name: compaction_at_default_threshold
description: Token count exceeds the default compaction threshold after tool execution, compact node runs
inputs:
  mode: auto
events:
  # LLM requests a tool that returns lots of data
  - node: agent_loop.call_llm
    output:
      message:
        role: assistant
        text: "Let me read a large file."
      response_text: "Let me read a large file."
      tool_calls:
        - id: call_0
          name: view
          input: '{"file_path":"/path/to/large/file.go"}'
  # Tool returns with token count above the 185000 default threshold
  - node: agent_loop.execute_tools
    output:
      message:
        role: tool
        text: ""
      tool_results:
        - tool_call_id: call_0
          content: "Very large file content..."
      thread_token_count: 200000
  # Compact node runs due to high token count
  - node: agent_loop.compact
    output:
      message:
        role: assistant
        text: "Context compacted successfully."
  # Next iteration: LLM provides final response
  - node: agent_loop.call_llm
    output:
      message:
        role: assistant
        text: "Here's the analysis of the large file."
      response_text: "Here's the analysis of the large file."
      tool_calls: []
expect:
  outcome: completed
  reached:
    - agent_loop
    - agent_loop.call_llm
    - agent_loop.execute_tools
    - agent_loop.compact
  not_reached:
    - agent_loop.approval
```

Notes on this example, all load-bearing:

- `inputs:` overrides workflow inputs for just this scenario (here, selecting `mode: auto` to skip
  the approval node).
- `not_reached:` is the primary way to prove a conditional path was actually SKIPPED, not just that
  the scenario didn't happen to visit it — assert it on every branch you didn't take.
- The loop node itself (`agent_loop`) belongs in `reached` alongside its children — a loop that never
  entered its body wouldn't reach `agent_loop.call_llm` either.
- Field names on `output:` are the real activity output fields for that node type: `call_llm` gives
  `message`, `response_text`, `tool_calls`, `token_count`; `execute_tools` gives `tool_results`,
  `response_data` (structured output keyed by response-tool name), `thread_token_count`;
  `ask_question` gives `has_feedback`. Use `get_schema(name="<node_type>")` for the full field list of
  any node type before guessing at output shapes.

### Asserting workflow outputs with `outputs:`

`outputs:` checks the workflow's declared outputs (as opposed to `node_outputs:`, which checks a
specific node's raw activity output). Keys support dotted paths into structured values, and matching
is partial — only the paths you list are checked. Verbatim from
`internal/workflow/builtin/structured-agent/scenarios/happy_path.yaml`:

```yaml
expect:
  outcome: completed
  reached:
    - agent_loop
    - agent_loop.call_llm
    - agent_loop.execute_tools
  outputs:
    response.choice: "complete"
    response.value: "Task completed successfully. The analysis shows positive results."
    completed: true
```

### Where scenario files live

A project workflow's scenarios live in `.reliant/workflows/<slug>/scenarios/`, **one scenario per
file**, where `<slug>` is the workflow's `name:` as a slug — not its file name. `blog.yaml` declaring
`name: blog-content-pipeline` keeps its scenarios in `blog-content-pipeline/scenarios/`:

```
.reliant/workflows/
  blog.yaml                          # name: blog-content-pipeline
  blog-content-pipeline/scenarios/
    drafts_from_brief.yaml           # one scenario; its name: defaults to the file stem
```

The app indexes exactly this layout, and `reliant workflow scenario run` /
`reliant workflow scenario list` read it the same way, so a scenario the CLI runs is one the app lists.
A file holding several scenarios (`---`-separated, or a `scenarios:` list) is an error: the app
identifies a project scenario by its file. The retired layouts — `scenarios/<file>/` and
`<file>_scenarios.yaml` — are an error naming where each file belongs.

The same rule addresses sub-workflows: `ref: project://blog-content-pipeline` names the workflow by its
`name:`, never its file name, in a run, in a scenario and in `reliant workflow validate` alike.
`validate` follows every `project://` ref transitively and reports a broken one with the whole chain
(`a.yaml → project://b → project://c: no project workflow is named "c"`) — and a ref cycle as an error.

The builtin workflows keep their scenarios in this same layout
(`internal/workflow/builtin/<workflow>/scenarios/`), embedded in the binary;
`reliant workflow scenario run --include-builtins` runs them alongside the project's.

### Reading `run_scenario` output

`run_scenario` reports, in order:

- **Status and outcome** — `✓ PASSED`/`✗ FAILED`/`⚠ ERROR`, plus actual vs. expected outcome when they
  differ.
- **`completed:` and, if any, `skipped:` and `errored:`** — every node broken out by its actual
  execution state, not merged into one "reached" list. A node showing under `skipped:` when you
  expected it to run, or absent from `completed:` entirely, tells you which fix to make: "never
  scheduled" means a routing/edge problem, "skipped" means the node's own condition was false.
- **Error details** — for a workflow-level error, the failing node, step, message, and (if the failure
  was a condition evaluation) the CEL expression itself.
- **Warnings**, under "Warnings — this run may not have tested what you think" — printed even on a
  PASS. See the next section; do not skip a warning just because the run went green.
- **Failed expectations** — on a FAIL, each mismatch (e.g. a node in `not_reached` was actually
  scheduled) annotated with that node's actual state, e.g. `[handle_question was skipped]`, so you
  don't have to cross-reference the completed/skipped lists yourself.
- **Workflow outputs and node outputs** — the actual values, truncated, so you can see what to assert
  against without a second run.

### Warnings: a PASS can still test nothing

Two warnings print even when the scenario passes, because a passing assertion can coexist with a run
that never exercised what its name claims to test:

- **Black-boxed sub-workflow** — `type: workflow` with a `ref:` (not `inline:`) executes as one
  opaque unit unless the scenario targets its internals with qualified node ids. If nothing in
  `events:` or `expect:` uses `<node_id>.<inner_id>`, the sub-workflow's output came straight from
  your scenario's mock and none of its own logic ran. Fix: mock its internals directly, e.g.
  `node: research.search`, or accept that this scenario only tests the caller's wiring and say so
  in the description.
- **Unmocked node router defaulted to `candidates[0]`** — a `type: router` node in node-routing mode
  (`router.nodes` set) that never receives a mocked `selected_node` silently falls back to its
  first candidate. A scenario can pass while asserting nothing about which branch was chosen. **If
  you are testing routing, you must mock the router explicitly** — otherwise the scenario proves
  nothing about the routing decision, no matter what its name says. Fix, verbatim from
  `internal/workflow/builtin/pitch-deck/scenarios/start_from_research.yaml`:

  ```yaml
  events:
    - node: classify
      output:
        selected_node: research_competitors
        reasoning: "Website analysis already exists — run the research phase."
  ```

  A corpus survey found 96 of 159 scenarios (60%) black-box at least one sub-workflow, and 27 (17%)
  have a silently-defaulted router — including scenarios whose names (e.g.
  `implement_only` or `plan_only`) assert a routing outcome `classify` never actually made.
  Don't let this happen to your scenario: if the point is "task X gets classified as Y," mock `classify`.

### Responding to a black-box warning: three options, pick deliberately

You have three ways to respond, and the warning itself cannot tell you which is right — that judgment
call is why the flag exists:

1. **Make it transparent.** Add an event (or an `expect:` reference) targeting a node STRICTLY
   INSIDE the ref with a qualified id, e.g. `node: research.search`. This is the right move whenever
   the sub-workflow's own logic — its routing, its loop exit condition, an expression evaluated inside
   it — is part of what this scenario is supposed to prove.
2. **Declare it intentional with `black_box: true`.** Add the flag to the event targeting the node
   itself. This is the right move when the sub-workflow is genuinely not what this scenario tests —
   e.g. mocking `builtin://agent` to test the pipeline stages around it — and you want the aggregate
   output without paying for a full simulated agent loop.
3. **Leave the warning.** If the scenario cannot reasonably be converted (e.g. it predates this
   convention and reworking it is out of scope), leaving the warning stand is better than converting it
   incorrectly. Do not silence it with `black_box: true` just to make the warning go away — see below.

`black_box: true` is a claim about INTENT, not a way to quiet the output. Setting it does not
change what ran; it tells the tool you already knew the body wouldn't run and that's what you meant.
The node genuinely does not execute, so anything inside it — every inner condition, loop, and
expression — is untested by this scenario, same as before you added the flag. Reach for it only when
you have actually decided the body is out of scope, never as a reflex to a warning you have not read.

Concrete precedent for why the distinction matters: `parallel-compete.yaml`'s parallel loop body
referenced `iter.item.num`, which never resolved at runtime — the loop's own iteration binding was
broken. No scenario noticed, because every one of them black-boxed the loop as a unit (mocking its
aggregate `_results`/`_completed` output) instead of letting the body execute, so the failing
expression was never even compiled. The bug shipped to production and stayed invisible in a fully
green corpus. Loop bodies now execute by default for exactly this reason — this is the one case where
opting back in with `black_box: true` needs a real reason, not a reflex.

Real example, verbatim from `internal/workflow/builtin/pitch-deck/scenarios/start_from_research.yaml`,
declaring a parallel loop black-box deliberately because the scenario tests the caller's aggregation of
per-slide results, not the per-slide pipeline itself:

```yaml
- node: slide_pipeline
  # Deliberate black box: this scenario mocks the parallel loop's AGGREGATE
  # result (_results/_completed) rather than exercising the per-slide body.
  # Loop bodies execute by default; this opts out explicitly.
  black_box: true
  output:
    _completed: 1
    _failed: 0
    _parallel: true
    _results:
      "01-hook.md":
        response_text: "Wrote hook slide."
```

The same flag applies to a ref-mode `type: workflow` node: set `black_box: true` on the event
targeting the node itself (not an internal qualified id) to declare its opacity intentional and
silence the warning for that node.

### Best practices

- **Aim for 3+ scenarios per workflow** — cover the happy path, error cases, and edge cases
- **`outcome:` accepts three values** — `completed`, `error` (workflow terminated with an error;
  pair with `error_contains`/`error_node`), and `failed` (e.g. validation failure, distinct from a
  runtime error)
- **Use `completed:` to prove a branch ran, not `reached:`** — see above; `reached:` also passes
  for skipped nodes
- **Test routing logic by mocking the router** — `reached:`/`completed:` on the destination node
  proves nothing if the router itself defaulted; read the warnings section
- **Test loop termination** — verify loops exit correctly (both via `while` condition and max iterations)
- **Test skipped nodes with `skipped:`**, not `not_reached:` — a node can be scheduled and skipped
  (`reached`/`skipped`) or never scheduled at all (`not_reached`); they are different failure modes
- **Use `start_at`** — to test specific sections of complex workflows without simulating the entire flow
- **Use `state`** — to pre-populate node outputs when testing downstream logic
- **Name scenarios descriptively** — e.g., `error_handling_api_failure` not `test_3`
- **Try to break it** — simulate unexpected LLM responses, tool failures, and edge cases. Finding bugs in scenarios is much cheaper than finding them during a real 1-hour workflow run.

## References

- Use `get_cel_reference` for the complete, auto-generated CEL reference
- Use `get_schema(name="<type>")` for full field docs on any node type, input type, or shared type
- Use `list_workflows` to find real-world examples and established patterns
