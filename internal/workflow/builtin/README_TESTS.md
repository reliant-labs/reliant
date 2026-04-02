# Agent Workflow Test Suite

Tests for `agent.yaml` — the core execution engine for all AI agent interactions.

## Test Cases

### 1. TestAgentWorkflowValidation

Verifies `agent.yaml` parses and validates correctly: YAML syntax, workflow name is "agent", all required steps exist (`save_message`, `call_llm`, `approval`, `execute_tools`, `compact`), and structural validation passes.

**Key Architecture Note**: Previously there were 5 separate SaveMessage steps. Now there is **one unified `save_message` step** using the **source namespace** pattern.

### 2. TestAgentWorkflowEdgeConditions

Tests that all CEL conditions compile and evaluate correctly. Seven routing conditions:

1. **Tools requested + manual approval** → `approval` step
   - `source.tool_calls != null && size(source.tool_calls) > 0 && chat.auto_approve == false`

2. **Tools requested + auto-approve** → `execute_tools` directly
   - `source.tool_calls != null && size(source.tool_calls) > 0 && chat.auto_approve == true`

3. **No tools requested** → Terminal state (no matching edges, workflow ends)

4. **Compaction needed** → `compact` step
   - `save_message.thread_token_count * 10 > 200000 * 8` (context >80% of 200k capacity)

5. **No compaction needed** → `call_llm` step
   - `save_message.thread_token_count * 10 <= 200000 * 8`

6. **Approval approved** → `execute_tools`
   - `event.data.status == 'approved'`

7. **Approval denied/timeout** → `call_llm`
   - `event.data.status == 'denied' || event.data.status == 'timeout'`

### 3. TestAgentWorkflowStepReferences

Verifies all step IDs referenced in edges exist. Every edge's `to` references a valid step ID; every `from` references either a valid step ID or a workflow-level event (`workflow`, `started`, `turn`, `message`, `tool`, `tools`).

### 4. TestAgentWorkflowDataFlow

Verifies data flows correctly through CEL expressions using the **source namespace** pattern.

**Standardized Message Schema** — all message-producing activities (CallLLM, ExecuteTools, Compact) output:
- `message.role`: "user" | "assistant" | "tool" | "system"
- `message.text`: The message content

**Source Namespace** — `save_message` receives from the triggering step via `source`:
- `source.message.role`, `source.message.text`
- `source.tool_calls`, `source.tool_results`
- `source.token_count`

**execute_tools** receives `call_llm.tool_calls`. Workflow initialization provides initial message via `started` event.

### 5. TestAgentWorkflowRoutingLogic

Tests complete routing scenarios end-to-end. Eight scenarios:

| # | Event | Context | Routes To |
|---|-------|---------|-----------|
| 1 | `started` | — | `save_message` |
| 2 | `save_message.completed` | Empty `source.tool_calls` | Terminal (workflow ends) |
| 3 | `save_message.completed` | `source.tool_calls` present, `auto_approve=true` | `execute_tools` |
| 4 | `save_message.completed` | `source.tool_calls` present, `auto_approve=false` | `approval` |
| 5 | `approval.completed` | `status="approved"` | `execute_tools` |
| 6 | `approval.completed` | `status="denied"` | `call_llm` |
| 7 | `save_message.completed` | Token count >80% capacity | `compact` |
| 8 | `save_message.completed` | Token count ≤80% capacity | `call_llm` |

## Running the Tests

```bash
# All agent workflow tests
go test -v ./internal/workflow/builtin/... -run TestAgent

# Specific test
go test -v ./internal/workflow/builtin/... -run TestAgentWorkflowValidation
```

## Test Coverage

- ✅ YAML parsing and structural validation
- ✅ CEL condition compilation and evaluation with mock data
- ✅ Step reference validation
- ✅ Data flow through source namespace
- ✅ End-to-end routing logic
- ✅ Standardized message schema validation
- ❌ Runtime activity execution (covered by activity tests)
- ❌ Database operations, actual LLM calls, UI (covered by integration/frontend tests)

## Maintenance

When modifying `agent.yaml`:
1. **New message-producing activities** → Must output `message.role` and `message.text`
2. **New edges with conditions** → Update `TestAgentWorkflowEdgeConditions`
3. **Changed data flow** → Update `TestAgentWorkflowDataFlow`
4. **Changed routing** → Update `TestAgentWorkflowRoutingLogic`
5. **Modified source namespace** → Update all tests referencing `source.*`

## Architecture

```
1. MESSAGE OPERATIONS:
   - SaveMessage: Single step for ALL message types (user/assistant/tool/system)
   - CallLLM: Streams to chunks table, returns standardized message output
   - ExecuteTools: Executes all tools, returns standardized message output
   - Compact: Generates summary, returns standardized message output

2. STANDARDIZED MESSAGE SCHEMA:
   All message-producing activities output:
     message.role: "user" | "assistant" | "tool" | "system"
     message.text: The message content
   Plus activity-specific fields (tool_calls, tool_results, tokens, etc.)

3. SOURCE NAMESPACE:
     source.message.role - Role from triggering step
     source.message.text - Content from triggering step
     source.tool_calls   - Tool calls (if any)
     source.tool_results  - Tool results (if any)

4. APPROVAL ROUTING (chat.auto_approve):
   - true: Tools execute immediately after save_message
   - false: Approval gate blocks until user approves/denies
   - Denied → loop back to LLM

5. CONTEXT COMPACTION:
   - Triggers at >80% of 200k token capacity
   - Checked after save_message, before calling LLM
   - Compact generates summary as system message
   - context_sequence incremented by Compact activity

6. LOOP STRUCTURE:
   - Main:     call_llm → save_message → execute_tools → save_message → call_llm
   - Approval:  call_llm → save_message → approval → execute_tools → save_message → call_llm
   - Compact:  save_message → compact → save_message (summary) → call_llm
   - Exit:     call_llm → save_message → terminal (no tools requested)
```
