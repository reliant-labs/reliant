# CEL Validation Testing Guide

This document explains the CEL validation test suite and how to use it to prevent regressions and find new bugs.

## Test Files

### `cel_adversarial_test.go` - Adversarial Test Suite
**Purpose**: Find bugs by testing edge cases, boundary conditions, and trying to break validation.

**Test Categories**:
1. **False Positives** - Valid expressions that should pass but might be rejected
2. **False Negatives** - Invalid expressions that should fail but might pass
3. **Type System Edge Cases** - Unusual type operations
4. **Null Handling** - Null safety and optional chaining
5. **Complex Nesting** - Deep object field access
6. **Builtin Workflows** - Regression testing on real workflows

**When to Run**: Before any release, after modifying CEL validation code.

**Run Command**:
```bash
go test ./internal/workflow/validation/... -run TestCELAdversarial -v
```

### `cel_compilation_test.go` - Compilation Tests
**Purpose**: Test that CEL expressions are compiled and type-checked correctly.

**Run Command**:
```bash
go test ./internal/workflow/validation/... -run Compilation -v
```

### Other Test Files
- `cel_test.go` - Basic CEL validation tests
- `validator_test.go` - General validation tests
- `structure_test.go` - Structural validation tests

## Adding New Tests

### Adding a False Positive Test (Valid Code That Should Pass)

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

### Adding a False Negative Test (Invalid Code That Should Fail)

```go
{
    name: "your test name",
    yaml: `...`,
    desc: "description of what should error",
    shouldContain: "expected error substring",
}
```

**Expected**: At least one error containing the specified substring.

### Adding a Type System Edge Case

```go
{
    name: "your test name",
    yaml: `...`,
    shouldError: true,  // or false if it's valid
    desc: "description of the edge case",
}
```

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

### What Should Warn (Future Enhancement)
⚠️ Unsafe conditional node access: `nodes.conditional_node.field` without null check

This is a TODO - the warning logic exists in regex-based validation but needs integration into compilation validation.

## Interpreting Test Results

### All Pass ✅
```
--- PASS: TestCELAdversarial_FalsePositives (0.00s)
--- PASS: TestCELAdversarial_FalseNegatives (0.00s)
```
**Meaning**: Validation is working correctly. No false alarms, catching all catchable errors.

### False Positive Failures ❌
```
--- FAIL: TestCELAdversarial_FalsePositives/some_test (0.00s)
    cel_adversarial_test.go:XXX: FALSE POSITIVE: description
    cel_adversarial_test.go:XXX:   Got 1 errors:
    cel_adversarial_test.go:XXX:     - [category] error message
```
**Meaning**: Validation is too strict. Valid code is being rejected. **This is a bug - fix it!**

### False Negative Failures ❌
```
--- FAIL: TestCELAdversarial_FalseNegatives/some_test (0.00s)
    cel_adversarial_test.go:XXX: FALSE NEGATIVE: description
    cel_adversarial_test.go:XXX:   Expected error but validation passed
```
**Meaning**: Validation is too lenient. Invalid code is passing. Check if this is a CEL limitation or a bug.

**Action**:
1. If it's a CEL limitation (e.g., `int.field`), document it and mark the test as expected to fail
2. If it's a bug in our validation, fix it

### Edge Case Failures ❌
Review each failure individually:
- Is this a CEL limitation? → Document it
- Is this a bug in our validation? → Fix it
- Is the test wrong? → Update the test

## Debugging Failed Tests

### 1. Run the Specific Test
```bash
go test ./internal/workflow/validation/... \
  -run "TestCELAdversarial_FalsePositives/your_test_name" -v
```

### 2. Check What CEL Says
Create a minimal test case:
```go
package main

import (
    "fmt"
    "github.com/google/cel-go/cel"
)

func main() {
    env, _ := cel.NewEnv(
        cel.Variable("x", cel.IntType),
    )
    
    ast, issues := env.Compile("x.length")
    if issues.Err() != nil {
        fmt.Printf("Error: %v\n", issues.Err())
    } else {
        fmt.Printf("Success! Type: %v\n", ast.OutputType())
    }
}
```

### 3. Check Validation Code
- Is the expression being validated? (Check `validateNodeTemplatesWithCompilation`)
- Is the type provider returning the right types? (Check `cel_provider.go`)
- Are we reporting the error correctly? (Check `validateCELExpressionWithCompilationAndSchema`)

## Common Issues

### "Everything Passes But Should Error"
**Likely Cause**: Expression isn't being validated at all.

**Check**:
1. Is the field a string containing `{{...}}`?
2. Is `validateNodeTemplatesWithCompilation` finding it via reflection?
3. Is the template regex matching it?

### "Valid Code Errors"
**Likely Cause**: Type provider returning wrong type or too strict error reporting.

**Check**:
1. What type is the type provider returning? (Add debug logs)
2. Is CEL compilation correctly configured? (Check `NewCELEnv`)
3. Are we filtering errors correctly? (We only report certain error types)

### "Builtin Workflow Fails"
**Likely Cause**: Real workflow hit a new edge case.

**Action**:
1. Review the error - is it valid?
2. If yes, this is a bug in the workflow - fix the workflow
3. If no, this is a false positive - fix validation
4. Add a test case to prevent regression

## Best Practices

### DO:
✅ Add a test case for every bug you find  
✅ Test both positive (should pass) and negative (should fail) cases  
✅ Document CEL limitations clearly  
✅ Run adversarial tests before merging validation changes  
✅ Keep tests simple and focused (one concept per test)  

### DON'T:
❌ Delete failing tests without investigation  
❌ Mark tests as expected-to-fail without understanding why  
❌ Add tests that are flaky or timing-dependent  
❌ Test runtime behavior (this is compile-time validation)  
❌ Assume CEL catches everything (it has limitations)  

## Maintenance

### When to Update Tests

**After Adding New Node Types**:
- Add false positive tests for valid field access
- Add false negative tests for invalid field access

**After Modifying CEL Environment**:
- Re-run all tests to check for regressions
- Update tests if new CEL features change behavior

**After Changing Type Provider**:
- Re-run false negative tests (should catch more errors)
- Re-run false positive tests (should not reject valid code)

**After User Reports Bug**:
- Add a test case reproducing the bug
- Fix the bug
- Verify the test now passes

### Quarterly Review
Every quarter, review the test suite:
1. Are all tests still relevant?
2. Are there new edge cases to test?
3. Have CEL updates changed behavior?
4. Are documented limitations still limitations?

## Getting Help

### Test Failing?
1. Read the test description
2. Check if it's a known CEL limitation (see "What's Not Caught" above)
3. Debug using steps in "Debugging Failed Tests"
4. Ask the team if still unclear

### Adding New Feature?
1. Write tests first (TDD)
2. Add both positive and negative test cases
3. Run full test suite
4. Document any new limitations

### Found a Bug?
1. Write a minimal reproduction test
2. Verify it fails (confirms the bug)
3. Fix the bug
4. Verify the test passes
5. Add to adversarial test suite

---

**Remember**: The goal is comprehensive validation without false positives. When in doubt, err on the side of allowing valid code and catching errors at runtime rather than blocking users with false alarms.
