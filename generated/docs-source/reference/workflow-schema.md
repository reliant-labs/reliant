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
| `daemon` | CelDaemonSelector | No | - |
| `resume_node` | string | No | - |
| `transition_to` | string | No | - |
| `triggers` | WorkflowTrigger[] | No | *WHEN the workflow runs: a list of {name, description, one source (schedule \| webhook \| integration \| workflow_event), filter (raw CEL over `trigger`), inputs (templates over `trigger`), prompt (template over `trigger`: the message each run starts from)}. Declaring one fires nothing; the user enables it in the app. See the workflow-builder skill's Triggers section.* |
| `title` | string | No | - |
| `hidden` | boolean | No | *A building block other workflows ref: true keeps it out of every picker and default listing while it stays resolvable by ref. With automation_only too, it is an automation's internal step and is listed nowhere. Unset means the workflow is listed.* |
| `automation_only` | boolean | No | *The Chat trigger turned off: true keeps the workflow out of chat pickers and refuses a chat start of it. Its triggers start it, and so does a deliberate start by hand: Run now, `reliant workflow run`, an agent's start_run, or a builder test run. Unset means a chat can start it.* |
| `concurrency` | WorkflowConcurrency | No | *Bounds trigger-launched runs: {max_running: N} (later firings wait, FIFO), optionally waiting: latest (only the newest waiting firing per key survives; older ones are recorded skipped, "superseded", never failed) and key (a template over `trigger` grouping the firings that replace each other). See WorkflowConcurrency below.* |
| `on_failure` | Workflow | No | *A finally-style handler: an inline graph ({entry, nodes, edges}) that runs once when a run FAILS (an unhandled node error, an agent that never produced its required response, a node that declares outcome: failure). Never when the run completes, pauses or is cancelled. Its nodes read `failure.{node_id, node_path, error, error_code, attempt}`, `inputs`, and the `nodes` outputs the run produced (guard them with `.?`). The run still ends FAILED; an error in the handler is logged and never recursive.* |
| `requires` | WorkflowRequirements | No | - |

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

A trigger admission policy (`concurrency:`), not a worker task limit. Pooled per owner, project and workflow; manual trigger fires participate, chat starts do not. Example, one release at a time with only the newest waiting merge per repository: `concurrency: {max_running: 1, waiting: latest, key: '{{ trigger.payload.attributes.repository }}'}`.

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `max_running` | integer | No | *How many trigger-launched runs of the workflow run at once; later firings wait. Required, at least 1.* |
| `waiting` | enum | No | *What happens to waiting firings: `fifo` (the default) keeps every one, oldest first; `latest` keeps only the newest waiting firing per key, and each older one it replaces is recorded skipped with the detail "superseded" (never failed). Running runs are never touched.* |
| `key` | string | No | *With `waiting: latest` only: a template over `trigger`, like a webhook dedupe_key, naming the group of waiting firings that replace each other, e.g. `'{{ trigger.payload.attributes.repository }}'`. Unset: one group for the whole workflow. max_running still bounds the whole workflow. A firing whose key does not render waits FIFO and is never superseded.* |

---

## Syntax Sugar (YAML-only)

These fields are not part of the proto schema — they are YAML convenience features that desugar to standard nodes, edges, and entry at parse time.

### `sequence:`

Top-level workflow field. Replaces `entry:` + `nodes:` + sequential `edges:` for linear chains. Cannot coexist with `entry:`.

### `type: parallel` (node type)

A node with `type: parallel` and `branches:` expands into branch nodes + a join node + fan-out/fan-in edges. The parallel node's `id` becomes the join node's id.

See workflow documentation for full examples and usage patterns.

