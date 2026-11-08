---
name: adapter
description: Outbound boundary translators. One adapter per third-party system / queue / storage backend; narrow interface, vendor-neutral callers.
emit: both
---

# Adapter

An adapter is the package that translates between your domain and one external system: a third-party HTTP API, a message broker, a storage gateway, an OAuth provider. It owns the wire format, the retries, the timeout policy, and the response mapping. It does not own business logic — that lives one layer up.

## When to add an adapter

Add an adapter the moment you need to call an external system that isn't your own database. Symptoms:

- You're about to import a vendor SDK in your application bootstrap or in a handler.
- You're about to write `POST https://api.stripe.com/v1/...` (or the equivalent in your stack) inline in a service.
- You're orchestrating two external calls together — that's an orchestrating service over two adapters, not an unstructured helper.

If the external system is your own first-party API that you already generate clients for, you don't need an adapter — depend on the generated client. Adapters exist for boundaries you don't already manage.

## What goes in

- The HTTP / gRPC / SDK client setup, with timeouts.
- Per-request authentication header construction.
- Retry / circuit-breaker / rate-limit policy.
- Response-body parsing, vendor → domain type mapping.
- Vendor-error → domain-error translation.

## What does NOT go in

- Multi-step workflows (validate → fetch → send → audit). Compose those in an orchestrating service that depends on this adapter's interface.
- Business logic — eligibility checks, pricing, dedupe. Those belong to the domain.
- Reaching into other adapters or services. Adapters are leaf nodes; they don't know about each other.

## The narrow interface

The adapter exposes ONE narrow interface in the language of your domain — not the full vendor SDK. Conventions worth keeping:

- **One method per use case the downstream supports.** Resist mirroring the entire vendor SDK; you only owe an interface for what your domain actually needs.
- **Domain types in / out, not vendor types.** Even if the vendor SDK has 40 fields per object, your adapter exposes the 1-2 your domain consumes.
- **Cancellation propagates.** Always accept a context / cancellation token as the first argument so timeouts and tracing work end-to-end.

Illustrative shape (Go):

```go
type Service interface {
    HealthCheck(ctx context.Context) error
    CreateCharge(ctx context.Context, in CreateChargeInput) (CreateChargeResult, error)
}

type CreateChargeInput struct {
    AmountMinor int64
    Currency    string
    CustomerID  string
}

type CreateChargeResult struct {
    ChargeID string
}
```

Same shape in any language: a narrow interface in domain types, hiding the vendor's wire format.

**Declare interfaces at the consumer, not the implementation.** The adapter's exported `Service` interface is a legitimate seam — it's the mock/test target and the DI boundary (accept interfaces, return concrete structs is honoured by forge's `New(Deps) Service`). But keep it narrow: one method per use case the domain actually needs. If two different consumers each use a different slice of a wide adapter interface, that's a smell — let each consumer declare the 1–2 method interface it needs at its own site (see the role-interface pattern in the `api` skill), so widening the adapter never breaks a consumer that didn't ask for the new method.

```go
// Adapter exposes a narrow, domain-worded Service (the mock seam).
type Service interface { CreateCharge(ctx context.Context, in CreateChargeInput) (CreateChargeResult, error) }

// A consumer that only needs one method declares its own view, next to itself:
type charger interface { CreateCharge(ctx context.Context, in CreateChargeInput) (CreateChargeResult, error) }
```

## Configuration

An adapter's settings — base URL, account IDs, API keys, timeouts — are configuration, not dependencies. Not every adapter has a base URL (a queue, a vendor SDK, a storage gateway), so none is assumed: `forge scaffold adapter <name>` alone writes no config, and its `HealthCheck` fails until you write a probe. For an HTTP API with one base URL, `forge scaffold adapter <name> --base-url` declares a `<Name>Config` message with a `base_url` leaf (env `<NAME>_BASE_URL`, or the one `--base-url-env STRIPE_API_URL` names) in `proto/<project>/config/v1/config.proto`, composes it on `AppConfig`, and the adapter takes the whole block as ONE Deps field:

```go
type Deps struct {
    Logger     *slog.Logger
    HTTPClient *http.Client
    Cfg        *config.StripeConfig // compose.go fills it by type: infra.Cfg.GetStripe()
}
```

Add a setting as another leaf on that message (`sensitive: true` for a secret), run `forge generate`, and read it through its getter (`s.deps.Cfg.GetApiKey()`). An adapter scaffolded without `--base-url` gets the same shape by hand: declare the message, compose it on `AppConfig`, add the `Cfg` field. Each leaf then comes from its env var, a `--config` file or `deploy/kcl/<env>/config.k`. Never add a scalar Deps field instead: `forge lint` reports it (forge-config-deps), and compose.go has no type to wire it by.

## How to test

Adapter tests use an in-process HTTP test server (or the SDK's record-and-replay equivalent) to stand in for the vendor. The point is to exercise the *adapter's* translation logic — request construction, header injection, response parsing, error mapping — against a controlled downstream.

```go
func TestCreateCharge_OK(t *testing.T) {
    stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/charges" { http.NotFound(w, r); return }
        if got := r.Header.Get("Authorization"); got == "" {
            t.Errorf("missing Authorization header")
        }
        _, _ = w.Write([]byte(`{"id":"ch_test_1"}`))
    }))
    defer stub.Close()

    svc := New(Deps{HTTPClient: stub.Client(), Cfg: &config.StripeConfig{BaseUrl: stub.URL}})

    res, err := svc.CreateCharge(context.Background(), CreateChargeInput{...})
    if err != nil { t.Fatalf("CreateCharge: %v", err) }
    if res.ChargeID != "ch_test_1" { t.Fatalf("got %q", res.ChargeID) }
}
```

The package that calls this adapter in production mocks the interface — `forge generate` already put a `MockService` for it in this package's `mock_gen.go` — never the downstream HTTP server.

## Library choice is yours

Your adapter wraps whatever client you choose — raw HTTP, the vendor SDK, an RPC client, a queue / streaming client. The convention is the *shape* of the package (narrow interface in domain types), not the transport library.

## Rules

- **One adapter per outbound boundary.** No multi-system "integration" packages.
- **Narrow interface in domain types.** Don't expose vendor types or mirror the vendor SDK.
- **Test against a stub server.** Never against the live downstream.
- **Adapters are leaf nodes.** No other adapters / services in the dep struct.
- **Tell answers from failures.** A lookup that found nothing is the downstream answering, not failing: return it as its own domain sentinel (`ErrNotFound`), never folded into a transport error, and declare it expected to your logging layer so it doesn't log as a failure.

## When a consumer must register onto the adapter (two-phase wiring)

Adapters are leaf nodes at construction, but occasionally a downstream consumer registers a callback / sink / subscriber onto the adapter after both exist (e.g. an event-bus adapter receiving subscribers from services built later). Don't add the consumer to the adapter's `Deps` — that inverts the leaf rule, and constructor topo-ordering alone deadlocks on this shape.

Use **construct-then-register** inside `NewComponents` (`internal/app/compose.go` is yours already): build the adapter, build the consumer, then call the register/subscribe setter. It's an ordinary method call after both ends exist — not a framework seam.

```go
bus := eventbus.New(eventbus.Deps{Logger: log})
svc := orders.New(orders.Deps{Bus: bus})  // consumer holds the adapter interface
bus.Subscribe("order.created", svc.OnOrderCreated)  // phase two
```

There is no `PostBootstrap` / `post_bootstrap.go` seam — late registration is plain Go in the `compose.go` you own. See the `interactor` skill for the canonical two-phase shape.

<!-- @forge-only:start -->
## Forge scaffolding

Scaffold an adapter with:

```
forge scaffold adapter stripe-adapter --base-url   # an HTTP API: config block + base_url leaf
forge scaffold adapter pubsub                      # anything else: no config assumed
forge scaffold adapter pubsub --dry-run            # print the plan, write nothing
```

This emits the canonical package layout:

```
internal/stripe-adapter/
  contract.go        # // forge:outbound-io — Service interface, narrow surface
  adapter.go         # Service struct + downstream calls; New(Deps) Service,
                     # marked // forge:constructor (adapters do I/O — keep it)
  adapter_test.go    # httptest stub of the downstream (--base-url); else the
                     # traced-client default and the failing HealthCheck
  observe_chain.go   # YOURS (scaffold-once) — newObserveChain() assembles the
                     # in-process middleware chain (span, metrics, structured
                     # log, panic-recovery) the generated decorator routes every
                     # Service method through (see the observability skill)
```

Plus a `cache.go` stub for any local caching the adapter needs (delete it if you don't). `forge scaffold package <name> --type adapter` resolves to the same code path.

## Marker comments and lint enforcement

Every adapter package's `contract.go` carries a `// forge:outbound-io` marker on the package doc, and its `func New` carries `// forge:constructor`.

`// forge:outbound-io` is named for what it ASSERTS, not for the pattern: *this package calls out to a third-party system and serves nothing inbound.* That is the only part of "adapter" a linter can check, and naming the marker after it means you can tell when to stamp it by reading it. Stamp it on any package that is an outbound boundary, whether or not you would have called it an adapter. Two rules read it:

- `forgeconv-outbound-io-no-rpc` — a package carrying the marker must not register Connect RPC handlers. An inbound handler contradicts the claim; the RPC surface is the actual service, and this package is the dependency underneath it.

## Wiring: construct in the explicit composition

An adapter is a leaf built in `internal/app/compose.go` `NewComponents` (off the owned `internal/app/providers.go` `Infra`) and passed to consumers as an **interface**. No `Setup(app *App)`, no name-matched `App` fields — just construct it and hand it down.

```go
// internal/app/compose.go
stripe := stripeadapter.NewServiceWithForgeMiddleware(stripeadapter.New(stripeadapter.Deps{
    HTTPClient: infra.DefaultClient(),  // instrumented shared transport: OTel client spans + propagation + 30s timeout
    Cfg:        infra.Cfg.Stripe,       // scalars travel as one typed Config block
}))
bill := billing.New(billing.Deps{Charges: stripe})  // consumer sees stripeadapter.Service, not the concrete type
```

`forge generate` emits exactly this shape automatically: any `HTTPClient *http.Client` Deps field is wired to `infra.DefaultClient()`, and an instrumented package — one carrying the `// forge:constructor` marker (adapter scaffolds stamp it by default) or the owned `observe_chain.go` seam — is constructed wrapped, as `New<Concrete>WithForgeMiddleware(New(...))` (the constructor named after the concrete return type; the canonical `&service{}` yields `NewServiceWithForgeMiddleware`). Prefer `infra.DefaultClient()` over a bare `&http.Client{}` — the bare client makes the adapter's outbound calls invisible to traces.

Because the consumer depends on the interface, swapping the real adapter for a mock (tests) or a different backend is a one-line change here — the consumer is untouched.

## Expected errors: declare the answers, so failures stay loud

The generated decorator logs every call through the package's chain, and an error is a failure — logged at ERROR — unless the package says otherwise. Many adapter errors are not failures: a storage read for a missing object (the origin turns it into a 404), a registry asked for a repository it does not hold (the caller treats it as empty). Logged at ERROR, those drown the real ones: a static-site proxy wrote 288 ERROR records in five hours, every one a crawler 404.

Declare them where the package shapes its observability — the owned `observe_chain.go`, on `observe.LogMiddleware`:

```go
// observe_chain.go (yours)
observe.LogMiddleware(logger, slog.LevelDebug,
    // A missing object is the answer a 404 is built from, not a failure.
    observe.WithExpectedErrors(ErrNotFound)),
```

- A matching error (`errors.Is` against any target) is logged like a success — at the success level, sampled with `observe.WithSuccessSampling` in its own slot, always written when slow — carrying the error and `expected=true`. Under a production INFO handler a DEBUG seam writes nothing for it.
- Everything else stays a failure: ERROR, with the error. Nothing is expected until declared — forge cannot tell an answer from a fault.
- `observe.WithExpectedErrorFunc(func(err error) bool)` is the primitive, for a classification no sentinel expresses (a typed vendor error whose status field decides it). Declarations accumulate.
- The declaration scopes to THIS package's chain; the error value is not marked. A caller that turns the answer into a failure ("the bundle this deploy needs is gone") logs its own failure normally.
- It changes logging only. The span and the `<pkg>.errors` metric still record the error.

Why the seam, and not a method on the adapter or a wrapper around the error: what is expected is a fact about this package's error vocabulary, the seam is the one owned place that configures this package's chain, and the decorator stays uniform. An `IsExpected` method would put a receiver on a pure function and silently stop working the moment anything wraps the adapter; marking the error value would carry "expected" past the boundary where it is true.

## When this skill is not enough (forge sub-skills)

- **Composing multiple adapters into a workflow** — see `interactor`.
- **The Service / Deps / New shape itself** — see `service-layer`.
- **Wrapping vendor errors into svcerr sentinels** — see `service-layer`'s errors section and `forge/pkg/svcerr`.
- **Webhook ingestion (inbound from a vendor)** — that's a webhook handler, not an adapter. See `forge scaffold webhook`.
<!-- @forge-only:end -->
