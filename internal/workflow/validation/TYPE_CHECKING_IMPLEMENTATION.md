# Generic CEL Output Type Validation

## Overview

Implemented generic CEL output type validation for workflows. This validates that CEL expressions produce output matching expected destination types. This is NOT response_tool specific - it works for ANY CEL expression going into a typed field.

## What It Does

When a user writes:
```yaml
save_message:
  tool_results: "{{some_cel_expression}}"
```

The validator now checks that `some_cel_expression` produces `[]ToolResult` at compile time.

## Implementation

### Files Created

1. **`internal/workflow/validation/type_check.go`** (~390 lines)
   - Core type checking logic
   - `CheckTypeCompatibility()` - Checks if actual vs expected types are compatible
   - `GetExpectedFieldType()` - Returns expected type for node input fields
   - `InferCELOutputType()` - Compiles CEL and infers output type
   - `FormatTypeError()` - Creates user-friendly error messages

2. **`internal/workflow/validation/type_check_test.go`** (~450 lines)
   - Comprehensive test coverage
   - Tests for exact matches, slice types, dynamic types, mismatches
   - Integration tests for tool_results, manual construction, conditional logic
   - All tests passing ✅

### Files Modified

3. **`internal/workflow/validation/cel.go`**
   - Integrated type checking into `validateSaveMessageCEL()`
   - Added `validateCELOutputType()` function
   - Added `buildValidationCELEnv()` helper
   - Added `ext` import for CEL native types

## Type Compatibility Rules

1. **Exact match** → compatible
2. **Dynamic actual** → compatible (CEL couldn't infer, allow at runtime)
3. **Nil/optional actual for optional expected** → compatible
4. **Element types must match for slices/maps**
5. **Mismatch** → incompatible with helpful error message

## Supported Fields

Currently validates these SaveMessageConfig fields:

| Field | Expected Type | Example |
|-------|--------------|---------|
| `tool_results` | `[]ToolResult` | `{{nodes.tools.tool_results}}` |
| `tool_calls` | `[]ToolCall` | `{{nodes.llm.tool_calls}}` |
| `attachments` | `[]string` | `{{inputs.attachments}}` |
| `role` | `string` | `"assistant"` |
| `content` | `string` | `{{nodes.llm.message.content}}` |

## Examples

### ✅ Valid: Correct Type

```yaml
nodes:
  - id: tools
    type: execute_tools
    tool_calls: "{{nodes.llm.tool_calls}}"
    save_message:
      role: assistant
      tool_results: "{{nodes.tools.tool_results}}"  # ✅ []ToolResult
```

### ❌ Invalid: Type Mismatch

```yaml
nodes:
  - id: llm
    type: call_llm
    save_message:
      role: assistant
      tool_results: "{{nodes.tools.tool_results[0].content}}"  # ❌ string, not []ToolResult
```

**Error:**
```
Type mismatch for 'tool_results': element type mismatch: expected message.ToolResult, got string
  Suggestion: ensure expression produces message.ToolResult elements
  Expected: []ToolResult
  Actual: string
```

### ✅ Valid: Filtered Array

```yaml
save_message:
  tool_results: "{{nodes.tools.tool_results.filter(r, !r.is_error)}}"  # ✅ Still []ToolResult
```

### ✅ Valid: Manual Construction

```yaml
save_message:
  tool_results: "{{[{tool_call_id: 'x', name: 'y', content: 'z', is_error: false}]}}"  # ✅ Array literal
```

### ✅ Valid: Dynamic Type (Runtime Check)

```yaml
save_message:
  tool_results: "{{response_data.tool_results}}"  # ✅ Dynamic, allowed
```

## Test Results

All tests passing:

```bash
$ go test ./internal/workflow/validation/... -v
...
=== RUN   TestCheckTypeCompatibility_ExactMatch
--- PASS: TestCheckTypeCompatibility_ExactMatch (0.00s)
=== RUN   TestCheckTypeCompatibility_SliceTypes
--- PASS: TestCheckTypeCompatibility_SliceTypes (0.00s)
=== RUN   TestCheckTypeCompatibility_DynamicAllowed
--- PASS: TestCheckTypeCompatibility_DynamicAllowed (0.00s)
=== RUN   TestGetExpectedFieldType_SaveMessage
--- PASS: TestGetExpectedFieldType_SaveMessage (0.00s)
=== RUN   TestInferCELOutputType_Simple
--- PASS: TestInferCELOutputType_Simple (0.00s)
=== RUN   TestIntegration_ToolResultsValidation
--- PASS: TestIntegration_ToolResultsValidation (0.00s)
=== RUN   TestIntegration_ManualConstruction
--- PASS: TestIntegration_ManualConstruction (0.00s)
=== RUN   TestIntegration_ConditionalLogic
--- PASS: TestIntegration_ConditionalLogic (0.00s)
...
ok  	github.com/reliant-labs/reliant/internal/workflow/validation	0.327s
```

## Future Extensions

This framework can be easily extended to:

1. **Other node types** - Add expected types for execute_tools, call_llm inputs
2. **Transform nodes** - Validate typed transform outputs
3. **Custom type mappings** - Add struct tags like `expects:"[]ToolResult"`
4. **Deep object validation** - Validate object field types in constructed objects

## Benefits

1. **Catch errors at validation time** instead of runtime
2. **Better error messages** with type information and suggestions
3. **Type-safe workflows** for critical fields like tool_results
4. **Generic infrastructure** that works for any CEL → typed field
5. **No runtime overhead** - only runs during validation

## Design Decisions

### Why mapping table instead of struct tags?

For now, we use a simple mapping table (`getSaveMessageExpectedType()`) because:
- Clearer and easier to understand
- These fields are string templates in Go but have runtime types
- Easy to extend without modifying structs
- Can be moved to struct tags later if needed

### Why allow dynamic types?

CEL can't always infer types statically (e.g., response_data access, dynamic expressions). Rather than fail validation, we allow dynamic types and rely on runtime checks. This balances safety with flexibility.

### Why only validate single {{expr}} templates?

String interpolation like `"prefix {{expr}} suffix"` can't be type-checked meaningfully since the result is always a string. We only validate when the entire field value is a single CEL expression.

## Verification

Build and test:
```bash
go build ./internal/workflow/...
go test ./internal/workflow/validation/... -v
```

All tests passing ✅