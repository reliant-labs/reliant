---
name: migration-v0.1.47-harness-mocks
description: Upgrade v0.1.47 handler tests from the harness's Stub<Field> doubles to the forge Mock<Role> that replaces them — run forge fix harness-mocks, then update any assertion on the old stub error text.
relevance: migration
---

# v0.1.47 — one test double per interface: the harness installs forge's mocks

Use when a handler test names a `Stub<Field>` (`&chat.StubPause{...}`,
`StubRepo{...}`) and the field's interface has a forge `mock_gen.go`.

## What changed

The per-service test harness (`helpers_gen_test.go`) fills every required
interface-typed Deps field so `NewTest<Svc>(t)` constructs. It used to
synthesize a `Stub<Field>` for each one — even when forge already generated a
`Mock<Role>` for the same interface in a `mock_gen.go`. One collaborator had
two same-shape doubles under two names, and a test written against one could
not be handed the other.

Now the harness uses the mock whenever forge writes one:

| Deps field | mock the harness installs | where it lives |
|---|---|---|
| `Pause runs.PauseController` | `runs.MockPauseController` | `internal/runs/mock_gen.go` (from its `contract.go` or `forge:deps` `deps.go`) — the harness imports it |
| `Narrow Narrow`, declared in the handler's `forge:deps` `deps.go` | `MockNarrow` | the handler's own `mock_gen.go` |
| `Estimates db.EstimateStore` (internal/db mocks nothing) | `MockEstimateStore` | the handler's own `mock_gen.go` (a Deps-discovered mock) |
| `Repo Repository`, declared in `service.go` | `StubRepo` — unchanged | `helpers_gen_test.go` |
| `Temporal client.Client` (third-party) | `StubTemporal` — unchanged | `helpers_gen_test.go` |

The declaring package's mock wins over a Deps-discovered copy in the handler,
so one interface has one mock name in every consumer's tests. `Stub<Field>`
remains only for an interface no `mock_gen.go` covers: the standard library's,
a third-party module's, or a handler-local one declared outside a `forge:deps`
`deps.go`. A `// forge:optional-dep` field is still left nil by
`NewTest<Svc>`; a test of its non-nil path passes the mock.

`forge generate --steps harness` now runs the contract-mock step first: a Deps
edit can add a Deps-discovered mock, and the harness must install the same
double the full run does.

### The unset-method error

Both doubles return a non-nil error from an error-returning method the test
did not set — never `(nil, nil)` — and zero values from one with no error
result. Only the text moves:

| | before | after |
|---|---|---|
| function | `testkit.StubNotConfigured` | `contractkit.MockNotSet` |
| message | `StubPause.PauseFunc not set: forge test stub not configured — …` | `MockPauseController.PauseFunc not set` |

## What to do

1. Run `forge fix harness-mocks` (`forge project upgrade` runs it for you;
   `--dry-run` lists the renames, `--scope internal/handlers/<svc>` limits
   them). For every handler it renames each retired `Stub<Field>` in the
   package's test files to the mock — bare (`MockNarrow`) or through the
   handler's import (`chat.MockNarrow`) for the handler's own mock, through
   the file's existing import of the declaring package for a foreign one
   (`rr.MockPauseController` where the file imports it as `rr`), adding
   that import where the file has none. A test struct that embeds a stub
   (`struct{ *chat.StubRepo; ... }`) has the embedded field renamed with it —
   its composite-literal key and its selectors (`StubRepo:` →
   `MockRepository:`). The `<Method>Func` fields are the same on both
   doubles, so nothing else in the test changes.
2. Fix what it lists under "next": a string literal naming the old stub's
   error (`"StubPause.PauseFunc not set"`) — assert on the mock's
   (`"MockPauseController.PauseFunc not set"`), or better, on the handler's
   error code. An import alias already taken in that file for another package
   is also listed: import the declaring package under a free name and qualify
   the mock with it.
3. `forge generate` (rewrites `helpers_gen_test.go` without the retired
   stubs), then `go vet ./internal/handlers/...` and `task test`.

A test that asserted the stub's behaviour beyond its error text has nothing to
change: the mock records every call (`CallCount`, `Calls`) through the same
embedded `contractkit.Recorder`.
