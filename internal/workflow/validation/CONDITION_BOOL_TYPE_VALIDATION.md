# CEL Boolean Type Validation for Conditions

## Overview

This feature adds type validation to ensure that condition expressions (both edge conditions and node conditions) return boolean types. Previously, only syntax validation was performed, which could allow non-boolean expressions that would fail at runtime.

## What is Validated

### 1. Edge Conditions

Edge conditions must return `bool` type:

```yaml
edges:
  - from: call_llm
    cases:
      - to: tools
        condition: "size(nodes.call_llm.tool_calls) > 0"  # ✓ Valid: returns bool
      - to: done
        condition: "size(nodes.call_llm.tool_calls)"      # ✗ Invalid: returns int
```

### 2. Node Conditions

Node conditions (conditional node execution) must return `bool` type:

```yaml
nodes:
  - id: optional_step
    type: call_llm
    condition: "inputs.should_run"                        # ✓ Valid: returns bool
    model:
      tags: [flagship]
      
  - id: conditional_step
    type: call_llm
    condition: "inputs.count"                             # ✗ Invalid: returns int
    model:
      tags: [flagship]
```

## Examples

### Valid Conditions

```yaml
# Boolean field access
condition: "inputs.enabled"

# Numeric comparison
condition: "nodes.call_llm.input_tokens > 100"

# String comparison
condition: "nodes.call_llm.response_text == 'yes'"

# String emptiness check
condition: "nodes.call_llm.response_text != ''"

# Size/length check
condition: "size(nodes.call_llm.tool_calls) > 0"

# Logical operators
condition: "inputs.enabled && nodes.call_llm.input_tokens > 0"
condition: "inputs.retry_count > 3 || inputs.force_stop"

# Negation
condition: "!inputs.disabled"

# In operator
condition: "nodes.call_llm.response_text in ['yes', 'true', 'ok']"

# Null check
condition: "nodes.optional_step.message != null"

# has() function
condition: "has(nodes.optional_step.message)"
```

### Invalid Conditions (Will Cause Validation Errors)

```yaml
# Integer without comparison
condition: "size(nodes.call_llm.tool_calls)"
# Error: condition must return bool, but expression returns 'int'
# Suggestion: Use a comparison like: size(nodes.call_llm.tool_calls) > 0

# String field without comparison
condition: "nodes.call_llm.response_text"
# Error: condition must return bool, but expression returns 'string'
# Suggestion: Use a comparison like: nodes.call_llm.response_text != '' or nodes.call_llm.response_text == 'expected_value'

# Numeric field without comparison
condition: "nodes.call_llm.input_tokens"
# Error: condition must return bool, but expression returns 'int'
# Suggestion: Use a comparison like: nodes.call_llm.input_tokens > 0

# List/array without size check
condition: "nodes.call_llm.tool_calls"
# Error: condition must return bool, but expression returns 'list(dyn)'
# Suggestion: Check list size: size(nodes.call_llm.tool_calls) > 0
```

## Error Messages

When a condition doesn't return a boolean type, you'll see an error like:

```
Error: condition must return bool, but expression returns 'int'
Path: workflow.edges.[0].cases.[0].condition
Suggestion: Use a comparison like: size(nodes.call_llm.tool_calls) > 0
```

The error includes:
- **Type mismatch**: What type the expression actually returns
- **Path**: Where the invalid condition is in the workflow
- **Suggestion**: Context-aware suggestion for how to fix it

## Implementation Details

### How It Works

1. **CEL Compilation**: The validator compiles each condition expression using the CEL (Common Expression Language) compiler
2. **Type Inference**: After successful compilation, it retrieves the output type from the AST (Abstract Syntax Tree)
3. **Type Check**: Verifies that the output type is `bool`
4. **Error Reporting**: If not boolean, reports an error with a helpful suggestion

### Code Location

- Implementation: `internal/workflow/validation/cel.go`
  - `validateConditionReturnType()` - Main validation function
  - `generateBooleanSuggestion()` - Context-aware suggestion generator
- Tests: `internal/workflow/validation/condition_bool_type_test.go`

### When Validation Runs

This validation is part of `ValidateCELWithCompilation()`, which runs:
- During workflow parsing/loading
- Before workflow execution
- In development tools (linters, IDE plugins, etc.)

## Benefits

1. **Early Error Detection**: Catches type errors at validation time instead of runtime
2. **Better Error Messages**: Provides clear, actionable suggestions
3. **Improved Developer Experience**: Immediate feedback when writing workflows
4. **Runtime Safety**: Prevents runtime errors from unexpected non-boolean conditions

## Related

- CEL Expression Validation: `internal/workflow/validation/cel.go`
- CEL Environment Setup: `internal/workflow/validation/cel_env.go`
- Workflow Validation: `internal/workflow/validation/validator.go`