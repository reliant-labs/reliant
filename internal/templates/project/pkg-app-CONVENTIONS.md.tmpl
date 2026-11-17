# pkg/app conventions

`pkg/app` is a thin substrate: a test harness and migration support. The LIVE
runtime dependency-injection composition lives in **`internal/app`**, which the
generated `cmd/<bin>/cmd/serve.go` drives:

```
OpenInfra    (internal/app/providers.go — yours, opens DB/NATS/clients)
  → NewComponents (internal/app/compose.go — yours, EXPLICIT inline wiring)
  → mount/supervise via internal/app/mounts_services_gen.go + lifecycle.go
  → serverkit.Run
```

Construction is EXPLICIT: `compose.go` builds every handler/worker/operator as
a local value with its `Deps` wired inline off the owned `*Infra`, resolved by
field type — no string-keyed name match, no hidden construction order.

`compose.go` is YOURS. Forge writes it once, then reconciles only the
projection: a newly discovered component gets its import, `Components` field
and `New(Deps{…})` call injected, a component that gained a `Deps` field gets
that key filled, and a key naming a field its `Deps` struct no longer has is
dropped. The EXPRESSION beside a key that still exists, your construction
order, and any carved cross-edge are never touched. `forge project disown
internal/app/compose.go` stops reconciliation altogether.

## What lives under pkg/app

- `migrate.go` (generated) — embedded-migration support (`AutoMigrate`).
- `CONVENTIONS.md` (this file).

Note there is no `testing.go` here any more. It was a non-test `.go` file that
imported `testing`, and `cmd/<proj>` imports `pkg/app` — so every project
linked package `testing` (and the flags it registers in `init()`) into its
production binary. The harness now lives beside each component as a
`_test.go` file; see "Test wiring" below.

To change WHAT fills a component's `Deps` field, edit
`internal/app/providers.go` (the owned `Infra` + `OpenInfra`). A
collaborator `NewComponents` reports as "no provider" becomes a new field on
`Infra`, constructed in `OpenInfra`. To customize the composition by hand
(a carved cross-edge, a late-bound setter), `forge project disown
internal/app/compose.go` and own the bytes.

## Where do per-RPC nil checks belong? (Spoiler: nowhere.)

Forge generates `New(Deps) (Service, error)` and a `validateDeps()`
helper. Add your required fields to that single method:

```go
func (d Deps) validateDeps() error {
    if d.Logger == nil   { return fmt.Errorf("foo: Deps.Logger required") }
    if d.Config == nil   { return fmt.Errorf("foo: Deps.Config required") }
    if d.Repo == nil     { return fmt.Errorf("foo: Deps.Repo required") }
    return nil
}
```

Once `Repo` is in `validateDeps`, individual RPC handlers can use
`s.deps.Repo.Get(...)` directly — no `if s.deps.Repo == nil` preamble.
The validation runs once at construction; if it passes, the field is
non-nil for every RPC the service ever handles.

If a Dep really is optional (e.g. an audit sink allowed to be nil in
dev), keep it out of `validateDeps` and check at the one call site that
needs it (mark it `// forge:optional-dep` on the Deps field).

## Component config blocks (configuration ≠ collaborators)

Scalar Deps fields (`MaxPerTick int`, `Interval time.Duration`, ...) are
configuration, not collaborators. Declare a config block in
`proto/<project>/config/v1/config.proto` and take it as one typed field
(`Cfg *config.<Block>Config`, which compose.go fills from the loaded
config by type) instead of threading naked scalars through `Deps`. `forge lint --config-deps` flags naked-scalar Deps fields.

## Test wiring

Each component gets its own generated `helpers_gen_test.go`, in that
component's directory and package — e.g.
`internal/handlers/order/helpers_gen_test.go`, `package order`. It emits
`NewTest<Svc>(t, ...opts)`. Tests construct `Deps` directly (filling in only
the fields they care about; defaults flow in for the bare-Deps trio) and call
`New` — the production DI graph is bypassed in tests so each test stays
self-contained. Use `With<Svc>Deps(deps)` to override the rich deps.

Two properties of that placement are load-bearing:

- The `_test.go` **suffix** is what keeps package `testing` out of the
  production binary. The toolchain compiles these files only into the
  package's test binary, so `go list -deps ./cmd/<proj>` never contains
  `testing`.
- The **package clause** is the component's own (`package order`), not
  `order_test`. Go compiles a package's in-package `_test.go` files INTO the
  package under test, and an external `order_test` package then imports that
  augmented package — so the helpers are usable from BOTH internal and
  external test files in that directory, and from nowhere else.

Typed entity factories (`New<Entity>(t, db, …)`, a minimal valid row) and
create-request factories (`New<CreateRequest>(t, db, variant)`) land beside the
handler whose CRUD RPCs own that entity, as
`internal/handlers/<svc>/factories_gen_test.go` — regenerated from the applied
schema every run, same package clause, same visibility rule as the harness
above. Tests in that directory call `NewItem(t, db)` unqualified; a test
elsewhere drives the entity through its RPCs rather than seeding rows behind
them.

See the `testing/patterns` skill for the canonical patterns.
