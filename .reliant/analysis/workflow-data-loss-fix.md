# Workflow Data Loss Fix - Analysis & Resolution

## Issue Summary

The workflow builder was losing critical node configuration data during round-trips through the API. Specifically:

**Lost Fields:**
- `system_prompt` in call_llm nodes
- `model` field  
- `tool_filter`
- `tools: true`
- Other custom node inputs

**Example:**
```yaml
# Database (CORRECT):
nodes:
  - id: llm
    type: call_llm
    model: "{{inputs.model}}"
    system_prompt: |
      You are the **GSD Orchestrator**.
      [... full prompt ...]
    tools: true
    tool_filter: "{{inputs.tools + ['spawn:builtin://agent']}}"

# API Response (BROKEN - before fix):
{
  "nodes": [{
    "id": "llm",
    "type": "call_llm"
    // model, system_prompt, tools, tool_filter ALL MISSING!
  }]
}
```

## Root Cause

The bug was in `internal/grpc/services/workflow.go` in the `parseDraftDefinitionWithPreservedNodeKeys` function.

**Issue #1: Not Recursive**
The function only processed top-level nodes and didn't recursively handle inline workflows inside loop nodes.

```go
// BEFORE (broken):
func parseDraftDefinitionWithPreservedNodeKeys(yamlData []byte) (*v2.Workflow, error) {
    // ... parse YAML ...
    if nodes, ok := raw["nodes"].([]interface{}); ok {
        for _, n := range nodes {
            // Process node...
            // ❌ NO recursion into inline workflows!
        }
    }
}
```

For the get-shit-done workflow with this structure:
```yaml
nodes:
  - id: agent_loop
    type: loop
    inline:  # ← This inline workflow's nodes were NOT processed!
      nodes:
        - id: llm
          type: call_llm
          model: "..."          # ← LOST!
          system_prompt: "..."  # ← LOST!
```

## The Fix

**Created recursive helper function:**

```go
// processNodesPreservingKeys recursively processes nodes and their inline workflows,
// merging unknown keys into args so they round-trip to the builder.
func processNodesPreservingKeys(nodes []interface{}) {
    for _, n := range nodes {
        nodeMap, ok := n.(map[string]interface{})
        if !ok {
            continue
        }

        // Process this node: merge unknown keys into args
        args, _ := nodeMap["args"].(map[string]interface{})
        if args == nil {
            args = make(map[string]interface{})
        }
        var toDelete []string
        for k, v := range nodeMap {
            if knownNodeKeys[k] {
                continue
            }
            args[k] = v
            toDelete = append(toDelete, k)
        }
        for _, k := range toDelete {
            delete(nodeMap, k)
        }
        if len(args) > 0 {
            nodeMap["args"] = args
        }

        // ✅ Recursively process inline workflow if present
        if inline, ok := nodeMap["inline"].(map[string]interface{}); ok {
            if inlineNodes, ok := inline["nodes"].([]interface{}); ok {
                processNodesPreservingKeys(inlineNodes)  // ← RECURSIVE!
            }
        }
    }
}
```

## Tests

### Backend Tests (Go)

Created comprehensive round-trip tests in `internal/grpc/services/workflow_roundtrip_test.go`:

#### Test 1: `TestWorkflowRoundTrip_CallLLMNode`
Tests that call_llm fields survive the round-trip:
- YAML → v2 → Proto → JSON → v2 → YAML

**Result:** ✅ PASS (after fix)

```
Proto Node Inputs: map[
    model:string_value:"{{inputs.model}}" 
    system_prompt:string_value:"You are a test assistant.\nThis is a multi-line prompt.\n" 
    tool_filter:string_value:"{{inputs.tools + ['spawn:builtin://agent']}}" 
    tools:bool_value:true
]
```

#### Test 2: `TestWorkflowRoundTrip_NestedLoopNode`
Tests that inline workflow nodes preserve fields:

**Before Fix:** ❌ FAIL
```
Inline LLM node args: map[]  ← EMPTY!
```

**After Fix:** ✅ PASS
```
Inline LLM node args: map[
    model:claude-3-5-sonnet-20241022 
    system_prompt:You are a helpful assistant. 
    tools:true
]
Proto inline LLM node inputs: map[
    model:string_value:"claude-3-5-sonnet-20241022" 
    system_prompt:string_value:"You are a helpful assistant." 
    tools:bool_value:true
]
```

### Frontend Tests (TypeScript)

Created comprehensive type-level tests in `web/src/lib/__tests__/workflow-api-roundtrip.test.ts`:

#### Test Coverage:
1. ✅ call_llm node with system_prompt, model, tools, tool_filter
2. ✅ Loop node with inline workflow containing call_llm
3. ✅ All node types (run, workflow, join, approval, save_message)
4. ✅ Workflow params with complex defaults
5. ✅ SaveMessage config with all fields
6. ✅ Thread config at all levels
7. ✅ Complex edges with cases and labels
8. ✅ Full get-shit-done workflow structure

All tests: **8 passed** (Vitest)

## How the System Works

### Data Flow

```
┌─────────────────┐
│ Database (YAML) │
└────────┬────────┘
         │ 1. Read
         ▼
┌──────────────────────────────────────────┐
│ parseDraftDefinitionWithPreservedNodeKeys│  ← FIX APPLIED HERE
│ - Recursively processes all nodes        │
│ - Moves unknown keys (model, etc.)       │
│   into args for round-trip               │
└────────┬─────────────────────────────────┘
         │ 2. v2.Workflow
         ▼
┌──────────────────┐
│ v2WorkflowToProto│
│ - Converts args  │
│   to proto inputs│
└────────┬─────────┘
         │ 3. Proto Workflow
         ▼
┌──────────────┐
│ API Response │
└──────────────┘
```

### Key Conversion Points

1. **YAML → v2.Workflow**: Unknown keys moved to `node.Args`
2. **v2.Workflow → Proto**: `node.Args` → `protoStep.Inputs` (map[string]*structpb.Value)
3. **Proto → JSON**: `structpb.Value` → JSON values
4. **Frontend**: JSON values → TypeScript types

### Known Node Keys

These keys are part of the v2.Node struct and should NOT be moved to args:

```go
var knownNodeKeys = map[string]bool{
    "id": true,
    "type": true,
    "command": true,
    "ref": true,
    "while": true,
    "inline": true,
    "condition": true,
    "args": true,      // ← This is where unknown keys go
    "timeout": true,
    "thread": true,
    "save_message": true,
    "presets": true,
    "project": true,
}
```

**Everything else** (model, system_prompt, tools, tool_filter, etc.) goes into `args`.

## Verification Steps

1. ✅ Backend tests pass
   ```bash
   cd internal/grpc/services && go test -v -run TestWorkflowRoundTrip
   ```

2. ✅ Frontend tests pass
   ```bash
   cd web && npm test -- workflow-api-roundtrip
   ```

3. 🔄 Manual API test (requires backend restart):
   ```bash
   curl 'https://localhost:9107/reliant.v1.WorkflowService/GetWorkflow' \
     --data-raw '{"projectId":"...","draftId":"..."}' \
     --insecure | jq '.workflow.nodes[0].inline.nodes[0].inputs'
   ```
   
   Should show: `model`, `system_prompt`, `tools`, `tool_filter` fields

## Related Files

- `internal/grpc/services/workflow.go` - Fix applied
- `internal/grpc/services/workflow_roundtrip_test.go` - Backend tests
- `web/src/lib/__tests__/workflow-api-roundtrip.test.ts` - Frontend tests  
- `web/src/lib/__tests__/workflow-serializer-roundtrip.test.ts` - Existing YAML tests
- `proto/reliant/v1/workflow.proto` - Proto definitions

## Impact

This fix ensures that **all workflow builder configurations survive round-trips** through:
- UI edits → Save → Reload
- Database → API → Frontend
- Nested inline workflows in loop nodes

Critical for:
- GSD (Get Shit Done) workflow
- Any workflow using call_llm with custom prompts
- Loop nodes with inline workflows
- Tool filtering and configuration

## Next Steps

1. ✅ Tests written and passing
2. 🔄 Backend restart required for changes to take effect
3. ✅ Fix is backward compatible (reads old data correctly)
4. 📝 Consider adding fuzzing tests for protobuf Value conversion
5. 📝 Consider migrating to v3 types throughout (mentioned in original issue)
