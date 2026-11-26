---
name: contracts/mocks
description: Using generated contract mocks in tests — func-field stubs, typed per-method call records, counts, generic call history and integration harnesses.
---

# Using contract mocks in tests

Mocks come from `mock_gen.go` — one per interface in the contract, so a dep interface has one too. They are drop-in for the real thing:

```go
// internal/user/service_test.go
package user

import (
    "context"
    "testing"

    "github.com/example/myproject/internal/email"
)

func TestCreateUser_SendsWelcomeEmail(t *testing.T) {
    // Set the func-field for the method you fake; unset methods
    // return "MockService.XxxFunc not set".
    mockEmail := &email.MockService{
        SendFunc: func(ctx context.Context, to, subject, body string) error {
            return nil
        },
    }

    svc := New(Deps{Email: mockEmail})
    _, err := svc.CreateUser(context.Background(), &usersv1.CreateUserRequest{
        Email: "alice@example.com",
    })

    assert.NoError(t, err)

    // Every call is recorded. SendCalls() returns them typed, one field
    // per parameter, named after the contract's parameters:
    calls := mockEmail.SendCalls()
    if len(calls) != 1 {
        t.Fatalf("expected 1 Send call, got %d", len(calls))
    }
    if calls[0].To != "alice@example.com" {
        t.Errorf("Send to = %q, want alice@example.com", calls[0].To)
    }
}
```

What each mock records, and how to read it back:

| Read | Returns | Use it for |
|---|---|---|
| `m.<Method>Calls()` | `[]Mock<Iface><Method>Call`, oldest first; `nil` when never called | asserting arguments by name: `calls[0].Lease.Epoch` |
| `m.CallCount("<Method>")` | `int` | counts and short-circuiting (`== 0`) |
| `m.Calls("<Method>")` | `[]contractkit.Call{Args []any, Time}` | ordering across methods (`Time`), or generic helpers over any method |

Prefer `<Method>Calls()` over `Calls("<Method>")[i].Args[n].(T)`: a positional assertion keeps compiling — and asserts the wrong argument — when the contract gains a parameter, while a field name breaks the test at compile time. The record's fields follow the contract method's parameters, in order and including the `context.Context` (`Ctx`), with the first letter upper-cased (`jobID` → `JobID`, a leading initialism whole: `id` → `ID`). An unnamed or `_` parameter is `P<index>`, and a variadic `...T` is a `[]T` field. Where a name is already taken — a contract method literally named `FooCalls`, or a package type already called `Mock<Iface><Method>Call` — the generated one gains a trailing `_` (`FooCalls_()`); `mock_gen.go` shows the final names.

For integration tests that need the real implementation, use the generated harness in that service's own `helpers_gen_test.go` (`internal/handlers/<svc>/`, `package <svc>`): `<pkg>.NewMigratedTestDB(t)` for a database with your migrations applied, then `<pkg>.NewTest<Svc>(t, <pkg>.WithDB(db))` for the real service wired to it (`<pkg>.With<Svc>Deps(...)` swaps one `Deps` field, `<pkg>.AuthedContext(t)` supplies claims). Typed factories live beside that same handler, in `internal/handlers/<svc>/factories_gen_test.go` (`package <svc>`), regenerated from the applied schema: a test in that directory calls `New<Entity>(t, db)` (a minimal valid row) or `New<CreateRequest>(t, db, 0)` (a create request with its FK parents seeded) unqualified.
