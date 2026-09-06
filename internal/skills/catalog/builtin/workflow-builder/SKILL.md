---
name: workflow-builder
description: Build, edit, debug, and test Reliant workflows. Use when creating new workflows, modifying existing ones, writing scenario tests, or troubleshooting workflow execution issues.
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

- Creating a new Reliant workflow from scratch
- Editing or extending an existing workflow
- Debugging workflow execution issues
- Writing scenario tests for workflows
- Understanding workflow patterns and best practices

## Approach

The workflow tools (`get_workflow`, `edit_workflow`, `write_workflow`, the scenario tools) all take an
optional `id` — a UUID, slug, or name. Omit it and they resolve to the workflow this chat is
editing; pass it only to target a different workflow. There is no draft ID injected into the
system message to look for.

Starting from scratch (no workflow exists yet)? Call `create_workflow` first — it returns the new
draft's `id`, `name`, and `slug`. Omit both `name` and `content` to get a random name and the
default agent template; pass `content` with complete workflow YAML to start from a specific design.
After that, the chat is editing this draft, so `get_workflow`/`edit_workflow`/`write_workflow` need no
`id` either.

Follow this process:

1. **Setup** — New workflow: call `create_workflow`. Existing workflow: call `get_workflow()` (no id
   needed) to see current content.
2. **Understand** — Ask clarifying questions about the user's goal
3. **Learn** — Use `list_workflows` to see examples and patterns, `list_presets`/`get_preset` to see what
   agent presets exist before inventing a system prompt from scratch
4. **Explore** — Read the user's codebase (test commands, code patterns). Note: references to "workflows" and "nodes" in user code are unlikely to be Reliant-specific.
5. **Build** — Use `edit_workflow` for small changes, `write_workflow` for larger rewrites
6. **Test** — Create and run scenarios (aim for 3+ covering positive, negative, and edge cases). Try to break your workflow. It's frustrating for users to run a workflow for an hour and hit a bug at the end—scenarios catch this early.

Working in a chat that isn't running this preset? These tools are still reachable:
`load_tool(query="workflow")` loads them on demand.

## Key concepts

### Workflows are DAGs

Every workflow must be a directed acyclic graph. You cannot create cycles in the graph structure. Use **loop nodes** (with `while` conditions) for iterative behavior.

### CEL expressions

Two syntax modes:

- **`condition` / `while` fields**: Pure CEL — no `{{}}` wrapping
  ```yaml
  condition: "nodes.llm.stop_reason == 'tool_use'"
  while: "outputs.stop_reason != 'end_turn' && iter.iteration < 50"
  ```
- **All other fields**: Template interpolation with `{{}}`
  ```yaml
  model: "{{inputs.model}}"
  system_prompt: "You are helping with {{inputs.task}}"
  ```

Always use `has()` or safe navigation (`object.?field`) before accessing optional fields.

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
optional `id` (UUID/slug/name) described above except `create_workflow`, which mints one.

| Tool | Purpose |
|------|---------|
| `create_workflow` | Create a new workflow draft (returns id, name, slug) |
| `get_workflow` | Read current workflow content |
| `edit_workflow` | Make targeted edits to a workflow |
| `write_workflow` | Full workflow rewrite |
| `get_schema` | Get full field documentation for any node/input/shared type |
| `get_cel_reference` | Authoritative CEL reference (namespaces, functions, types) |
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

Outside a workflow-building chat, these tools aren't preloaded, but `load_tool(query="workflow")`
is always available (it's in `tag:default`) and loads the full set on demand.

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
| `inputs.*` | Workflow input values passed at invocation | workflow-specific |
| `iter.*` | Loop iteration context (iteration count, first/last flags) | `iteration` |
| `nodes.*` | Output from completed nodes (nodes.<id>.<field>) | workflow-specific |
| `output.*` | Current activity output (for save_message context) | workflow-specific |
| `outputs.*` | Loop iteration outputs for while condition evaluation | workflow-specific |
| `thread.*` | Current thread context (token_count, message_count) | workflow-specific |
| `trigger.*` | Trigger context (message, attachments) for triggered workflows | workflow-specific |
| `workflow.*` | Workflow execution context (id, name, run_id, etc.) | `id`, `name`, `run_id`, `session_id`, `path`, `worktree_path`, `branch`, `mode` |

#### `iter` fields

| Field | Type | Description |
|-------|------|-------------|
| `iter.iteration` | `int` | Current loop iteration (0-indexed) |

#### `workflow` fields

| Field | Type | Description |
|-------|------|-------------|
| `workflow.branch` | `string` | Current git branch (empty if not in git repo) |
| `workflow.id` | `string` | Workflow execution ID (unique per run) |
| `workflow.mode` | `string` | Execution mode (auto, manual, plan) |
| `workflow.name` | `string` | Workflow definition name |
| `workflow.path` | `string` | Working directory path |
| `workflow.run_id` | `string` | Workflow run ID (Temporal run ID) |
| `workflow.session_id` | `string` | Session ID for the workflow |
| `workflow.worktree_path` | `string` | Git worktree path (if in a worktree) |

### Key functions

| Function | Description | Example |
|----------|-------------|--------|
| `coalesce(dyn, dyn) -> dyn` | Return first non-null/non-empty argument | `coalesce(inputs.name, "default")` |
| `string.format(list) -> string` | Format a string with positional arguments | `"Hello %s, you have %d items".format([name, count])` |
| `getOrDefault(map, key, default) -> dyn` | Safely access a map key with a fallback default value | `getOrDefault(inputs, "mode", "auto")` |
| `list.join(string) -> string` | Join list elements with separator | `["a", "b"].join(", ")` |
| `now() -> string` | Return current time as RFC3339 string | `now()` |
| `parseDuration(string) -> double` | Parse a Go duration string and return seconds as a number | `parseDuration("5m") == 300.0` |
| `parseJson(string) -> dyn` | Parse a JSON string into a dynamic value | `parseJson(nodes.run.stdout)` |
| `string.replace(string, string) -> string` | Replace all occurrences of old with new | `nodes.llm.response_text.replace("\n", " ")` |
| `spawn(string, list) -> string` | Generate a spawn directive for a child workflow with presets | `spawn("builtin://agent", ["general", "researcher"])` |
| `string.split(string) -> list(string)` | Split string by separator | `nodes.run.stdout.split("\n")` |
| `toJson(dyn) -> string` | Serialize a value to a JSON string | `toJson(nodes.llm.tool_calls)` |
| `string.trimPrefix(string) -> string` | Remove prefix from string | `nodes.run.stdout.trimPrefix("Error: ")` |
| `string.trimSuffix(string) -> string` | Remove suffix from string | `nodes.run.stdout.trimSuffix("\n")` |

### Common patterns

```yaml
# Check if LLM wants to use tools
condition: "nodes.llm.stop_reason == 'tool_use'"

# Agentic loop — keep going until LLM stops calling tools
while: "outputs.stop_reason != 'end_turn' && iter.iteration < 50"

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
| `auditing-agent` | Agent with per-turn audit oversight. Main agent generates response, auditor (cheap model) reviews it. If denied, guidance is injected and tools are NOT executed. If approved, response is saved and tools run. Main agent response is deferred until audit approval to keep thread clean. The auditor replaces the manual approval gate — there is no separate "mode" input because every turn is automatically audited. |
| `blog-content-pipeline` | Structured content pipeline for producing technical blog posts for Reliant Labs. |
| `bmad-lite` | BMAD-Lite — Simplified BMAD methodology with persona-driven planning. Inspired by https://github.com/bmad-code-org/BMAD-METHOD |
| `build-workflow` | Build Workflow — create a custom Reliant workflow from a conversation. |
| `default-router` | Default workflow router — classifies the user's request and dispatches to the best strategy from a curated set of workflows. |
| `discovery-relay` | Discovery Relay — iterative waves with progressive knowledge transfer. |
| `env-setup` | Environment isolation pipeline: [setup → validate] (loop) → complete. Analyzes any codebase, sets up dynamic ports, isolated databases, worktree-named processes, language-appropriate hot-reload, consolidated logging (including browser console capture), and writes all state to .reliant/ephemeral/. Validation agent tests the full setup in a feedback loop until everything works. |
| `forge-migrate` | Migrate an existing codebase into a Forge project. Distinct from `migrate` (which imports Claude Code/Cursor/Codex config into Reliant) and from `forge-one-shot` (which builds a greenfield app from a conversation). |
| `forge-one-shot` | Build a production Forge app from a conversation, in ONE get-it-right loop: SCOPE → SCAFFOLD → VERIFY GREEN → FAN OUT → SYNTHESIZE → VERIFY. |
| `get-it-right` | Get It Right — for complex brownfield codebases where LLMs paper-mache code on top. The insight: sometimes you need to try and fail to truly understand the codebase. |
| `gsd` | GSD (Get Shit Done) — A pragmatic, no-ceremony workflow focused on rapid parallel execution. Inspired by https://github.com/gsd-build/get-shit-done. |
| `implement-review` | Generic implement → review loop. Implements changes then reviews them in a structured cycle until the reviewer approves or max iterations are reached. |
| `landing-page` | Build a polished landing page by chaining two get-it-right review loops and ending in a plain handoff agent that serves the page and hands the user a URL. |
| `markdown-checklist` | Complete a markdown checklist/task file until all checklist items are done. |
| `migrate` | Guided migration workflow for importing useful configuration from Claude Code, Cursor, Codex, or Windsurf into Reliant. |
| `one-ring` | Unified development pipeline: planning → write_tests → [get-it-right loop] → complete. |
| `parallel-compete` | 3 agents implement in parallel worktrees, reviewer picks winner or synthesizes. Thread mode: new (isolated context). Each worktree is independent. Apply path: use_winner copies via rsync, synthesize merges best parts. |
| `parallel-loop-sample` | Minimal sample showing a parallel loop over items with a custom key. Each item runs a builtin agent in parallel and the workflow routes based on iteration count, while scenarios assert the keyed aggregate result map. |
| `pitch-deck` | Generate an investor pitch deck from a company website with competitive research and founder interview. Includes parallel per-slide write+review pipeline and visual review via puppeteer screenshots + image attachments. |
| `ralph-wiggum` | Ralph Wiggum — brute-force iteration for complex tasks. |
| `scope-conversation` | Reusable scoping conversation sub-workflow. |
| `structured-agent` | Agent that requires structured output via response tool. Unlike builtin://agent which returns on end_turn, this loops until the response tool is called. If LLM responds without tools, a reminder is injected. Access output via output.response (structured data) and output.completed (boolean). |


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
| `approval` | Pause workflow execution for user approval |
| `ask_question` | Pause workflow execution to ask the user a question |
| `call_llm` | Send a prompt to a language model and get a response |
| `compact` | Conversation context to reduce token usage |
| `create_worktree` | Create a git worktree for isolated development |
| `execute_tools` | Execute tool calls from an LLM response |
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
| `tool` | string | No | Tool is the tool name (for type: tool_result or tool_error). |
| `tool_output` | object | No | ToolOutput is the tool execution result (for type: tool_result). |
| `is_error` | boolean | No | IsError marks a tool_result as failed (for type: tool_result). |

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

### Targeting Nodes

The `node` field on events targets specific nodes by ID:

- **Top-level nodes**: `node: "call_llm"`
- **Inner loop nodes**: `node: "loop_id.inner_node_id"` (dot-separated)
- **Inner workflow nodes**: `node: "workflow_id.inner_node_id"` (for `type: workflow` with `inline:`)
- **Nested structures**: `node: "outer.inner.node_id"`

For inline loops and inline workflow nodes, the simulator executes each inner node individually, evaluates conditions, and tracks skipped nodes with their qualified IDs.
For ref-based nodes, the default is black-box mocking with the ref name. If the runner can resolve the reference and the scenario targets qualified inner nodes, the simulator executes the referenced workflow internally instead.

**Event matching:**
- Events with a `node` field are matched to that specific node
- Events without a `node` field are consumed sequentially in order
- Multiple events with the same node are consumed in order per-node (use for multi-iteration loops)

### Event Types

| Type | Description | Required Fields |
|------|-------------|-----------------|
| `llm_response` | Simulate LLM returning text and/or tool_calls | `text` or `tool_calls` |
| `tool_result` | Simulate tool returning output | `tool`, `tool_output` |
| `tool_error` | Simulate tool error | `tool`, `tool_output` (with error) |
| `llm_error` | Simulate LLM error | `output` (with error structure) |
| `user_input` | Simulate user message | `text` |

### Use the raw `output:` form, not `type:`

Every event needs either `output:` (a raw activity output map) or `type:` (a convenience wrapper
like `llm_response`/`tool_result` that gets converted into one). **Write `output:` directly.** It is
the form every scenario in `internal/workflow/builtin/testdata/` uses (the CI-gating corpus), and it is
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
`internal/workflow/builtin/scenarios/get-it-right/stuck_feedback_rereviews_without_reimplementing.yaml`:

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

Verbatim from `internal/workflow/builtin/testdata/agent_scenarios.yaml`, run against `builtin://agent`.
It exercises a tool call whose output crosses the compaction token threshold, so the `compact`
node runs before the loop's next iteration:

```yaml
name: compaction_triggered
description: Token count exceeds the model's compaction threshold after tool execution, compact node runs
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
          input:
            file_path: "/path/to/large/file.go"
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
`internal/workflow/builtin/scenarios/structured-agent/happy_path.yaml`:

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

### Multi-scenario files

A scenario file holds one scenario per YAML document, separated by `---`. Put related scenarios for
one workflow in a single file (e.g. `agent_scenarios.yaml` holds 9+ scenarios) rather than one file per
scenario — this is the convention every builtin workflow follows.

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
  `internal/workflow/builtin/scenarios/pitch-deck/start_from_research.yaml`:

  ```yaml
  events:
    - node: classify
      output:
        selected_node: research_competitors
        reasoning: "Website analysis already exists — run the research phase."
  ```

  A corpus survey found 96 of 159 scenarios (60%) black-box at least one sub-workflow, and 27 (17%)
  have a silently-defaulted router — including every `one-ring` scenario, where names like
  `implement_only` and `plan_only` assert a routing outcome `classify` never actually made.
  Don't let this happen to your scenario: if the point is "task X gets classified as Y," mock `classify`.

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
