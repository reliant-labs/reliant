---
name: workflow-builder
description: Build, edit, debug, and test Reliant workflows. Use when creating new workflows, modifying existing ones, writing scenario tests, or troubleshooting workflow execution issues.
compatibility: reliant
metadata:
  category: workflow
  owner: reliant
---
# Workflow Builder

## When to use

- Creating a new Reliant workflow from scratch
- Editing or extending an existing workflow
- Debugging workflow execution issues
- Writing scenario tests for workflows
- Understanding workflow patterns and best practices

## Approach

Follow the 6-step process:

1. **Setup** — Call `get_workflow(id="<draft_id>")` to see current content
2. **Understand** — Ask clarifying questions about the user's goal
3. **Learn** — Use `list_workflows` to see examples and patterns
4. **Explore** — Read the user's codebase (test commands, code patterns). Note: references to "workflows" and "nodes" in user code are unlikely to be Reliant-specific.
5. **Build** — Use `edit_workflow` for small changes, `write_workflow` for larger rewrites
6. **Test** — Create and run scenarios (aim for 3+ covering positive, negative, and edge cases). Try to break your workflow. It's frustrating for users to run a workflow for an hour and hit a bug at the end—scenarios catch this early.

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

Load these with `load_tool` as needed:

| Tool | Purpose |
|------|---------|
| `get_workflow` | Read current workflow content |
| `edit_workflow` | Make targeted edits to a workflow |
| `write_workflow` | Full workflow rewrite |
| `get_schema` | Get full field documentation for any node/input/shared type |
| `get_cel_reference` | Authoritative CEL reference (namespaces, functions, types) |
| `list_workflows` | Browse existing workflows for examples and patterns |
| `write_scenario` | Create a test scenario |
| `run_scenario` | Execute a scenario and check results |
| `list_scenarios` | List existing scenarios for a workflow |
| `view_scenario` | Read a scenario's content |
| `edit_scenario` | Modify an existing scenario |
| `delete_scenario` | Remove a scenario |
| `get_workflow_suggestions` | Get AI-powered suggestions for workflow improvements |

## CEL Reference

> **Authoritative source**: Use `get_cel_reference` tool for the complete, auto-generated reference. This section covers essential patterns and quick-lookup tables.

### Syntax rules

| Context | Syntax | Example |
|---------|--------|--------|
| `condition`, `while` | Pure CEL (no `{{}}`) | `condition: "nodes.llm.stop_reason == 'tool_use'"` |
| All other fields | Template interpolation `{{}}` | `model: "{{inputs.model}}"` |

### Namespaces

| Namespace | Description |
|-----------|-------------|
| `inputs.*` | Workflow input values passed at invocation |
| `iter.*` | Loop iteration context (iteration count, first/last flags) |
| `nodes.*` | Output from completed nodes (`nodes.<id>.<field>`) |
| `output.*` | Current activity output (for `save_message` context) |
| `outputs.*` | Loop iteration outputs for `while` condition evaluation |
| `thread.*` | Current thread context (token_count, message_count) |
| `trigger.*` | Trigger context (message, attachments) for triggered workflows |
| `workflow.*` | Workflow execution context (id, name, run_id, session_id, path, branch, mode) |

#### `workflow` fields

| Field | Type | Description |
|-------|------|-------------|
| `workflow.id` | string | Workflow execution ID (unique per run) |
| `workflow.name` | string | Workflow definition name |
| `workflow.run_id` | string | Temporal run ID |
| `workflow.session_id` | string | Session ID |
| `workflow.path` | string | Working directory path |
| `workflow.worktree_path` | string | Git worktree path (if applicable) |
| `workflow.branch` | string | Current git branch |
| `workflow.mode` | string | Execution mode (auto, manual, plan) |

#### `iter` fields

| Field | Type | Description |
|-------|------|-------------|
| `iter.iteration` | int | Current loop iteration (0-indexed) |

### Key functions

| Function | Example |
|----------|--------|
| `parseJson(string) -> dyn` | `parseJson(nodes.run.stdout)` |
| `toJson(dyn) -> string` | `toJson(nodes.llm.tool_calls)` |
| `coalesce(dyn, dyn) -> dyn` | `coalesce(inputs.name, "default")` |
| `getOrDefault(map, key, default) -> dyn` | `getOrDefault(inputs, "mode", "auto")` |
| `now() -> string` | `now()` |
| `parseDuration(string) -> double` | `parseDuration("5m") == 300.0` |
| `spawn(string, list) -> string` | `spawn("builtin://agent", ["general", "researcher"])` |
| `string.trimPrefix(string) -> string` | `nodes.run.stdout.trimPrefix("Error: ")` |
| `string.trimSuffix(string) -> string` | `nodes.run.stdout.trimSuffix("\n")` |
| `string.replace(string, string) -> string` | `nodes.llm.response_text.replace("\n", " ")` |
| `string.split(string) -> list(string)` | `nodes.run.stdout.split("\n")` |
| `list.join(string) -> string` | `["a", "b"].join(", ")` |
| `string.format(list) -> string` | `"Hello %s, you have %d items".format([name, count])` |

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

# Parse JSON from a command output
# (in a CEL-interpolated field)
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

# Bad — will error if node was skipped or field doesn't exist
condition: "nodes.check.exit_code == 0"
```

## Scenario Testing

Test workflows by simulating LLM and tool responses without making real API calls.

### Scenario fields

| Field | Description |
|-------|-------------|
| `name` | Unique scenario identifier (required) |
| `description` | What this scenario tests |
| `events` | Simulated events in execution order (required) |
| `expect` | Expected outcome and assertions |
| `inputs` | Override workflow inputs |
| `start_at` | Begin execution at a specific node |
| `state` | Pre-populate node outputs |

### Event types

| Type | Key Fields | Description |
|------|------------|-------------|
| `llm_response` | `text` or `tool_calls` | Simulate LLM output |
| `tool_result` | `tool`, `tool_output` | Simulate tool execution result |
| `tool_error` | `tool`, `tool_output` | Simulate tool failure |
| `user_input` | `text` | Simulate user message |

### Expectations

| Field | Description |
|-------|-------------|
| `outcome` | `completed` or `error` |
| `reached` | Nodes that must be scheduled |
| `not_reached` | Nodes that must NOT be scheduled |
| `error_contains` | Substring that must appear in the error message |
| `node_outputs` | Assert specific output values from nodes |

### Example scenario

```yaml
name: agent_tool_usage
description: Agent calls a tool and completes
events:
  - node: agent_loop.call_llm
    type: llm_response
    tool_calls: [{name: bash, input: {command: ls}}]
  - node: agent_loop.execute_tools
    type: tool_result
    tool: bash
    tool_output: {result: "file.txt"}
  - node: agent_loop.call_llm
    type: llm_response
    text: "Found file.txt"
expect:
  outcome: completed
  reached: [agent_loop.call_llm, agent_loop.execute_tools]
```

### Best practices

- **Aim for 3+ scenarios per workflow** — cover the happy path, error cases, and edge cases
- **Test routing logic** — create scenarios that exercise each branch/case in your edges
- **Test loop termination** — verify loops exit correctly (both via `while` condition and max iterations)
- **Test skipped nodes** — verify conditional nodes skip correctly and downstream nodes handle it
- **Use `start_at`** — to test specific sections of complex workflows without simulating the entire flow
- **Use `state`** — to pre-populate node outputs when testing downstream logic
- **Name scenarios descriptively** — e.g., `error_handling_api_failure` not `test_3`
- **Try to break it** — simulate unexpected LLM responses, tool failures, and edge cases. Finding bugs in scenarios is much cheaper than finding them during a real 1-hour workflow run.

### Scenario tools

| Tool | Purpose |
|------|--------|
| `write_scenario` | Create a new scenario |
| `run_scenario` | Execute a scenario and see results |
| `list_scenarios` | List existing scenarios for a workflow |
| `view_scenario` | Read a scenario's content |
| `edit_scenario` | Modify an existing scenario |
| `delete_scenario` | Remove a scenario |

## References

- Use `get_cel_reference` for the complete, auto-generated CEL reference
- Use `get_schema(name="<type>")` for full field docs on any node type, input type, or shared type
- Use `list_workflows` to find real-world examples and established patterns