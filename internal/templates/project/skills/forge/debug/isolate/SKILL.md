---
name: isolate
description: Isolate bugs via TDD — top-down bisection from e2e to unit test.
---

# Isolate Bugs

## When to Use

The bug is suspected in a specific area and you need to pin down the exact function or line.

## Top-Down Bisection

1. **Broad**: start with an e2e or integration test that reproduces the bug
2. **Narrow**: create tighter integration tests for specific subsystems
3. **Pinpoint**: bisect down to unit tests for individual functions
4. **Goal**: find the smallest code unit where the bug manifests

```
task test:e2e                 # start broad
task test -- ./internal/handlers/<name>/...    # narrow to one service
task test -- -v ./internal/handlers/<name>/... # verbose output for details
```

## Mocking Strategy (Critical)

This is debugging, not normal testing — mocking rules are different:

- **Keep components REAL** if they might be causing the bug
- **Only mock** things clearly unrelated to the bug (external APIs, auth tokens)
- **When uncertain, keep it real** — you're trying to catch the bug, not hide it
- Each bisection step should mock ONE more thing to narrow the scope

## Red-Green Workflow

1. Write a failing test that demonstrates the bug (red)
2. Fix the code
3. Verify the test passes (green)
4. Run the full suite to check for regressions:

```
task test
```

## When Your Test Passes But the Bug Persists

Your test isn't testing the right thing. Fix by:
- Going broader — remove mocks, test a larger scope
- Checking your test inputs — are they realistic?
- Verifying you're hitting the same code path as production
- Using `forge debug start` to step through the actual execution

## After the Fix

Keep the test as a regression guard. It should live alongside related tests, not in a separate "bug fix" directory.
