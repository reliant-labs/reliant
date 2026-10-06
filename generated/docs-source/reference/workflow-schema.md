<!--
=============================================================================
GENERATED FILE - DO NOT EDIT DIRECTLY

Source: proto messages V2Workflow, V2Edge, V2EdgeCase in workflow_v2.proto
Generator: tools/docgen/schema
Regenerate: make generate-schema
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
| `triggers` | WorkflowTrigger[] | No | *WHEN the workflow runs: a list of {name, description, one source (schedule \| webhook \| integration \| workflow_event), filter (raw CEL over `trigger`), inputs (templates over `trigger`), prompt (template over `trigger`: the message each run starts from)}. Declaring one fires nothing; activate it with activate_trigger. See the workflow-builder skill's Triggers section.* |
| `title` | string | No | - |
| `hidden` | boolean | No | - |
| `automation_only` | boolean | No | *The Chat trigger turned off: true keeps the workflow out of chat pickers and refuses a chat start of it, so only its triggers (or a builder test run) start it. Unset means a chat can start it.* |

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

## Syntax Sugar (YAML-only)

These fields are not part of the proto schema — they are YAML convenience features that desugar to standard nodes, edges, and entry at parse time.

### `sequence:`

Top-level workflow field. Replaces `entry:` + `nodes:` + sequential `edges:` for linear chains. Cannot coexist with `entry:`.

### `type: parallel` (node type)

A node with `type: parallel` and `branches:` expands into branch nodes + a join node + fan-out/fan-in edges. The parallel node's `id` becomes the join node's id.

See workflow documentation for full examples and usage patterns.

