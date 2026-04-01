# Agent Workflow Test Suite

This directory contains comprehensive tests for the `agent.yaml` workflow file, which is the core execution engine for all AI agent interactions in Reliant.

## Test File: `agent_test.go`

### Overview

The test suite validates the agent workflow's structure, routing logic, CEL conditions, and data flow. These tests are critical because the agent workflow is used for every AI conversation, and any errors would break the entire system.

## Test Cases

### 1. TestAgentWorkflowValidation

**Purpose**: Verifies that `agent.yaml` parses and validates correctly.

**What it tests**:

- YAML syntax is valid
- Workflow name is "agent"
- All required steps exist:
  - `save_message` - **Single unified step** that handles all message types (user/assistant/tool/system)
  - `call_llm` - Calls the LLM to get assistant response
  - `approval` - Approval gate for tool execution
  - `execute_tools` - Executes approved tools
  - `compact` - Compacts context when token limit is reached
- Workflow passes structural validation (via parser.ValidateWorkflow)

**Why it's important**:
The agent workflow is the foundation for all AI interactions. If the YAML is malformed or missing required steps, the entire agent system will fail. This test catches basic structural errors early.

**Key Architecture Change**:
Previously, the workflow had 5 separate SaveMessage steps (`save_initial_message`, `save_assistant_message`, `save_tool_results`, `save_user_after_compact`, `save_tool_results_after_compact`). Now there is **one unified `save_message` step** that handles all message types using the **source namespace** pattern.

---

### 2. TestAgentWorkflowEdgeConditions

**Purpose**: Tests that all CEL conditions compile and evaluate correctly.

**What it tests**:
Seven critical routing conditions:

1. **Tools requested with manual approval**

   - Condition: `source.tool_calls != null && size(source.tool_calls) > 0 && chat.auto_approve == false`
   - Routes to: `approval` step
   - Why: When tools are requested and auto-approve is disabled, user must approve
   - **Note**: Uses `source` namespace to access the triggering step's outputs

2. **Tools requested with auto-approve**

   - Condition: `source.tool_calls != null && size(source.tool_calls) > 0 && chat.auto_approve == true`
   - Routes to: `execute_tools` step directly
   - Why: When auto-approve is enabled, skip approval gate

3. **No tools requested - workflow terminates**

   - No explicit condition needed
   - Routes to: Terminal state (workflow ends)
   - Why: When no tools are requested and no edges match, workflow completes

4. **Compaction needed**

   - Condition: `save_message.thread_token_count * 10 > 200000 * 8`
   - Routes to: `compact` step
   - Why: When context exceeds 80% of model capacity (200k tokens), trigger compaction

5. **No compaction needed**

   - Condition: `save_message.thread_token_count * 10 <= 200000 * 8`
   - Routes to: `call_llm` step
   - Why: When context has space, skip compaction and continue

6. **Approval approved**

   - Condition: `event.data.status == 'approved'`
   - Routes to: `execute_tools` step
   - Why: User approved tool execution

7. **Approval denied or timeout**
   - Condition: `event.data.status == 'denied' || event.data.status == 'timeout'`
   - Routes to: `call_llm` step
   - Why: Loop back to LLM without executing tools

**Why it's important**:
CEL conditions control workflow routing. If they fail to compile, the workflow crashes at runtime. If they evaluate incorrectly, the workflow takes the wrong path (e.g., executing tools without approval, or compacting context too early). This test validates both compilation and evaluation logic.

---

### 3. TestAgentWorkflowStepReferences

**Purpose**: Verifies all step IDs referenced in edges exist.

**What it tests**:

- Every edge's `to` field references a valid step ID
- Every edge's `from` field references either:
  - A valid step ID, or
  - A valid workflow-level event (workflow, started, turn, message, tool, tools)

**Why it's important**:
If an edge references a non-existent step, the workflow will fail to route correctly. This can cause the workflow to get stuck or crash. This test ensures workflow integrity by validating all connections.

---

### 4. TestAgentWorkflowDataFlow

**Purpose**: Verifies data flows correctly through CEL expressions using the **source namespace** pattern.

**What it tests**:

#### Standardized Message Schema

All message-producing activities (CallLLM, ExecuteTools, Compact) output a standardized schema:

- `message.role`: "user" | "assistant" | "tool" | "system"
- `message.text`: The message content

#### Source Namespace Pattern

The `save_message` step receives data from the triggering step via the **source namespace**:

1. **save_message** receives from **any triggering step** via source:

   - `role: source.message.role` - Message role from source
   - `content: source.message.text` - Message content from source
   - `tool_calls: source.tool_calls` - Tool calls (if any)
   - `tool_results: source.tool_results` - Tool results (if any)
   - `token_count: source.token_count` - Total token count (prompt + response + context)

2. **execute_tools** receives from **call_llm**:

   - `tool_calls: call_llm.tool_calls` - Tools to execute

3. **Workflow initialization** provides:

   - Initial message via `started` event with `message.role` and `message.text`

4. **All steps** receive workflow context:
   - `chat_id` - Chat identifier (auto-injected to activity inputs)
   - `thread` - Thread path from execution context

#### Edge Condition Validation

Verifies edge conditions reference valid outputs:

- `nodes.call_llm.tool_calls` - Used in routing conditions for tool execution
- `inputs.auto_approve` - Controls approval routing

**Why it's important**:
The `nodes.*` namespace pattern is the foundation of the workflow. If node outputs aren't correctly stored and accessed, edge conditions won't work. This test ensures the data flow through the nodes namespace works correctly.

---

### 5. TestAgentWorkflowRoutingLogic

**Purpose**: Tests complete routing scenarios end-to-end.

**What it tests**:

Eight routing scenarios that represent real user interactions:

1. **Initial user message flow**

   - Event: `started`
   - Routes to: `save_message`
   - Scenario: User starts a new conversation (saves user message)

2. **No tools requested - workflow terminates**

   - Event: `save_message.completed`
   - Context: Empty or null `source.tool_calls`
   - Routes to: Terminal state (no matching edges)
   - Scenario: Assistant responds with text only, workflow completes

3. **Tools requested with auto-approve**

   - Event: `save_message.completed` (after saving assistant message)
   - Context: `source.tool_calls` present, `auto_approve = true`
   - Routes to: `execute_tools`
   - Scenario: Assistant wants to use tools, auto-approve enabled, skip approval

4. **Tools requested with manual approval**

   - Event: `save_message.completed` (after saving assistant message)
   - Context: `source.tool_calls` present, `auto_approve = false`
   - Routes to: `approval`
   - Scenario: Assistant wants to use tools, user must approve

5. **Approval approved - execute tools**

   - Event: `approval.completed`
   - Context: `event.data.status = "approved"`
   - Routes to: `execute_tools`
   - Scenario: User approved tool execution

6. **Approval denied - loop to LLM**

   - Event: `approval.completed`
   - Context: `event.data.status = "denied"`
   - Routes to: `call_llm`
   - Scenario: User denied tool execution, ask LLM to try different approach

7. **After user message - compaction needed**

   - Event: `save_message.completed`
   - Context: `save_message.thread_token_count * 10 > 200000 * 8` (>80% full)
   - Routes to: `compact`
   - Scenario: Context is almost full, need to compact before calling LLM

8. **After user message - no compaction needed**
   - Event: `save_message.completed`
   - Context: `save_message.thread_token_count * 10 <= 200000 * 8` (<80% full)
   - Routes to: `call_llm`
   - Scenario: Context has space, continue to LLM

**Why it's important**:
This test validates the entire workflow logic by simulating real user interactions. It ensures that:

- The workflow correctly routes based on user actions (approve/deny)
- The workflow correctly routes based on LLM behavior (tools vs no tools)
- The workflow correctly routes based on system state (context full vs not full)
- Edge conditions work together correctly (not just in isolation)
- The source namespace pattern correctly passes data between steps

---

## Running the Tests

```bash
# Run all agent workflow tests
go test -v ./internal/workflow/builtin/... -run TestAgent

# Run a specific test
go test -v ./internal/workflow/builtin/... -run TestAgentWorkflowValidation
go test -v ./internal/workflow/builtin/... -run TestAgentWorkflowEdgeConditions
go test -v ./internal/workflow/builtin/... -run TestAgentWorkflowStepReferences
go test -v ./internal/workflow/builtin/... -run TestAgentWorkflowDataFlow
go test -v ./internal/workflow/builtin/... -run TestAgentWorkflowRoutingLogic
```

## Test Coverage

These tests cover:

- ✅ YAML parsing and structural validation
- ✅ CEL condition compilation
- ✅ CEL condition evaluation with mock data
- ✅ Step reference validation
- ✅ Data flow validation through source namespace
- ✅ End-to-end routing logic
- ✅ Standardized message schema validation

## What These Tests Don't Cover

These tests do NOT cover:

- ❌ Runtime execution of activities (covered by activity idempotency tests)
- ❌ Database operations (covered by integration tests)
- ❌ Actual LLM calls (covered by integration tests)
- ❌ UI rendering (covered by frontend tests)

## Maintenance

When modifying `agent.yaml`, ensure:

1. **Add new message-producing activities** → Ensure they output standardized `message.role` and `message.text` schema
2. **Add new edges** → Update `TestAgentWorkflowEdgeConditions` if they have conditions
3. **Change data flow** → Update `TestAgentWorkflowDataFlow` expected references
4. **Change routing logic** → Update `TestAgentWorkflowRoutingLogic` scenarios
5. **Modify source namespace** → Update all tests that reference `source.*` fields

## Debugging Failed Tests

### TestAgentWorkflowValidation fails

- Check YAML syntax in `agent.yaml`
- Ensure all required steps exist (save_message, call_llm, approval, execute_tools, compact)
- Run: `yamllint agent.yaml`

### TestAgentWorkflowEdgeConditions fails

- Check CEL syntax in edge conditions
- Ensure `source` variable is available in CEL context
- Verify step output variables are defined (e.g., `save_message.thread_token_count`)
- Test CEL conditions in isolation using the CEL playground

### TestAgentWorkflowStepReferences fails

- Check for typos in edge `from` and `to` fields
- Ensure all referenced steps exist in the `steps` list
- Verify no references to old step names (save_initial_message, save_assistant_message, etc.)

### TestAgentWorkflowDataFlow fails

- Check CEL expression syntax in step inputs (e.g., `source.message.role`)
- Ensure activities output standardized message schema
- Verify source namespace is populated correctly
- Check that CEL expressions evaluate without errors

### TestAgentWorkflowRoutingLogic fails

- Check edge conditions match expected routing behavior
- Verify mock data in test matches real runtime data structure
- Ensure `source` namespace is included in mock data
- Verify multiple edges don't conflict (same source with overlapping conditions)

## Architecture Notes

The agent workflow follows this **consolidated architecture**:

```
1. MESSAGE OPERATIONS:
   - SaveMessage: Single step that handles ALL message types (user/assistant/tool/system)
   - CallLLM: Streams to chunks table, returns standardized message output
   - ExecuteTools: Executes all tools, returns standardized message output
   - Compact: Generates summary, returns standardized message output

2. STANDARDIZED MESSAGE SCHEMA:
   All message-producing activities output:
     message.role: "user" | "assistant" | "tool" | "system"
     message.text: The message content
   Plus activity-specific fields (tool_calls, tool_results, tokens, etc.)

3. SOURCE NAMESPACE:
   The "source" namespace provides access to the triggering step's outputs:
     source.message.role - Role from triggering step
     source.message.text - Content from triggering step
     source.tool_calls - Tool calls (if any)
     source.tool_results - Tool results (if any)

4. APPROVAL ROUTING (chat.auto_approve):
   - auto_approve=true: Tools execute immediately after save_message
   - auto_approve=false: Approval gate blocks until user approves/denies
   - Denied approvals loop back to LLM

5. CONTEXT COMPACTION:
   - Triggers when context exceeds 80% of 200k token capacity
   - Happens BEFORE calling LLM (checked after save_message)
   - Compact generates summary and saves it internally as a system message
   - context_sequence is incremented automatically by the Compact activity

6. LOOP STRUCTURE:
   - Main loop: call_llm → save_message → execute_tools → save_message → call_llm
   - Approval branch: call_llm → save_message → approval → execute_tools → save_message → call_llm
   - Compaction: save_message → compact → save_message (summary) → call_llm
   - Exit: call_llm → save_message → terminal (when no tools requested)
```

### Key Benefits of Consolidated Architecture:

1. **Simpler**: One save step instead of five
2. **More maintainable**: Standardized schema across all activities
3. **Better separation of concerns**: Activities return data, SaveMessage persists
4. **Easier to extend**: Adding new message-producing activities just requires outputting the standard schema
5. **Cleaner data flow**: Source namespace makes it explicit which step's output is being saved

All tests are designed to validate this consolidated architecture remains intact.
