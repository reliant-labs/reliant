# Design: the composition graph — every part of a process is a component

**Status:** approved by the owner 2026-10-19 (all §9 recommendations accepted). Implementation per §7. **Date:** 2026-10-15.
**Scope:** forge (`pkg/serverkit`, `pkg/lifecyclekit`, `internal/codegen`, project templates, KCL, lint), with
reliant's api / worker / gateway as the forcing case.
**Owner direction:** (1) these are all "configurable structs", so nothing gets a special layer. (2) "Give each
thing as little as possible": least knowledge and least privilege is a design rule, not a guideline.

## Summary

- **One primitive.** A component is a struct built from its Deps: `New(Deps) (*T, error)`. A DB pool, a NATS
  client, a handler, a route set, an interceptor chain, an HTTP listener, a Temporal worker and a background loop
  are all components. Infra, mounts and servers are not special categories.
- **The graph is typed, generated Go.** Deps resolve by type at generate time. There is no reflection and no
  runtime container. A **root** is a component a process starts from. A binary builds only the subgraph its root
  reaches, so the worker never constructs the api's 35 handlers or dials what it doesn't use.
- **Lifecycle comes from graph order.** Start runs in dependency order and stop runs in reverse. Every reliant
  drain case fits plain fields on library components. No hook categories are needed.
- **Least privilege is enforced** by types, by lint, by the import graph, and at deploy time. A root's reach also
  determines which env vars, secrets and connections its workload gets.
- **Partial adoption needs no fork.** Every constructor is callable from a hand-written `main`, so "B" becomes a
  stage on the way to "A".

## 1. The model

```go
// internal/handlers/tools_daemon/service.go — a component, exactly as forge scaffolds one today
type DaemonStore interface { // declared HERE, by the consumer: only the methods it calls
	UpsertDaemon(ctx context.Context, d *db.Daemon) error
	RenewDaemonAttachment(ctx context.Context, id string, until time.Time) error
}
type Deps struct {
	Daemons DaemonStore                // a capability, not the 447-method db.Repository
	Events  DaemonEventPublisher       // scoped publisher (§2)
	Config  config.ToolsDaemonConfig   // its own config block, not *config.Config
	Logger  *slog.Logger
}
func New(d Deps) (*Service, error) // validateDeps: every field required; nil is a boot error
```

A component can also implement any of four single-method interfaces from `lifecyclekit`. Each one is
optional and detected by **codegen** (go/types sees the concrete type). Detection is never a runtime type switch.

| Interface          | Meaning                                                  | Typical implementers                                                    |
| ------------------ | -------------------------------------------------------- | ----------------------------------------------------------------------- |
| `Run(ctx) error`   | long-lived; returns when ctx ends                        | listener serve loop, Temporal worker, background loop, operator manager |
| `Drain(ctx) error` | stop taking new work, finish in-flight by ctx's deadline | listener, Temporal worker, gateway connector                            |
| `Close(ctx) error` | release resources                                        | DB pool, NATS conn, Temporal client, OTel                               |
| `Ready(ctx) error` | contributes to `/readyz`                                 | DB pool, NATS conn                                                      |

**Everything is a component.** Forge ships _library_ components and the project owns _instances_:

| Thing                                   | Library constructor (forge)                                               | Instance (project)                                                   |
| --------------------------------------- | ------------------------------------------------------------------------- | -------------------------------------------------------------------- | ------- | --------- | ------------------- |
| Postgres pool                           | `serverkit.NewPool(PoolDeps{Config})` (wraps today's `serverkit.OpenDB`)  | provider func in `providers.go`                                      |
| NATS / Temporal client, third-party SDK | —                                                                         | 5-line provider func: `func NewNATS(d NATSDeps) (*nats.Conn, error)` |
| Handler / package / worker / operator   | —                                                                         | `internal/handlers                                                   | workers | operators | <pkg>/` (unchanged) |
| Route set                               | generated `<Audience>Routes`: every service of one audience (§4)          | hand-write one for a custom grouping                                 |
| Interceptor chain                       | `serverkit.NewChain(ChainDeps{Auth, …})`: today's inline chain, extracted | `UserChain`, `DaemonChain`                                           |
| HTTP listener                           | `serverkit.NewListener(ListenerDeps{Config, Chain, Routes})`              | `APIListener`, `DaemonListener`                                      |
| Root                                    | —                                                                         | `internal/app/roots.go` struct marked `//forge:root` (§3)            |

**Instance identity is the Go type.** Two listeners are two types: `type DaemonListener struct{ *serverkit.Listener }`.
That is what keeps by-type resolution unambiguous without names, tags or qualifiers.

**Config binding.** The binary loads the root `*config.Config` once and only the composition root reads it.
Each component receives **its own block**: `Config: g.Cfg.ToolsDaemon`, or scalar fields resolved by name
(`inject_gen.go` already does `infra.Cfg.<field>`). A block is a config message referenced from the root config
message, which is the existing block mechanism. Today it allows one nesting level (`config_gen.go:357`), and that
is enough.

**Generated vs owned:**

| File                         | Status                                                                                                                                                                                          |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/app/compose.go`    | owned and reconciled. One constructor method per component, holding the value expressions (§3). Forge injects missing methods and Deps keys and never touches values. This is today's contract. |
| `internal/app/providers.go`  | owned. **Provider constructors** replace the single eager `OpenInfra`                                                                                                                           |
| `internal/app/roots.go`      | owned. Root structs                                                                                                                                                                             |
| `internal/app/graph_gen.go`  | generated. Memoized accessors, `Build<Root>`, `Run<Root>`, lifecycle plan, cycle check                                                                                                          |
| `internal/app/routes_gen.go` | generated. Per-audience route sets (replaces `mounts_services_gen.go` and `MountAll`)                                                                                                           |
| `cmd/<bin>/cmd/roots_gen.go` | generated. One cobra command per root (replaces `cmd-tree-serve.go.tmpl`'s `Serve`/`ServeSpec`)                                                                                                 |

## 2. Least knowledge, least privilege (first-class rule)

Today a process opens the whole `Infra` eagerly (`providers.go.tmpl:156`), and `NewComponents(*Infra)`
(`compose.go.tmpl:73`) builds every component from it. Scaffolded Deps take the root `*config.Config`
(`service.go.tmpl:24`, wired by convention at `inject_gen.go:1204`). Reliant's handlers take `Repo db.Repository`:
a 447-method interface held by 9 Deps fields, of which `tools_daemon` calls 19. The rules below reverse that:

| Rule                                     | Means                                                                           | Enforced by                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ---------------------------------------- | ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **K1** Deps name exactly what is used    | consumer-declared interfaces, usually 1–3 methods                               | **lint** `forgeconv-deps-surface`: go/types compares the methods a Deps interface declares with the methods the package calls through `s.deps.X` (and the interfaces it passes the value on as, below). It is an error for a _foreign_ interface with more than 10 methods and unused ones, and a warning otherwise. **Codegen** `forge fix narrow-deps <pkg>` writes the interface from the observed call set. `mock_gen` extends to every Deps interface, so narrowing costs no test work |
| **K2** Nothing receives a container      | no `*Graph`, `*Infra`, `*Components`, root config, `cmd.Deps`, locator or `any` | **compiler**: `internal/app` imports every component, so a component importing it is an import cycle. **lint** `forgeconv-no-container-deps` for the rest                                                                                                                                                                                                                                                                                                                                   |
| **K3** Config: own block only            | `Config config.XConfig`, never `*config.Config`                                 | **codegen** scaffolds a block per component. **lint** fails on root config in Deps and on `os.Getenv` in a component package. The conventional `Config` wiring is retired                                                                                                                                                                                                                                                                                                                   |
| **P1** Capabilities, not handles         | read-only views, scoped publishers, no downcasts                                | **types**: generated `db.<Entity>Reader` (reads only) beside `<Entity>Store`, and `natskit.ScopedPublisher{Subjects}`. **lint** `forgeconv-deps-no-downcast` fails a type assertion from a Deps field to a concrete type (reliant's `database.(*db.Repo)`, q2 C2). **runtime**, opt-in where a breach matters: a read-only pool on a read-only Postgres role (`dbrole` exists in control-plane), NATS credentials scoped to subjects                                                        |
| **P2** A process holds only its subgraph | a root builds its closure and nothing else                                      | **construction** (§3). **deploy**: `config_gen.k` emits each root's reachable config, secrets and env, and a workload bound to a root gets only those. The worker never receives the gateway's daemon-token secret. **lint** `config-reach` gains per-root over-grant (warning) and under-grant (error)                                                                                                                                                                                     |
| **P3** Trust domains do not touch        | daemon-audience handlers cannot read user-auth state                            | **boot + lint** audience check (§5). **lint** forbids a non-`user` audience handler package from importing the user-claims accessor                                                                                                                                                                                                                                                                                                                                                         |

K1 counts what the package passes a field's value on as, not only what it calls. A parameter, struct field or conversion of an interface type with methods is a role: the receiver can use the value only through that interface, so its whole method set counts as used. `forge fix narrow-deps` embeds those roles in the interface it writes. Only a value that leaves as `any`, as a concrete type or as a return value exempts the field, because then what the package needs can't be seen from here.

**Not enforceable, so not claimed:** ambient authority. That covers values smuggled through `context.Context`,
process globals (`slog.Default`, `http.DefaultClient`, package vars beyond what the exported-vars lint already
catches), reflection and `unsafe`, and a consumer that declares its own wide interface. Review catches those.
Lint only narrows them.

## 3. The graph and roots

**Discovery** is unchanged in spirit: every `Deps`+`New` package forge already discovers, plus provider funcs in
`providers.go` (`func New<X>(<X>Deps) (T, error)` produces `T`) and root structs. **Resolution** is by type, at
generate time, using today's `inject_gen.go` order (producer, then provider, then config by name). The changes:
a provider func replaces "Infra field", and an **ambiguous** match (two producers assignable) fails generate
until an owned value expression picks one. Names never resolve.

```go
// internal/app/compose.go — owned; one constructor per component, reconciled
func (g *Graph) newToolsDaemon() (*tools_daemon.Service, error) {
	return tools_daemon.New(tools_daemon.Deps{Daemons: g.Repo(), Events: g.DaemonEvents(),
		Config: g.Cfg.ToolsDaemon, Logger: g.Log})
}
func (g *Graph) newDaemonListener() (*DaemonListener, error) {
	l, err := serverkit.NewListener(serverkit.ListenerDeps{Config: g.Cfg.DaemonListener,
		Chain: g.DaemonChain(), Routes: g.DaemonRoutes()}) // ambiguous by type, so explicit
	return &DaemonListener{l}, err
}
// internal/app/graph_gen.go — generated
func (g *Graph) ToolsDaemon() *tools_daemon.Service { return build(g, &g.toolsDaemon, "tools_daemon", g.newToolsDaemon) }

// internal/app/roots.go — owned
//forge:root gateway command=server,gateway
type Gateway struct {
	Connector *gateway.DaemonConnector // siblings drain in field order: outbound dials stop first
	Daemon    *DaemonListener
}
```

- **Reachable subgraph only.** `BuildGateway(ctx, cfg, log)` calls the root's field accessors. Each accessor
  memoizes, so one call constructs that node's transitive closure in dependency order, once. A node no root
  reaches is never called, so it is never constructed and never dialled. Control-plane's hand-added
  `ServeSpec.NoKubernetesAPI` and `WorkerLeases` (`control-plane/cmd/control-plane/cmd/serve.go:186-197`) exist only
  because infra opens eagerly. Under the graph they become plain reachability.
- **Eager within a root, fail loud at boot.** `Build` forces the whole closure before anything `Run`s, then
  seals the graph. An accessor called after sealing panics, so nothing is ever constructed on a first request.
  A construction error unwinds with its path (`build gateway: daemon_listener → daemon_chain → daemon_auth: …`).
- **Cycles** are a generate error that names the edge, which is static from the Deps types. A runtime re-entry
  guard in `build` backs it up if a hand-edited value expression creates one. A real cycle is broken with a
  two-phase setter in compose.go, as today.
- **Completeness** replaces `RequireComplete` (`cmd-tree-serve.go.tmpl:364`). `forge lint --graph` fails when a
  proto service (unless `forge:unmanaged`), worker or operator is reachable from no root. It also fails on an
  unreachable provider and on a component no root reaches. One exception: an internal package that no node
  consumes yet (the state `forge scaffold package` leaves) is a pending warning, because a package runs
  nothing and is constructed only as a dependency. Once a consumer takes it, an unreached package fails like
  any other component. Boot checks only what generate cannot see: the audience check (§5).
- **Default.** A new project has one generated-default root, `server`: `APIListener` serving `UserRoutes`,
  plus every worker and operator. That is today's `server` command and nothing else, so the 80% case sees no
  new concept.
- **Deploy.** `forge.Workload{root = "gateway"}` replaces the free `args` string for forge-built binaries.
  `config_gen.k` emits `ROOTS = {gateway: {args, env, secrets}}`. Rendering fails on an unknown root, and the
  workload's env and secrets come from the root's reach (P2).

## 4. Lifecycle from the graph

**Boot:** construct the closure in topological order, then `Run` each runner in topological order. Infra
starts first and listeners start last, so a listener accepts traffic only after everything it reaches is up.
**Stop:** this is the root's policy, not a component. Flip readiness to 503, sleep `PreStopDelay`, then walk
the nodes in **reverse** topological order. For each node: `Drain`, cancel its `Run` ctx and wait, then
`Close`. The walk is bounded by the root's `ShutdownTimeout` and flushes telemetry last. Siblings follow
root-field order. Each phase logs counts (P9). Dependents always stop before what they depend on, so the order
is listener, then handlers, then workers, then infra, as the graph implies:

| Case (q3)                        | How it fits                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Long-lived streams (#773, P1/P2) | `serverkit.Listener` owns a `Streams` registry. Its `Drain` stops accepting, **soft-signals** every bound stream (`serverkit.Draining(ctx)` channel), waits until `deadline − DrainReserve`, hard-ends leftovers (cancel with cause `ErrServerDraining`, ending OK by default or `Unavailable` for procedures listed in `Config.RedialOnDrain`), then runs `http.Server.Shutdown` for unary. `ErrServerDraining` is registered as expected (P3) |
| Hijacked websockets, SSE (P4)    | the route is registered as `serverkit.LongLived(h)`, so its request ctx is bound into `Streams`. On the soft signal the terminal handler sends 1012 and returns, and its deferred PTY close runs. If the handler needs to wait for sessions, it implements `Drain` and stops after the listener                                                                                                                                                 |
| Gateway handover (#898, P5)      | **no `BeforeShutdown` hook.** `Gateway.Connector` is the first field, so outbound dials close first. The daemon listener's soft signal is the make-before-break trigger: the ToolsDaemon stream handler sends `GatewayDrain` and returns once the daemon has moved. Leftovers end with `Unavailable` (`RedialOnDrain`), and `Shutdown` gets the reserve                                                                                         |
| Worker's longer budget (B12)     | the worker root's `shutdown_timeout: 60s`, from its own config. KCL already derives `terminationGracePeriodSeconds` from it. The Temporal worker's `Drain` is `worker.Stop()` bounded by ctx                                                                                                                                                                                                                                                    |
| Health port stopped last (B5)    | probes are root policy, served last and stopped last. A separate `HealthAddr` remains a root field (q3 P7)                                                                                                                                                                                                                                                                                                                                      |

There is one genuine exception: stop order among siblings with no edge between them. It is a plain field order
in a struct the project owns. It is not a hook and not a new category.

## 5. Trust boundaries and chains as configuration

A chain is a component: `serverkit.NewChain(ChainDeps{Auth, Logger, …})`. It is today's inline chain
(`cmd-tree-serve.go.tmpl:112-233`) as a library. Every slot is defaulted (trace, recovery, request-id, logging,
metrics, deadline, audit, activity, stream-drain, protovalidate, payload caps) except **`Auth`, which is required**, so a missing
validator still fails at boot. Dropping a default needs an explicit value (`serverkit.NoAudit`). A nil
default is a `validateDeps` error. The gateway is two components (`DaemonAuth`, `DaemonChain`) and a listener
whose `Config` carries its own address, TLS, `MaxConcurrentStreams` and `ReadTimeout: 0` (P6). Nothing forks
`serve.go`.

**Audience.** A proto service declares `option (forge.v1.service) = {audience: "daemon"}` (default `user`;
control-plane's admin services become `operator`). An `Authenticator` reports `Audience()`. Three checks follow:
(a) **generate** emits `<Audience>Routes` per audience, so the default wiring cannot mix trust domains;
(b) **boot**: `Listener` reads each mounted service's descriptor option and refuses to start if it differs
from its chain's `Auth.Audience()`; (c) **lint** `--graph` proves the same statically
(listener → routes → services vs listener → chain → auth, with auth marked `//forge:audience daemon`). So a daemon
can never reach a user RPC, even through a hand-written route set.

## 6. Incremental adoption falls out (reliant's path to A)

Every node is a public constructor, `Graph` accessors are exported for unsealed use, and
`lifecyclekit.Supervise(ctx, policy, nodes...)` runs any hand-built node list. A hand-written `main` can adopt
one component at a time. Each step below ships alone, with three roles, one image and the same commands:

1. **Gateway on the library** (after forge PR 1–2). `servergateway` builds `DaemonListener` + `DaemonChain`
   and calls `Supervise`. `DaemonServer`'s listener and drain code is deleted. This is the smallest role and
   the hardest drain, so it is the best proof.
2. **Worker**: Temporal worker as a `Run`/`Drain` component under `Supervise`. Its hand-rolled health
   server and shutdown block are deleted.
3. **API**: `APIListener` + `UserChain` + `Streams`. `internal/drain`, `stream_drain.go` and the ~200 lines of
   listener plumbing in `grpc/server.go` are deleted.
4. **Narrowing** (can run in parallel with 1–3): `Repo db.Repository` → per-consumer interfaces via
   `forge fix narrow-deps`, root config → blocks, nil-able collaborators → required Deps (q2). The forge-API
   kill switch becomes `ForgeConfig.Enabled`: mounted, answering `FailedPrecondition`.
5. **Declare roots** (after PR 3–4): `roots.go` gets `api`, `worker` and `gateway`, compose.go absorbs the
   hand-wired value expressions, and `serverapi`, `serverworker` and `servergateway` are deleted.
   `command=server,api` keeps the KCL strings valid.
6. **Deploy** (after PR 6): `workloads.k:624-672` switch to `root = "…"`, and env and secrets come from reach.

## 7. What changes in forge (PR sequence)

| PR  | Change                                                                                                                                                                                                                                                                      | Files                                                                                                                                               | Size |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- | ---- |
| 1   | `Listener` (multi-instance, TLS/h2/timeouts, `Streams`, soft/hard drain, `LongLived`), `Chain` + `Authenticator` + audience boot check. `serverkit.Run` becomes a one-listener wrapper with no behaviour change. Covers q3 P1–P4, P6 and P8                                 | `pkg/serverkit/{listener,chain,streams}.go`, `run.go`                                                                                               | M    |
| 2   | `lifecyclekit`: the four interfaces, `Supervise` (topo start, reverse drain/close, budget, phase logs). The old `Worker` Start/Stop is adapted                                                                                                                              | `pkg/lifecyclekit/supervise.go`                                                                                                                     | S–M  |
| 3   | Graph codegen: provider funcs replace `Infra`/`OpenInfra`, the per-component constructor-method compose.go and its reconciler, `graph_gen.go` (memo, seal, `Build`/`Run<Root>`, plan, cycles), `//forge:root` discovery, per-audience `routes_gen.go`                       | `internal/codegen/{inject_gen,build_topo,compose_membership,deps_parser}.go`, `compose.go.tmpl`, `providers.go.tmpl`, `mounts_services_gen.go.tmpl` | L    |
| 4   | Commands: `roots_gen.go` replaces `Serve`/`ServeSpec`/`MountFunc`/`MountNone`, and `cmd-svc-group`/`cmd-worker-group` are retired (Q5)                                                                                                                                      | `cmd-tree-serve.go.tmpl` (deleted), `cmd-tree-server.go.tmpl`, `lifecycle.go.tmpl` (deleted)                                                        | M    |
| 5   | Lint: `deps-surface`, `no-container-deps`, `deps-no-downcast`, root-config/getenv, `--graph` (reachability, audience, imports), plus `forge fix narrow-deps`, and `mock_gen` for Deps interfaces                                                                            | `internal/linter/forgeconv/`, `internal/contractcheck/`                                                                                             | M    |
| 6   | KCL: `Workload.root`, `ROOTS` in `config_gen.k`, per-root env/secret projection, unknown-root render error. This retires `binary_config.command` (Q4)                                                                                                                       | `kcl/workload.k`, `internal/codegen/config_gen.go`                                                                                                  | M    |
| 7   | Upgrade playbook and codemod: `c.X = pkg.New(pkg.Deps{…})` → `newX` methods (mechanical, `infra.Y` → `g.Y()`), `OpenInfra` body → provider funcs (codemod plus playbook), custom `ServeSpec` fields → roots/components (control-plane's three flags are the worked example) | `migrations/vX-composition-graph`, `internal/cli/upgrade`                                                                                           | S–M  |

PRs 1–2 ship value alone, since every forge app with a server stream inherits #773 today. PR 3 is the
keystone. PR 5's new rules land as warnings and become errors one release later.

## 8. Devil's advocate

- **"This is a DI framework."** It would be one if it grew scopes, qualifiers, provider sets or runtime
  lookup. Hard limits: identity is the Go type, ambiguity is resolved by an owned expression, the only scope is
  per process, and the output is straight-line Go. Request-scoped things are ctx values, never components.
- **Compile-time vs boot-time.** Graph shape errors (missing producer, ambiguity, cycle, unreachable, audience)
  fail at generate. Type errors fail at compile. Connectivity errors fail at boot, before `Run`. Nothing fails
  at first request.
- **Debuggability of generated wiring.** Every edge is an owned value expression in compose.go, and stack traces
  run through named accessors. `forge project graph --root gateway [--dot]` prints the tree with each node's
  lifecycle methods and config block.
- **Interface proliferation and drift.** K1 means many tiny interfaces with signatures duplicated across
  consumers. We accept that (WET at boundaries, per project convention). Codegen writes them (`narrow-deps`)
  and their mocks, and a producer signature change surfaces as one compile error in compose.go.
- **Adapter boilerplate.** Knowledge narrowing needs no adapter, because Go satisfies narrow interfaces
  structurally. Only privilege narrowing (read-only, subject-scoped) needs a wrapper, and those ship as library
  types.
- **Two-phase init.** True cycles stay explicit setters. "Needs a started dependency" is handled by `Run`
  order, so no `Start` returns values.
- **Where uniformity hurts.** The root's stop policy (readiness flip, pre-stop) is not a node, because forcing
  it into the graph would need a fake edge to everything. It stays a root field. Lists of N handlers are
  generated route sets, not 35-field hand-written Deps.
- **Not components:** per-request values (claims, tx, request logger), pure values and functions (money, ids),
  the root config struct, protos and generated stores (a store is a view of the `Repo` component), and cobra
  commands (they select roots).

## 9. Decisions (owner, 2026-10-19: every recommendation accepted)

1. **Instance identity:** a distinct Go type per instance (`type DaemonListener struct{ *serverkit.Listener }`). No name qualifiers.
2. **Deps-surface severity:** error for a foreign interface with more than 10 methods and unused ones; warning otherwise. Tighten once reliant has narrowed its Deps.
3. **Privilege enforcement:** types and lint in v1. Runtime capabilities (a read-only DB role, subject-scoped NATS credentials) are opt-in per component, the DB role first.
4. **Per-root config:** derived from each root's reach. `binary_config.command` is retired.
5. **Per-service and per-worker subcommands:** retired.
6. **Audience:** a proto service option, `option (forge.v1.service) = {audience: "daemon"}`, default `user`.
