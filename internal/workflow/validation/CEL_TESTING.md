# CEL Validation Testing Guide

## Test Files

### `cel_adversarial_test.go` — Adversarial Test Suite
Tests edge cases, boundary conditions, and tries to break validation.

**Categories**: False Positives (valid code rejected?), False Negatives (invalid code passes?), Type System Edge Cases, Null Handling, Complex Nesting, Builtin Workflow regression tests.

```bash
go test ./internal/workflow/validation/... -run TestCELAdversarial -v
```

### `cel_compilation_test.go` — Compilation Tests
```bash
go test ./internal/workflow/validation/... -run Compilation -v
```

### Other Test Files
- `cel_test.go` — Basic CEL validation tests
- `validator_test.go` — General validation tests
- `structure_test.go` — Structural validation tests

## Current Test Status

### What's Caught (Should Error)
✅ Type mismatches: `"string" + 5`, `bool + int`, `string == bool`
✅ Field typos: `nodes.llm1.respons_text` (should be `response_text`)
✅ Non-existent nodes: `nodes.nonexistent.field`
✅ Non-existent inputs: `inputs.typo`
✅ Wrong node output fields: `nodes.cmd1.response_text` (RunNode doesn't have this)
✅ Nested field typos: `inputs.config.databas.timeout` (should be `database`)
✅ Undefined nested fields: `inputs.config.database.unknown`
✅ Invalid field on primitives: `inputs.count.length` (type 'int' does not support field selection)
✅ Invalid methods: `inputs.name.push('x')` (undeclared reference to 'push')
✅ Null method calls: `null.toString()` (undeclared reference to 'toString')

### What's Allowed (Runtime Behavior)
⚠️ Object operations: `inputs.obj + inputs.obj`, `inputs.obj > inputs.obj` (allowed on `dyn` types, may fail at runtime)
⚠️ Division by zero: `5 / 0` (compiles but fails at runtime)

### What Should Warn (TODO)
⚠️ Unsafe conditional node access: `nodes.conditional_node.field` without null check — warning logic exists in regex-based validation but needs integration into compilation validation.

## Adding New Tests

### False Positive Test (Valid Code That Should Pass)

```go
{
    name: "your test name",
    yaml: `
name: test
entry: test
inputs:
  name:
    type: string
    default: "test"
nodes:
  - id: test
    type: call_llm
    model: {tags: [flagship]}
    messages:
      - role: user
        content: "{{your_expression_here}}"
`,
    desc: "description of what should work",
}
```

**Expected**: No errors, possibly warnings.

### False Negative Test (Invalid Code That Should Fail)

```go
{
    name: "your test name",
    yaml: `...`,
    desc: "description of what should error",
    shouldContain: "expected error substring",
}
```

### Type System Edge Case

```go
{
    name: "your test name",
    yaml: `...`,
    shouldError: true,  // or false if valid
    desc: "description of the edge case",
}
```

## Interpreting Test Results

**All Pass** ✅ — Validation working correctly.

**False Positive Failure** ❌ — Validation too strict, valid code rejected. **This is a bug — fix it.**

**False Negative Failure** ❌ — Validation too lenient, invalid code passes. Either a CEL limitation (document it, mark expected-to-fail) or a bug (fix it).

## Debugging Failed Tests

### 1. Run the specific test
```bash
go test ./internal/workflow/validation/... \
  -run "TestCELAdversarial_FalsePositives/your_test_name" -v
```

### 2. Check what CEL says — minimal repro
```go
env, _ := cel.NewEnv(cel.Variable("x", cel.IntType))
ast, issues := env.Compile("x.length")
```

### 3. Check validation code
- Is the expression being validated? (`validateNodeTemplatesWithCompilation`)
- Is the type provider returning the right types? (`cel_provider.go`)
- Are we reporting the error correctly? (`validateCELExpressionWithCompilationAndSchema`)

### Common Issues

**"Everything passes but should error"** — Expression isn't being validated. Check: Is the field a `{{...}}` string? Is `validateNodeTemplatesWithCompilation` finding it? Is the template regex matching?

**"Valid code errors"** — Type provider returning wrong type. Check: What type does the provider return? Is `NewCELEnv` configured correctly? Are we filtering errors correctly?

**"Builtin workflow fails"** — Real workflow hit a new edge case. Review if the error is valid (fix workflow) or a false positive (fix validation).

## When to Update Tests

- **New node types**: Add false positive + false negative tests for field access
- **Modified CEL environment**: Re-run all tests for regressions
- **Changed type provider**: Re-run false positive + false negative tests
- **User-reported bug**: Add reproducing test case, fix, verify
