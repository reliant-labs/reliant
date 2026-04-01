# Output Type Validation

## Overview

Workflow output expressions are now type-checked at compile time. The validation ensures that:

1. **Syntax is valid**: Output CEL expressions compile successfully
2. **Types are known**: Output expressions return a concrete type (not `dyn`)

## Validation Levels

### ✅ Valid - Known Types

Output expressions that return concrete types pass validation without warnings:

```yaml
name: example
entry: llm
nodes:
  - id: llm
    type: call_llm
    args:
      model: claude-4-sonnet

outputs:
  # ✅ String type - inferred from nodes.llm.message.text
  result: "{{nodes.llm.message.text}}"
  
  # ✅ Integer type - size() function returns int
  count: "{{size(nodes.llm.tool_calls)}}"
  
  # ✅ Boolean type - comparison returns bool
  is_done: "{{nodes.llm.stop_reason == 'end_turn'}}"
```

### ❌ Error - Unknown Field on Known Type

Accessing a non-existent field on a known type (e.g., `CallLLMOutput`) produces an **error**:

```yaml
outputs:
  # ❌ ERROR: 'unknown_field' does not exist on CallLLMOutput
  data: "{{nodes.llm.unknown_field}}"
```

The CEL type provider knows the structure of `CallLLMOutput` and validates field access.

### ⚠️  Warning - Dynamic Type

When the output type cannot be determined at compile time, a **warning** is issued:

```yaml
outputs:
  # ⚠️  WARNING: nodes.child is dyn type (external workflow outputs unknown)
  result: "{{nodes.child.some_field}}"
```

This occurs when:
- Accessing fields on external workflow references (outputs not known at compile time)
- Using dynamic expressions where the type cannot be inferred

## Why This Matters

### Parent Workflow Type Safety

When a workflow's outputs are used by a parent workflow, type validation ensures compatibility:

```yaml
# child.yaml
name: process-data
outputs:
  count: "{{size(nodes.processor.items)}}"  # ✅ Returns int

# parent.yaml
name: orchestrator
nodes:
  - id: child
    type: workflow
    ref: process-data
  
  # Parent can safely use child outputs with type checking
  - id: check
    type: call_llm
    system_prompt: "Processed {{nodes.child.count}} items"
```

### Early Error Detection

Catch type errors at compile time instead of runtime:

```yaml
outputs:
  # ❌ Caught at compile time
  bad: "{{nodes.llm.mesage.text}}"  # Typo: 'mesage' vs 'message'
```

## Implementation Details

### Type Inference

Output types are inferred by compiling CEL expressions and inspecting their `OutputType()`:

```go
inferredTypes, inferErrors := validation.InferOutputTypes(outputExprs, env)

for name, fieldInfo := range inferredTypes {
    if fieldInfo != nil && fieldInfo.IsDynamic {
        // Warn about dynamic type
    }
}
```

### Known vs Dynamic Types

- **Known types**: Concrete types like `string`, `int`, `bool`, or structured types like `CallLLMOutput`
- **Dynamic types**: The `dyn` type when CEL cannot determine the concrete type at compile time

### Integration with CEL Type Provider

The validation uses the workflow-aware CEL type provider (`WorkflowTypeProvider`) which:

1. Registers all node output types (e.g., `CallLLMOutput`, `ExecuteToolsOutput`)
2. Validates field access against the registered types
3. Returns `dyn` for nodes with unknown outputs (e.g., external workflow refs)

## Testing

See `output_validation_test.go` for comprehensive test cases covering:

- Valid outputs with known types (string, int, bool)
- Errors for unknown fields on known types
- Warnings for dynamic types from external refs
- Type inference accuracy

## Future Enhancements

Possible future improvements:

1. **Cross-workflow type propagation**: Load external workflow definitions to infer their output types
2. **Type compatibility checking**: When parent workflows expect specific types from child workflows
3. **Generic type parameters**: Support for parameterized workflows with type variables
