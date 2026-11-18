---
name: interactor
description: Use-case orchestrators — a service whose Deps are other services. Deps are interfaces only, so the workflow unit-tests against generated mocks alone.
emit: both
---

# Interactor

An interactor is the package that owns one workflow: a sequence of calls to two or more deps, with validation, error wrapping, and (when needed) transaction coordination. It sits *above* adapters and services and *below* the transport / handler layer.

**An interactor is not a forge package kind.** It is an ordinary service whose `Deps` fields happen to be other packages' `Service` interfaces rather than a database handle. There is no scaffold flag for it, no marker, and nothing in forge behaves differently for one — which is the point: the rules that make an interactor testable (every `Deps` field is an interface) apply to every package, so an orchestrator needs no special status to get them. This page is the design pattern; `service-layer` is the shape it is built on.

## When to add an interactor

Add an interactor whenever you find yourself wanting to call two or more deps in sequence to fulfill one user-facing operation:

- "Charge the card, then write the audit log, then publish a domain event" — an interactor over three deps.
- "Fetch the account, validate eligibility, hit the partner API" — one interactor over two deps + one validation step.
- "Read from queue, transform, write to storage" — one interactor over a queue adapter + a storage adapter.

If a workflow only calls one dep, you don't need an interactor — that one method belongs on that dep's own interface. Interactors earn their keep by *composing*.

Scaffold it — don't hand-roll the package: `forge scaffold package <name>` emits `contract.go`, `service.go`, and a starter `contract_test.go`; declare your dep interfaces in `contract.go` and grow the all-mock workflow tests as you implement. Never write orchestration inline in a handlers package — it grows into a god-function (and trips `funlen`).

## What goes in

- Input validation at the top of each method (`if in.Foo == "" { return ... }`).
- The sequence of dep calls expressing the use case.
- Error wrapping so the failure chain points at the failing step (`fmt.Errorf("step: %w", err)` in Go; equivalents elsewhere).
- Transaction coordination — one transaction around the steps that must commit together. A serializable transaction may be retried on conflict, so a step that leaves the database (charge a card, call a partner API) does not belong inside it.
- Domain-event emission after the workflow's commit point — registered inside the transaction, run only once it commits, never for a rolled-back attempt.

## What does NOT go in

- Direct calls to HTTP / vendor SDK / queue / storage. That's an adapter's job. Declare a role interface in `contract.go`, add the matching `Deps` field, and call `s.deps.Foo.Fetch(ctx, ...)`.
- Transport-layer logic — request/response shape conversion, validation tied to wire types. That's the handler's job; the handler *calls* the interactor.
- Cross-package state. If two interactors need to share state, the state belongs on a service (or a dedicated state holder both interactors depend on as another `Deps` field).

## Composition: deps are interfaces, always

The reason interactors are testable is that **every `Deps` field is an interface**. Concrete struct pointers in the dep set defeat the all-mock test surface and force tests to drag in real downstreams. This is lint-enforced for every package, not just this one: `forgeconv-deps-are-interfaces` FAILS `forge lint` on any `Deps` field that is a concrete struct or a pointer to one.

```go
type Service interface {
    ChargeAndAudit(ctx context.Context, in ChargeAndAuditInput) error
}

// Dep interfaces — what this interactor needs FROM each dep.
// They live in contract.go (here), not next to the adapter, for two
// reasons: the interactor's full dep surface is documented in one
// place, and contract.go is the file `forge generate` mocks — an
// interface declared anywhere else gets no mock.
type Charger interface {
    Charge(ctx context.Context, userID string, amount int64) (chargeID string, err error)
}

type Auditor interface {
    Append(ctx context.Context, userID, action, refID string) error
}
```

```go
type Deps struct {
    Logger  *slog.Logger
    Charger Charger // implemented by a concrete adapter in production
    Auditor Auditor
}

type service struct{ deps Deps }

func New(deps Deps) (Service, error) { return &service{deps: deps}, nil }

func (s *service) ChargeAndAudit(ctx context.Context, in ChargeAndAuditInput) error {
    if in.UserID == "" || in.AmountMinor <= 0 {
        return fmt.Errorf("billing-flow: invalid input")
    }
    chargeID, err := s.deps.Charger.Charge(ctx, in.UserID, in.AmountMinor)
    if err != nil {
        return fmt.Errorf("billing-flow: charge: %w", err)
    }
    if err := s.deps.Auditor.Append(ctx, in.UserID, "charge", chargeID); err != nil {
        return fmt.Errorf("billing-flow: audit: %w", err)
    }
    return nil
}
```

The interactor is unaware that `Charger` is Stripe (or Adyen, or a test fake) — that's the point.

This is the general idiom, not an interactor quirk: **declare interfaces at the CONSUMER, not the implementation.** Accept interfaces (flexibility in), return concrete structs (no speculative abstraction out). Each dep interface is *narrow* — it names only the methods this interactor calls. If a dep exposes a wide interface and every caller uses a different slice of it, that's a smell: split it into per-consumer interfaces like the `Charger`/`Auditor` pair above, rather than importing one god-interface everywhere.

```go
// Smell: one wide interface each caller uses a slice of.
type Payments interface { Charge(...); Refund(...); ListDisputes(...); /* …12 more… */ }

// Better: this interactor declares only what it calls.
type Charger interface { Charge(ctx context.Context, userID string, amount int64) (string, error) }
```

## Late-bound dependencies: construct-then-inject in `NewComponents`

Sometimes package B needs a value that only exists *after* package A is constructed (worker A produces a snapshot saver that worker B consumes; service X exposes a registry that interactor Y registers handlers into). Putting that value in B's `Deps` creates a construction-order cycle — `New(Deps)` resolves its dep closure once and has no slot for "set this later".

Construct-then-inject is a first-class plain method call in the composition, not a framework hook. `internal/app/compose.go` is already yours — in `NewComponents` construct A and B, and call B's setter with A's product:

```go
func NewComponents(infra *Infra) (*Components, error) {
    c := &Components{}
    c.Snapshotter = snapshot.New(snapshot.Deps{...})
    c.Trader = trader.New(trader.Deps{...})

    // two-phase wiring: B's setter, after both ends exist
    c.Trader.SetSnapshotSaver(c.Snapshotter.SnapshotSaver())

    return c, nil
}
```

This is just ordinary Go in the `compose.go` you already own — no `PostBootstrap`, no `*App` field read by name, no parallel hook system. Near-diamonds and post-construction setters (`bill.WithReliantAPIKeyIssuer(llm)`) are the same pattern: construct, then inject. Any model based on pure constructor topo-ordering deadlocks on the real graph; `NewComponents` supports construct-then-inject explicitly.

For the related case where a typed Deps field can't reference its target yet because the owning lane hasn't merged, the interface seam handles it — the consumer depends only on the dep's *interface*, so the fill in `NewComponents` is a one-line swap once the concrete type lands (real in-process instance, a Connect client, or a mock). There is no placeholder marker; see the "Deferred / cross-lane typing is handled by the seam" section of the `api` skill. That is distinct from the runtime construct-then-inject case above.

## How to test

All-mock deps — and the mocks already exist. **Never hand-roll a fake here.** `forge generate` writes one mock per interface in `contract.go` into `mock_gen.go`, in the same package: `Charger` → `MockCharger`, `Auditor` → `MockAuditor`. A hand-rolled `fakeCharger` is a reimplementation of a file sitting next to the one you are typing in.

Each mock has a `XxxFunc` field per method (assign it to script the call; leave it unset and the method returns a `"MockCharger.ChargeFunc not set"` error, which is itself a useful assertion) and embeds `contractkit.Recorder`, which records every call. That recorder is what removes the bookkeeping the hand-rolled version existed for:

```go
func TestChargeAndAudit_HappyPath(t *testing.T) {
    charger := &MockCharger{
        ChargeFunc: func(context.Context, string, int64) (string, error) { return "ch_1", nil },
    }
    auditor := &MockAuditor{
        AppendFunc: func(context.Context, string, string, string) error { return nil },
    }

    svc, err := New(Deps{Charger: charger, Auditor: auditor})
    if err != nil {
        t.Fatalf("New: %v", err)
    }
    if err := svc.ChargeAndAudit(ctx, ChargeAndAuditInput{UserID: "u1", AmountMinor: 100}); err != nil {
        t.Fatalf("ChargeAndAudit: %v", err)
    }

    // Step 2 ran once, with step 1's output.
    if got := auditor.CallCount("Append"); got != 1 {
        t.Fatalf("Append called %d times, want 1", got)
    }
    if args := auditor.Calls("Append")[0]; args[3] != "ch_1" {
        t.Fatalf("Append got refID %v, want the charge id", args[3])
    }
}
```

The two assertions interactor tests typically need — and the recorder answers both without a line of fake bookkeeping:

1. **Composition order.** "When step 1 succeeds, step 2 is called with step 1's output." → `auditor.Calls("Append")[0]` carries the recorded arguments.
2. **Failure short-circuiting.** "When step 1 fails, step 2 is not called." → set `ChargeFunc` to return an error and assert `auditor.CallCount("Append") == 0`. This is the pattern that catches half-applied workflows.

Both assertions read off the mock. If you find yourself adding a field to a fake to record what it was called with, stop — that field is `contractkit.Recorder`, and it is already there.

## Rules

- **Every dep is an interface, not a concrete type.** Concrete deps defeat the all-mock surface and force tests to drag in real downstreams.
- **Two or more deps.** A one-dep interactor is a smell — that method belongs on the dep itself.
- **Interactors don't call third-party systems directly**, only adapters that wrap them.
- **Tests use all-mock deps**; never live downstreams.
- **Validation lives at the interactor's edge** (top of each method); adapters trust validated input.

<!-- @forge-only:start -->
## Forge scaffolding

An interactor is a service. Scaffold it with the plain package verb — there is no interactor flag, because after scaffolding there would be nothing to tell one apart from any other package:

```
forge scaffold package billing-flow
```

This emits the canonical package layout:

```
internal/billing-flow/
  contract.go     # Service + the dep interfaces this workflow needs
  service.go      # Deps + service struct; New(Deps) (Service, error)
  contract_test.go
  mock_gen.go     # one mock per interface in contract.go — emitted immediately
  observe_chain.go
```

The scaffold ships with `Logger` and `Config` as its only deps, so `forge generate` stays green the moment the package exists. Declare one role interface per dep in `contract.go` and add the matching `Deps` field **once its implementation exists** — `forge generate` wires each field by type to the registered component that implements it. Declaring a dep interface before any component satisfies it fails generate loudly (`MissingProvider`): scaffold the adapter first, then declare the dep.

## Lint enforcement

- `forgeconv-deps-are-interfaces` — every field on `Deps` must be an interface type, not a concrete struct or a pointer to one. Concrete deps defeat the all-mock test surface. It is an **error** — it fails `forge lint`, though not `forge generate`, which only stops for things that would make generated code fail to compile — and it applies to **every** package: there is no marker to opt in with and none to escape through. A `pkg.T` dep is RESOLVED, in your module or any dependency's, so a dep that is already an interface never fires. Logger and Config are carve-outs (one shared singleton each, supplied by the composition). A func-typed field is already a seam and passes, and so does a slice or map whose elements are interfaces or funcs (`map[string]IntegrationAccess`): a table of seams, inert when nil. When the rule fires on a concrete dep the package never calls a method on — it only passes it to another call, or only calls a func-typed field of it — the finding names that call or field, with `file:line`, as the seam.

## Wiring in the explicit composition (`NewComponents`)

The interactor is wired in `internal/app/compose.go` `NewComponents` (yours; constructs every component INLINE off the owned `internal/app/providers.go` `Infra`), as typed interface fills in one place: adapters first, then the interactor on top, then the handler depending on the interactor. Each fill is resolved by type off `infra.<Field>` — no `*App` fields, no name-matching, no string-keyed registry.

```go
func NewComponents(infra *Infra) (*Components, error) {
    charger := stripeadapter.New(stripeadapter.Deps{...})
    auditor := audit.New(audit.Deps{...})

    flow, err := billingflow.New(billingflow.Deps{
        Logger:  infra.Log,
        Charger: charger, // adapter satisfies the dep interface
        Auditor: auditor,
    })
    if err != nil {
        return nil, err
    }

    c := &Components{}
    c.Billing = billinghandler.New(billinghandler.Deps{Flow: flow})
    return c, nil
}
```

Each dep is filled by its interface, so swapping the real in-process adapter for a Connect client or a mock is a one-line change here with the interactor untouched. That is also what makes "spin up this interactor with every dep mocked" a few-line call against `NewComponents`.

## When this skill is not enough (forge sub-skills)

- **The outbound boundary itself** (third-party calls, response mapping) — see `adapter`.
- **The Service / Deps / New shape** — see `service-layer`.
- **Transactions** — declare `DB orm.Context` and wrap the steps in `s.deps.DB.RunTx(ctx, orm.Serializable, fn)` (lock-free read-check-write; `orm.ReadCommitted` when a lock is the correctness argument — there is no default level): every dep that writes through forge's ORM with `fn`'s `ctx` joins it (a dep's own `RunTx` joins too), and `orm.AfterCommit` carries the post-commit event. See `service-layer/transactions`.
- **Handler-side validation and error wrapping** — see `api`.
- **Translating vendor errors into svcerr sentinels at the boundary** — see `service-layer`'s errors section and `forge/pkg/svcerr`.
<!-- @forge-only:end -->
