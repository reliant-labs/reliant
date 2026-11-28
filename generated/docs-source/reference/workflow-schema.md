<!--
=============================================================================
GENERATED FILE - DO NOT EDIT DIRECTLY

Source: proto messages V2Workflow, V2Edge, V2EdgeCase in workflow_v2.proto
Generator: tools/docgen/schema
Regenerate: task generate:docs:schema
=============================================================================
-->

# Workflow Schema Reference

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
| `daemon` | CelDaemonSelector | No | *Which machine runs the workflow's tools: a kind (`local`, `cloud`, `any`), a machine name, a DaemonSelector, or a CEL template producing either. A node's own `daemon:` wins over this, and this wins over the chat's or automation's machine. `{pool: <name>}` places the run once on one member of a machine pool instead of pinning one machine. See DaemonSelector below.* |
| `resume_node` | string | No | - |
| `transition_to` | string | No | - |
| `triggers` | WorkflowTrigger[] | No | *WHEN the workflow runs: a list of {name, description, one source (schedule \| webhook \| integration \| workflow_event), filter (raw CEL over `trigger`), inputs (templates over `trigger`), prompt (template over `trigger`: the message each run starts from)}. Declaring one fires nothing; the user enables it in the app. See the workflow-builder skill's Triggers section.* |
| `title` | string | No | - |
| `hidden` | boolean | No | *A building block other workflows ref: true keeps it out of every picker and default listing while it stays resolvable by ref. With automation_only too, it is an automation's internal step and is listed nowhere. Unset means the workflow is listed.* |
| `automation_only` | boolean | No | *The Chat trigger turned off: true keeps the workflow out of chat pickers and refuses a chat start of it. Its triggers start it, and so does a deliberate start by hand: Run now, `reliant workflow run`, an agent's start_run, or a builder test run. Unset means a chat can start it.* |
| `concurrency` | WorkflowConcurrency | No | *Bounds trigger-launched runs: {max_running: N} (later firings wait, FIFO), {per_member: K} (K per machine of the trigger's pool), {max_running_per_key: N, key: ...} (N per key, e.g. one run per pull request), or a combination, optionally waiting: latest (only the newest waiting firing per key survives; older ones are recorded skipped, "superseded", never failed), key (a template over `trigger` grouping firings) and priority (a CEL integer over `trigger`: waiting firings run highest first). See WorkflowConcurrency below.* |
| `on_failure` | Workflow | No | *A finally-style handler: an inline graph ({entry, nodes, edges}) that runs once when a run FAILS (an unhandled node error, an agent that never produced its required response, a node that declares outcome: failure). Never when the run completes, pauses or is cancelled. Its nodes read `failure.{node_id, node_path, error, error_code, attempt}`, `inputs`, and the `nodes` outputs the run produced (guard them with `.?`). The run still ends FAILED; an error in the handler is logged and never recursive. To release something however the run ends, cancel included, use `finally`.* |
| `finally` | Workflow | No | *A handler for however a run ends: an inline graph ({entry, nodes, edges}) that runs once when a run completes, fails (after on_failure) or is cancelled. Never when it pauses or hands off to a successor run. Its nodes read `run.status` ('completed' \| 'failed' \| 'cancelled'), `failure.*` (empty unless the run failed), `inputs`, and the `nodes` outputs the run produced (guard them with `.?`). It cannot change how the run ended; an error in it is logged and never recursive. Release leases, claims and labels here.* |
| `requires` | WorkflowRequirements | No | *What a run needs from outside its definition, checked after its machine is up and before its first node: {tools, integrations, repos}. A run missing anything fails at once, naming every missing item with its fix. See WorkflowRequirements below.* |
| `secrets` | map[string]SecretDeclaration | No | - |
| `permissions` | string[] | No | - |
| `attention` | WorkflowAttention | No | *Explicit human-attention incident correlation for root unattended launch runs, independent of lifecycle and outcome. Absent preserves legacy behavior. See WorkflowAttention below.* |
| `check` | WorkflowCheck | No | *A GitHub check run the engine owns, one per run an event trigger fires, written as the Reliant App: queued when the firing is recorded, in_progress when the first node starts, and concluded on every ending (success, failure, cancelled, timed_out; never neutral or skipped). `check: <name>` or {name, repository, head_sha, output, timeout}. See WorkflowCheck below.* |
| `observe` | NodeObserve | No | *The default observe block for every node of this graph ({attributes, events, metrics}, templates over each node's `output` and `node`), merged with a node's own: attributes by key and metrics by name (the node wins), events concatenated. Not inherited by sub-workflows. See the nodes reference, Observability. This release reads and validates it and records nothing yet.* |
| `timeout` | string | No | *Bounds a run, as a literal Go duration more than zero and at most 168h (7 days), e.g. `timeout: 3h`, counted from the run's start and across continue-as-new; it keeps counting while the run is paused or waits for an approval, a question, a machine or capacity. When it passes the run is stopped like a user stop and ends FAILED with "run timeout <d> exceeded" (failure.error_code `run_timeout`); on_failure and finally still run, outside it. Only the run's root workflow's applies. Unset: no bound.* |

---

## Edge

Connects a source node to destination(s) with conditional routing.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `from` | string | Yes | - |
| `cases` | EdgeCase[] | No | - |
| `default` | string[] | No | - |

---

## EdgeCase

Defines one conditional routing path from an edge.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `to` | string[] | No | - |
| `condition` | string | No | - |
| `label` | string | No | - |

---

## WorkflowConcurrency

A trigger admission policy (`concurrency:`), not a worker task limit. Pooled per owner, project and workflow; manual trigger fires participate, chat starts do not. It bounds runs that are running: a run waiting on a provider holds no slot until it resumes. Example, one release at a time with only the newest waiting merge per repository: `concurrency: {max_running: 1, waiting: latest, key: '{{ trigger.payload.attributes.repository }}'}`. Another, at most six reviews at once and one per pull request, main-red fixes first: `concurrency: {max_running: 6, max_running_per_key: 1, key: '{{ trigger.payload.attributes.repository }}#{{ trigger.payload.pull_request.number }}', priority: "'main-red' in trigger.payload.labels ? 10 : 0"}`.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `max_running` | integer | No | *How many trigger-launched runs of the workflow run at once; later firings wait. Set it, per_member, max_running_per_key, or a combination; with per_member it is a ceiling over the scaled cap. A run waiting on a provider (a usage limit, provider capacity, no provider that can serve it) with nothing else executing does not count: it gives its slot back, and takes one again before its next step, ahead of every waiting firing but never above the cap.* |
| `waiting` | enum | No | *What happens to waiting firings: `fifo` (the default) keeps every one, oldest first; `latest` keeps only the newest waiting firing per key, and each older one it replaces is recorded skipped with the detail "superseded" (never failed). Waiting never touches running runs; `cancel_running` does.* |
| `key` | string | No | *With `waiting: latest` or `max_running_per_key`: a template over `trigger`, like a webhook dedupe_key, naming a group of firings, e.g. `'{{ trigger.payload.attributes.repository }}#{{ trigger.payload.pull_request.number }}'`. Under waiting: latest its waiting firings replace each other (unset: one group for the whole workflow); with max_running_per_key its running runs are capped (required). max_running still bounds the whole workflow. A firing whose key does not render waits, is never superseded, and counts only against max_running.* |
| `per_member` | integer | No | *Scales the cap with the trigger's machine pool: this many runs for each member that can take work (installed, synced with the pool's catalog, not failed or removed), so adding a machine raises throughput with no workflow edit. A trigger on one machine, or on none, counts as a pool of one.* |
| `priority` | string | No | *A CEL expression over `trigger` (no `{{ }}`) yielding an integer, evaluated once when a firing starts waiting, e.g. `"'main-red' in trigger.payload.labels ? 10 : 0"`. Waiting firings are admitted highest priority first, then in arrival order; running runs are never preempted. Unset, or a firing whose expression fails: 0. A low-priority firing can wait behind higher ones until it expires, a day after it arrived.* |
| `max_running_per_key` | integer | No | *How many runs of one key (`key`, required) run at once, e.g. 1 for one run per pull request; the key's later firings wait. A firing whose key is at its cap does not hold back firings of other keys behind it. With max_running or per_member too, both caps apply; alone, only this one does. A run waiting on a provider gives back its max_running slot but keeps its key's.* |
| `cancel_running` | enum | No | *Lets a newer firing of a key stop the run holding the key's slot instead of waiting behind it (requires `key` and `max_running_per_key: 1`). `never` (the default) leaves running runs alone; `on_new_key_event` cancels on every newer firing of the key; `on_new_head` cancels only when the firing carries a GitHub pull request head (`trigger.payload.data.pull_request.head.sha`) that differs from the running run's firing's, so a comment, or a firing on the same head, never cancels. The cancel is a user stop's: the run's `finally:` runs, its firing records run status cancelled and "superseded by event <id>", and the new firing takes the slot under `waiting`.* |

---

## WorkflowAttention

Correlates incidents within one owner and project, never across them. Failures remain failed and emit failure events. Only explicit on_success: resolved closes an incident; a key-only observer's success does not. Example: `attention: {key: 'repo:{{ inputs.repo }}:pr:{{ inputs.pr }}', on_start: recovering, recovery_timeout: 30m, on_success: resolved}`.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `key` | string | Yes | *Required literal or template over finalized inputs only. Evaluated once before graph execution; must produce a nonempty string of at most 512 bytes. Invalid dynamic keys fail visibly with legacy attention as a fail-safe.* |
| `on_start` | string | No | *Absent, recovering, or action_required. Recovering requires recovery_timeout. Only an actually started recovery run suppresses attention.* |
| `recovery_timeout` | string | No | *Go duration in [1s, 24h], allowed only with on_start: recovering. Fixed from run start; retries never extend it. Expiry escalates through the durable inbox read, without a push notification.* |
| `on_success` | string | No | *Absent, resolved, or action_required. No implicit resolution on success. A recovery finishing without explicit successful resolution escalates.* |
| `on_failure` | string | No | *Absent (defaults to action_required), or action_required. Crashes, cancellation, and unsuccessful outcomes never resolve an incident.* |

---

## WorkflowCheck

A GitHub check run whose lifecycle the engine owns (`check:`): one per firing, posted as the Reliant App on the event's head commit, details_url the run's page. Example: `check: {name: reliant/ci, output: '{{ nodes.report.check }}', timeout: 2h}`.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | - |
| `repository` | string | No | *A template over `trigger` naming the repository, owner/name. Unset: the event's (trigger.payload.attributes.repository).* |
| `head_sha` | string | No | *A template over `trigger` naming the commit. Unset: the event's head commit (a pull request's head, the check suite's or check run's head_sha, the workflow run's head_sha, a push's new commit).* |
| `output` | string | No | *A template over the ended run's scope (nodes, inputs, trigger, workflow, run.status) evaluating to {title, summary, text?, annotations?}. Unset, or one that fails: the engine's own summary. The conclusion comes from how the run ended, never from this.* |
| `timeout` | string | No | *A Go duration from 1m to 72h (default 6h), from the firing: a check still not concluded then concludes timed_out.* |

---

## DaemonSelector

Where tools run (`daemon:` on the workflow or a node; node > workflow > the chat's or automation's machine > the default machine). A string is shorthand: `local`, `cloud` or `any` is a kind, anything else a machine name. `{pool: <name>}` places the run (workflow level) or the node (node level, reused by every loop iteration) ONCE on one machine and holds a slot on it while the run is live, so the run is not tied to any one machine being there and free. Example: `daemon: {pool: ci}`.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | No | *Exact machine id. Pins one machine (lint: daemon-machine-pin).* |
| `name` | string | No | *Machine name, or id. Pins one machine (lint: daemon-machine-pin).* |
| `type` | string | No | *Machine kind: `local`, `cloud` or `any`.* |
| `labels` | map[string]string | No | *Match by the labels a machine advertises when it connects (all must match), on each call. Alone or beside id, name or type; never placed.* |
| `pool` | string | No | *Place once on one member of the owner's machine pool of this name: the least-loaded member that can take work, on the pool's agreed workflow catalog, under its max_runs_per_member (a full pool waits for a slot). Exclusive with id, name, type and labels. A pool the owner does not have fails validation at save.* |

---

## Syntax Sugar (YAML-only)

These fields are not part of the proto schema — they are YAML convenience features that desugar to standard nodes, edges, and entry at parse time.

### `sequence:`

Top-level workflow field. Replaces `entry:` + `nodes:` + sequential `edges:` for linear chains. Cannot coexist with `entry:`.

### `type: parallel` (node type)

A node with `type: parallel` and `branches:` expands into branch nodes + a join node + fan-out/fan-in edges. The parallel node's `id` becomes the join node's id.

See workflow documentation for full examples and usage patterns.

