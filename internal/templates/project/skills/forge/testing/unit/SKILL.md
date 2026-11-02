---
name: unit
description: "Unit tests — hermetic, fast, handler-level tests using generated mocks."
---

# Unit Tests

## Principles

Unit tests are **hermetic**: no network, no database, no filesystem. They test a single handler or function in complete isolation using generated mocks for all contract dependencies.

## Running

```bash
task test:short -- ./internal/handlers/<name>/...  # inner loop: after each edit
task test                  # all unit tests + frontends, race on: before you finish
task test -- ./internal/handlers/<name>/...      # full lane for one service
task test -- -v ./...                    # verbose output for debugging
```

Iterate on `task test:short`, scoped to what you touched: it is `-short`,
cached, and race-free, so an unchanged package costs nothing. Don't add
`-count=1` or `-race` to it — those belong to `task test` and CI.

## Test Naming Convention

Follow the pattern: `TestHandlerName_Scenario_ExpectedOutcome`

```go
func TestCreateUser_DuplicateEmail_ReturnsAlreadyExists(t *testing.T) { ... }
func TestGetOrder_NotFound_ReturnsNotFoundError(t *testing.T) { ... }
```

## Table-Driven Tests

Use table-driven tests for edge cases — this is idiomatic Go:

```go
tests := []struct {
    name    string
    input   *pb.Request
    wantErr connect.Code
}{
    {"empty name", &pb.Request{Name: ""}, connect.CodeInvalidArgument},
    {"valid",      &pb.Request{Name: "ok"}, 0},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) { ... })
}
```

## Using Generated Mocks

Dependencies are defined as interfaces in your handler contracts. Mock implementations are generated in `mock_gen.go` files — don't hand-write stubs when the contract mock already exists. Inject them during test setup, never construct real clients.

**Mock collaborators, never the subject.** Mock the handler's *dependencies*; the handler itself stays real. A unit test that stubs the thing it claims to test asserts only that your mock returned what you told it to.

For the table-driven scaffolding around these mocks (terse construction, RPC case tables, authed contexts), use the `pkg/tdd` + `pkg/testkit` helpers rather than wiring it by hand — see `testing/patterns`.

## Testing Error Paths

Always verify the correct Connect error code is returned, not just that an error occurred:

```go
_, err := handler.CreateUser(ctx, req)
require.Error(t, err)
assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
```

## Rules

- **Don't test generated code** in `gen/` — it's regenerated and not your logic
- **Don't `t.Skip()` failing tests** without linking an issue reference in the skip message
- **Don't over-mock** — if you're mocking 5+ dependencies, the unit is too big

## Common Pitfalls

- **Testing mocks instead of code** — asserting that your mock returned what you told it to
- **Over-mocking** — mocking things that are fast and deterministic; just use the real thing
- **Assertions that always pass** — e.g., checking `err != nil` when the mock can't fail
