---
name: contracts
description: Using forge's contract system — mark the implementing struct with //forge:contract (forge derives the Service interface), or hand-write contract.go for strategy interfaces; what forge generate produces, integrating with tests.
---

# Forge Contracts

Internal Go packages in a forge project expose a Go interface as their contract. The contract is the public surface of the package; everything else is implementation detail. From the contract `forge generate` produces the test seam (mocks) and the cross-cutting middleware (logging, tracing, metrics) for free.

There are two ways to declare it. For the usual single-implementation package you **mark the implementing struct** with `//forge:contract` and forge derives the interface from its exported methods, so you never write it twice. For a strategy interface, where several implementations satisfy one abstraction, you **hand-write `contract.go`**. Both feed the same mock and decorator generation.

Applies to greenfield packages and ports alike.

## The marker form: `//forge:contract` on the struct

```go
// internal/email/service.go
package email

import "context"

// service sends transactional email.
//
//forge:contract
type service struct{ deps Deps }

func New(deps Deps) (Service, error) { return &service{deps: deps}, nil }

// Send delivers one message.
func (s *service) Send(ctx context.Context, to, subject, body string) error { /* ... */ }
```

`forge generate` writes **`contract_gen.go`** (forge-owned; never edit it):

- `type Service interface` holding every exported method *declared on* the marked type, pointer and value receivers alike. Unexported methods are skipped, and methods promoted from embedded fields are not included.
- each method's doc comment copied across, so a method-level `// forge:no-observe` on the implementation still routes that method around the observe chain; the type's doc prose becomes the interface doc.
- imports resolved from the files declaring the methods (aliases kept), plus `var _ Service = (*service)(nil)`.

`mock_gen.go` and `middleware_gen.go` are then generated from it exactly as from a hand-written `contract.go`. The derived interface is always named `Service`. Add or remove an exported method and the interface, mock and decorator follow on the next `forge generate`; remove the marker and `contract_gen.go` is removed with it. A package-level `// forge:no-observe` can sit on the marked type's doc, as it can on a `Service` interface doc.

**Dependency interfaces stay hand-declared, consumer-side.** Put them in `deps.go`, whose first line is `// forge:deps — consumer-declared interfaces for this package's Deps.` (the file `forge fix narrow-deps` writes), or in a `contract.go` kept beside the marked type. They are mocked together with `Service` (`MockStore`, …).

Refused, by `forge generate` and by `forge lint` (`forgeconv-internal-package-contract-names`): two marked types in one package; a marker on an alias or a generic type; a marked type named `Service`; a package that has both a hand-written `Service` (or a `//forge:contract` / `//forge:service`-marked interface) and a marked type — pick one form per package.

`forge scaffold package <name>` writes this shape: `service.go` with `Deps`, the marked `service` struct and `New`, and no `contract.go`. An existing single-implementation package migrates with `forge fix derive-contract <package-dir>`, which deletes the hand-written `Service`, marks the implementing type and moves leftover dependency interfaces to `deps.go`. It refuses when `Service` has no implementation or several, embeds interfaces, or the implementation exports methods outside `Service`.

`//forge:contract` on an **interface** keeps its old meaning: that interface is the package contract under a non-canonical name.

## When does a hand-written `contract.go` belong?

Three categories of internal package, three answers:

| Category | Contract | Why |
|----------|----------|-----|
| **Single-impl service** (DB-backed, calls external API, holds state) | Marked struct (`//forge:contract`) | Test seam + auto-generated middleware/tracing/metrics. The mock is the value; the interface is derived, not written. |
| **Multi-impl with strategy pattern** (pluggable backends — local vs S3 storage, JWT vs Clerk auth) | Hand-written `contract.go` | The interface IS the abstraction; multiple implementations satisfy it, so it cannot be derived from one of them. Adapter, client and eventbus scaffolds still write `contract.go`. |
| **Pure utility** (string formatters, math helpers, no I/O / time / state) | Optional — see "Pure-utility packages" below | Nothing meaningful to mock. |

The lane needs no strictness settings. `requirecontract` requires a `contract.go` or `//forge:contract` type in root `internal/` packages with exported struct methods, subject to the exemptions below; exported free functions alone do not trigger it. `contracts.exclude` is still supported. Below, "`contract.go`" lint behaviour also applies to marked types.

It judges a package's API, which is its non-test, hand-written source. Methods
and vars in `_test.go` files are never flagged, so a test fake that satisfies a
contract interface may carry whatever helpers the test needs. Generated files
are skipped too.

A type that implements `Service` (or a `//forge:contract` interface) may export
only the contract's methods. An interface that `contract.go` declares only as a
`Deps` field's type is a requirement, not part of the contract, so a type that
satisfies nothing but such seams keeps its own API. A registry can satisfy a
`Get`-only seam your `Deps` takes and still export `Register` for the
composition root.

## Run the contract lane

```bash
forge lint --contract
```

The rules are not configurable by severity. `forge.yaml` takes only two contract keys, both escape hatches; whether the lane runs at all is `features.contracts`:

```yaml
contracts:
  exclude:                  # paths that opt out of every contract rule
    - "internal/buildinfo"
  interface_types:          # project-local interfaces a contract method returns
    - "billing.MeterClient"
```

`strict`, `allow_exported_vars` and `allow_exported_funcs` are gone: no analyzer ever read them, and forge's `forge.yaml` validation reports each one as a removed key, with what to do instead. Add the lane to your per-phase gate (below).

### Scope: which packages need a contract

`requirecontract` covers the module's **root `internal/` tree** (`internal/<pkg>`, nested packages included). That is the tree forge composes and generates contract code for: the derived `Service`, `mock_gen.go` and the decorators.

It does not cover:

- `pkg/<name>/`, library code (utilities, clients, shared types). A contract is optional there.
- a **nested** `internal/` (`pkg/<sdk>/internal/<x>`, `cmd/<tool>/internal/<x>`). Go makes it private to its parent, forge never composes it, and a `contract.go` written there gets no mock or decorator. The private plumbing of a public SDK under `pkg/` is library code, like the SDK.
- the forge-shaped kinds: handlers, the `internal/app` seam, workers, operators and the ORM package.

The single-seam rule (`contract`: a type implementing a `contract.go` interface exports only its methods) and `exportedvars` run on every package the lane scans, `pkg/` included.

### Package markers: which one, when

| Marker | The server… | Mock | Use when |
|---|---|---|---|
| *(none)* | constructs it in `compose.go` | yes | the default |
| `//forge:external-component` | has it, built by hand in `providers.go` | yes | the server needs it, you build it |
| `//forge:not-a-component: <why>` | does not have it | yes | only the CLI, a daemon or another binary builds it |
| `//forge:exclude-contract: <why>` | — no contract at all | **no** | pure functions, test support, API types, strategy registries |

A bare marker (no reason) is a lint error. `exclude-contract` is refused on packages that make outbound calls or have an interface with two or more implementations. A server component that requires a `not-a-component` package fails `forge generate`. Both spellings, `//forge:` and `// forge:`, are accepted; prefer `//forge:`. **Load `contracts/package-markers`** for the full rules, the refusals and their fixes, and how gofmt places `//forge:` directives.

## Suppressing a contract or convention finding

Every forge rule, including `forgeconv-internal-package-contract-names`, `forgeconv-deps-are-interfaces`, `forgeconv-one-service-per-file`, `forgeconv-list-filter-optional` and `forgeconv-create-request-nullability` (the last three in a `.proto`), takes the same directive:

```go
type Deps struct {
    //forge:lint-disable-next-line forgeconv-deps-are-interfaces: token bucket is pure in-memory state; nothing to fake
    Limiter *Limiter
}
```

Other forms: `forge:lint-disable-file <rule>: <why>` for a whole file, a `forge:lint-disable` … `forge:lint-enable` block, or golangci's `//nolint:<rule> // <why>`. **A reason is required** for an error-severity rule. A reasonless suppression still silences the finding, but it is reported as `forge-suppression-missing-reason`, which is itself an error. Warnings need no reason.

## In a Go repo that is not a forge project

`forge lint` works in any Go module, including one with no `forge.yaml`. The contract discipline still applies there, but the codegen it pays for does not: without `forge generate` there is no bootstrap, no `mock_gen.go` and no decorator. So the three rules whose **error** severity exists to protect that codegen report as **warnings** there, and the message says why:

| Rule | Why it gates in a forge project | Without forge.yaml |
|---|---|---|
| `forge-exclude-contract-outbound-io` | the boundary loses its generated mock and decorator | warning |
| `forge-exclude-contract-multi-impl` | the interface loses its generated mock | warning |
| `forgeconv-internal-package-contract-names` | a non-canonical contract breaks the generated bootstrap | warning |

`forge-exclude-contract-reason` and `forgeconv-deps-are-interfaces` keep gating everywhere. A reason costs one line, and faking a dependency behind an interface does not need codegen. `forge lint --strict` restores all of them to errors, for a non-forge repo that wants the full gate.

You do **not** need the marker for a package with no interfaces at all (constants, structs, top-level funcs). Those are skipped automatically: a package that declares no interface cannot declare `Service`, and the rule works that out for itself. Other lint rules still apply either way.

## Prefer narrow per-aggregate interfaces over wide facades

The contract pattern works best when `Service` is **focused** — one cohesive responsibility, methods that belong together by domain. A 100+ method `Repository` interface (one struct exposing every DB operation in the system) defeats the pattern:

- Every consumer depends on every method, even the ones it doesn't use.
- Mocks become unusable in tests — you can't easily fake just the 3 methods a handler calls.
- `interfacebloat` will fire, correctly.

The remedy depends on your shape:

| Source shape | Port-time fix |
|---|---|
| Hand-written wide `Repository` interface + a concrete `*PostgresRepository` that satisfies it | Drop the wide `Repository`. Define narrow per-aggregate interfaces (`UserRepository`, `OrgRepository`, `BillingRepository`). The same concrete `*PostgresRepository` satisfies all of them structurally — Go does that for free. Each handler depends on the 10-method interface it actually needs. |
| Multi-aggregate package with wide internal surface | Split into per-aggregate packages: `internal/db/user/`, `internal/db/org/`, `internal/db/billing/`. Each gets its own narrow `contract.go`. Larger refactor; cleanest long-term. |
| A query generator's `Queries` struct carried over from the source (e.g. sqlc: one method per query, all in one struct) | Transitional only, since forge does not run sqlc. Port the queries onto the generated `<Entity>Store` and `internal/db/<entity>_repo_ext.go` (see `db/generated-orm`). Until each aggregate moves, path-exclude the generated dir with a note naming that port. It is the one case where `interfacebloat` is a false positive: the generator's output shape is not yours to split. |
| Wide interface but only one or two callers | Either narrow it inline (delete the methods nobody calls) or split per-caller. Don't keep a 50-method interface that two functions use. |

Design for the *callers*, not the implementation. Have the split-or-narrow conversation at port time, not after lint failures pile up.

## Writing a `contract.go` (strategy interfaces)

Reach for the hand-written form when the interface is the abstraction: several implementations, or a seam you want to read as a document. For a single implementation, prefer the marker form above. One file per package, defining one (or a few cohesive) interfaces. Keep methods focused — every method becomes a mock entry, a middleware wrapper, a tracing span.

```go
// internal/email/contract.go
package email

import "context"

type Service interface {
    Send(ctx context.Context, to, subject, body string) error
    SendTemplated(ctx context.Context, to, templateID string, data map[string]any) error
}
```

Naming convention: **`Service` for single-impl, `<Name>er` for multi-impl** (`Storer`, `Authenticator`, `Validator`). Forge templates assume `Service` for the canonical case.

The implementation lives in a sibling file:

```go
// internal/email/email.go
package email

import (
    "context"
    "github.com/sendgrid/sendgrid-go"
)

type sendgridSvc struct {
    client *sendgrid.Client
}

func New(apiKey string) Service {
    return &sendgridSvc{client: sendgrid.NewSendClient(apiKey)}
}

func (s *sendgridSvc) Send(ctx context.Context, to, subject, body string) error {
    // ...
}
```

The constructor returns the interface type (`Service`), not the concrete struct. This forces consumers to depend on the abstraction.

## What `forge generate` produces

For every contract it finds — a `contract.go`, or a `//forge:contract` type (which also gets `contract_gen.go`, above) — one sibling file: **`mock_gen.go`** — holding a mock for **every interface in the contract, not just `Service`**. `Service` → `MockService`; a `Store` interface declared beside it → `MockStore`; a `Charger` → `MockCharger`. The scaffold emits the stub immediately, so the mocks exist from the moment the directory does.

**So never hand-roll a fake for a dep interface** — its mock is in the file next to the one you are typing in. The corollary: declare dep interfaces in the contract's boundary — `contract.go`, or `deps.go` (`// forge:deps` first line) beside a marked type. One declared in `service.go` or a `ports.go` gets no mock; mocking only what the boundary declares is deliberate.

`mock_gen.go` is regenerated on every `forge generate`. **Never
hand-edit it.** If the contract changes, edit `contract.go` (or the marked type's methods) and re-run.

The generated file carries the canonical forge ownership header — a
`// Code generated by forge. DO NOT EDIT.` line, a `// Source:` pointer
at `contract.go` (or `contract_gen.go`), and a `// To customize:` pointer back at the contract.
`forge lint --scaffolds` enforces both halves: hand-editing the `_gen.go`
file (or stripping its header) is a build-gating error.

### Diverging from generated CRUD wiring — the owned-shim op override

The same split governs CRUD handlers: the per-entity op wiring (`handlers_crud_ops_gen.go`) is regenerated, the delegating shims in `handlers_crud.go` are yours. To customize one RPC, never edit or disown the generated ops file — take the generated op in your shim, replace the one exported field you need, and keep delegating, so the CRUD lifecycle stays in `forge/pkg/crud` and schema/proto evolution keeps flowing underneath. The **`db/crud-overrides` skill carries the worked recipe** (which fields are overridable, the List/single-row variants).

### The other generated sibling: `middleware_gen.go`

A package whose `func New` carries `// forge:constructor` also gets
**`middleware_gen.go`** — a decorator implementing `Service` that routes every
method through the in-process chain assembled in the OWNED `observe_chain.go`
(span, metrics, structured log, panic recovery). Add a method to `Service` and
the wrapper appears on the next `forge generate`. `// forge:no-observe` on the
constructor opts the package out; on one method, routes that method around the
chain. Lifecycle methods (`Run`/`Drain`/`Close`/`Ready(ctx) error`, a worker's
`Start`/`Stop`, `Close() error`) go around the chain by default;
`// forge:observe` on one routes it through.

That decorator is the ONLY per-package observability codegen — request-scoped
observability is Connect interceptors at the handler boundary
(`serverkit.NewChain(serverkit.ChainDeps{…})`, built in the generated `cmd serve.go`). See
`observability`.

## Using mocks in tests

Use the generated `Mock<Iface>` func-fields to stub dependencies, `<Method>Calls()` for typed argument assertions, and `CallCount("<Method>")` for counts. Load `contracts/mocks` for the worked example, call-record naming and collision rules, generic call history, and integration harnesses.

## Pure-utility packages — three options

For packages of pure logic on no state (`naming`, `format`, math helpers) that export methods. Only exported methods draw `requirecontract`: a package of top-level funcs needs no contract and no opt-out.

### (A) Opt out of the contract
In the package's `doc.go`, with a reason:
```go
// Package naming converts identifier case.
//
//forge:exclude-contract: pure string-case helpers, no I/O, time or state
package naming
```
Or centrally, in `forge.yaml`:
```yaml
contracts:
  exclude:
    - "internal/naming"
```
This is honest about having no test seam, and it suits string-case conversions and pure formatters. The marker is refused (`forge-exclude-contract-outbound-io`, `forge-exclude-contract-multi-impl`) on a package that makes outbound calls or owns an interface with two or more implementations, because there the contract is the point; `contracts/package-markers` has the fixes.

### (B) Wrap functions in a `Service` struct
Expose the functions as methods on a struct and mark it `//forge:contract` (or define a `Service` interface in `contract.go`). Mockable, but verbose at every call site (`naming.New().ToPascalCase(s)` instead of `naming.ToPascalCase(s)`).

### (C) Hybrid
Keep free functions for ergonomics AND expose a `Service` interface whose methods delegate to them. Pick this when consumers want mockability but you don't want to break call sites.

**Decision rule:** pure utility with no I/O / time / randomness / external state → (A). Anything that touches one of those → (B) or (C). Don't apply this rule to your DB layer, HTTP clients, or anything that calls `time.Now()` — those are stateful and need a mock.

A helper package that is private to a public SDK (`pkg/<sdk>/internal/<x>`) is outside `requirecontract`'s scope and needs none of these.

## Greenfield: `forge scaffold package`

For new packages in an existing forge project:

```bash
forge scaffold package <name>
```

Scaffolds `internal/<name>/`:
- `service.go` with `Deps`, the `//forge:contract`-marked `service` struct and `New`
- `contract_gen.go` and `mock_gen.go` via `forge generate`

There is no `contract.go`: the `Service` interface is derived from the struct's exported methods. It will refuse if the directory already exists. For ports of existing packages, either move the implementation into the marked struct and let forge derive the interface, or (for a strategy interface, or when you want the surface reviewed first) hand-write `contract.go` extracted from the source's exported surface, run `forge generate`, then copy the implementation behind the interface. An existing hand-written single-implementation `Service` converts with `forge fix derive-contract <package-dir>`. See `migration-cli` and `migration-service` for the porting flow.

## Per-phase lint gate

```bash
forge lint --contract --exported-vars && go build ./... && go test ./...
```

If any of those three fail, the phase isn't done. Catching a contract gap at introduction time is cheap; backfilling six phases later is not.

## Rules

- One contract per package: a `//forge:contract` struct (single implementation) or a `contract.go` (strategy interface), never both. The interface IS the public surface.
- Constructors return the interface, not the concrete struct.
- Never hand-edit `mock_gen.go` or `contract_gen.go`. Edit `contract.go` (or the marked type's exported methods) and re-run `forge generate`. (Pre-1.7 projects also had `middleware_gen.go` / `tracing_gen.go` / `metrics_gen.go`; those are removed by the next `forge generate`.)
- Naming: `Service` for single-impl, `<Name>er` for multi-impl pluggable backends.
- Pure utilities: pick (A), (B), or (C) explicitly. Don't leave the package half-mocked.
- Run the contract lane from day one. Fix findings before porting the next package.

## When this skill is not enough

- **Designing proto-level contracts** (the external API surface) — see `proto` and `services`.
- **Choosing what middleware to apply** (auth, rate-limit, audit) — see `auth`, `observability`.
- **Test patterns** beyond unit-level mocking (integration, e2e) — see `testing` and its sub-skills.
- **Migrating an existing codebase** that doesn't yet have contracts everywhere — see `migration`.
