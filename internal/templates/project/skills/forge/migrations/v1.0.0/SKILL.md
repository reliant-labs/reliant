---
name: v1.0.0
description: Upgrade a project to forge v1.0.0. One section per change — (1) the interceptor chain's Deadline slot, ahead of Auth: every unary RPC is now bounded by a 30s server deadline by default and watched for slow calls, and `(forge.v1.method).timeout` is enforced through `middleware.ProcedureDeadlines`, which an owned chain builder passes as `Deadline: serverkit.Deadline{Procedures: middleware.ProcedureDeadlines}`. Use when cmd/<bin>/cmd/serve.go or internal/app/providers.go calls serverkit.NewChain or observe.Chain and names no Deadline, or when a project hand-wraps a timeout or watchdog interceptor around its authenticator. (2) The composition graph upgrade path: `forge fix composition-graph` converts the legacy Infra / OpenInfra / NewComponents / lifecycle.go / ServeSpec composition and the per-service, per-worker and per-operator subcommands to providers, compose.go constructor methods and roots, and lists — writing nothing — every shape it cannot convert without deciding for you; ServeSpec knobs become reachability (control-plane's OperatorListener, NoKubernetesAPI and WorkerLeases are the worked example); deploy binds workloads to roots. Use when internal/app declares `type Infra struct` or `func NewComponents`, or a graph project moves to binary_config. (3) Configurable probe paths: `serverkit.Config.Probes` (liveness_paths / readiness_paths config fields, default /healthz and /readyz), honoured by the boot probes, serverkit.Run and `serverkit.Probes.Paths`, with `Probes.Handle` for extra routes such as /flow-health, and read by the KCL lowering for the default probe. Use when cmd/<bin>/cmd/serve.go builds `&serverkit.Probes{` with no `Paths:`. (4) Behavior change: every pod forge renders sets `enableServiceLinks: false` (Deployment, Job, CronJob, the node agent's DaemonSet), so Kubernetes no longer injects `<SERVICE>_PORT=tcp://ip:port` / `<SERVICE>_SERVICE_HOST` / `<SERVICE>_SERVICE_PORT` env vars for every Service in the namespace; `enableServiceLinks = True` on a `fw.Workload` opts one back in. Use when a deploy/kcl workload reads `*_SERVICE_HOST`, `*_SERVICE_PORT` or a `tcp://` `*_PORT` variable it never declares, or when a typed config field named like a Service (`TEMPORAL_PORT`, `ZITADEL_PORT`) was shadowed by hand. (5) Explicit transaction isolation: pkg/orm has no default level — RunTx, RunTransaction and BeginDetached take an orm.TxOptions (orm.Serializable, orm.ReadCommitted, orm.RepeatableRead), RunTxWithOptions, RunTransactionWithOptions and Client.BeginTx are gone, and `forge fix tx-isolation` spells every old call out exactly as it ran; then review forgeconv-tx-lock-under-serializable. Use when Go code calls RunTx(ctx, fn), RunTxWithOptions, RunTransaction(ctx, fn), RunTransactionWithOptions or an orm Client's BeginTx. (6) Hand-built Connect errors: a *connect.Error is never redacted, so connect.NewError(code, err) publishes err.Error(); `forge fix connect-errors` rewrites the fixed-message sites to svcerr with the same code and message and lists the rest for svcerr.WrapOr, forgeconv-handler-connect-error warns on every connect.NewError in a handler package, and tdd.AssertConnectError reads svcerr.Code. Use when internal/handlers calls connect.NewError. (7) local alert delivery (the `alertmanager` and `alerts` workloads). Use when deploy/kcl/dev/main.k declares `_observability` but no `alertmanager_port`. (8) Dev images off Docker Hub: docker-compose.yml pulls from public.ecr.aws, ghcr and mirror.gcr.io, pinned by digest. Use when a docker-compose.yml image names no registry host. (9) Project-unique config protos: `forge fix config-proto-package`. Use when proto/config/v1 declares package config.v1. (10) One double per interface: the test harness installs a Deps field's forge Mock<Role> (mock_gen.go) instead of a Stub<Field>; `forge fix harness-mocks`. Use when a handler test names a Stub<Field> whose interface has a mock_gen.go. (11) activity.Deps.Principal is required; NewChain refuses an Audit/Activity it cannot attribute. Use when Go code calls activity.Interceptor. (12) The library Button: `loading` replaces `isLoading` (spinner, no width change, aria-busy), sizes are xs-xl on a denser 24-40px scale, leftIcon/rightIcon/icon slots, href renders through the link primitive; reinstall with `forge component install button`. Use when an installed button.tsx declares `isLoading?: boolean`. (13) `pgtest.StartEmbedded` returns `*pgtest.Embedded`. Use when Go code keeps its result as `*embeddedpostgres.EmbeddedPostgres`.
version: v1.0.0
automation: fix:composition-graph, fix:connect-errors
detection: grep -qsE '^type Infra struct|^func NewComponents\(' internal/app/*.go && exit 0; for f in cmd/*/cmd/serve.go internal/app/providers.go; do test -f "$f" && grep -qsE 'serverkit\.NewChain|observe\.Chain\(' "$f" && ! grep -qsE 'Deadline:|ProcedureDeadlines' "$f" && exit 0; done; for f in cmd/*/cmd/serve.go; do test -f "$f" && grep -qs '&serverkit.Probes{' "$f" && ! grep -qs 'Paths:' "$f" && exit 0; done; grep -qsE 'fw\.Workload|forge\.workloads' deploy/kcl/*.k deploy/kcl/*/*.k deploy/kcl/lib/*.k && exit 0; grep -rqsE --include='*.go' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules '\.(RunTxWithOptions|RunTransactionWithOptions)\(|orm\.IsolationDefault|\.(RunTx|RunTransaction)\(ctx, func' . && exit 0; test -d internal/handlers && grep -rqs --include='*.go' --exclude='*_gen.go' --exclude='*_test.go' 'connect\.NewError(' internal/handlers && exit 0; (grep -qs '^_observability = ' deploy/kcl/dev/main.k && ! grep -qs 'alertmanager_port' deploy/kcl/dev/main.k) && exit 0; grep -qsE '^[[:space:]]+image:[[:space:]]+"?([a-z0-9_-]+/)?[a-z0-9._-]+(:[A-Za-z0-9._-]+)?(@sha256:[0-9a-f]+)?"?[[:space:]]*$' docker-compose.yml && exit 0; grep -qs '^package config\.v1;' proto/config/v1/*.proto && exit 0; test -d internal/handlers && grep -rqsE --include='*_test.go' --exclude='helpers_gen_test.go' '[^A-Za-z0-9_]Stub[A-Z][A-Za-z0-9_]*[{)]' internal/handlers && exit 0; grep -rqsE --include='*.go' --exclude-dir=gen 'activity\.(Interceptor|DefaultActor)\(' . && exit 0; grep -rqs --include=button.tsx --exclude-dir=node_modules 'isLoading?: boolean' frontends packages && exit 0; grep -rqsE --include='*.go' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules 'pgtest\.StartEmbedded\(' . && exit 0; for f in package-lock.json frontends/*/package-lock.json; do test -f "$f" && exit 0; done; exit 1
---

# Upgrading to v1.0.0

Each section below is independent: apply the ones whose "Use when" matches.

## 1. The Deadline slot: a per-RPC deadline that bounds authentication

Use when `cmd/<bin>/cmd/serve.go` or `internal/app/providers.go` calls
`serverkit.NewChain` or `observe.Chain` and sets no `Deadline:`.

### What changed

`serverkit.ChainDeps` has a `Deadline` slot, between metrics and auth
(`observe.Deps.Deadline` is the same slot in `observe.Chain`, which
`NewChain` builds on):

    trace → recovery → request-id → logging → tracing → metrics →
    deadline → auth → audit → rate-limit → extras → activity → stream-drain →
    protovalidate → svcerr (`observe.Chain` always ends with it)

It runs **before** Auth, so authenticating — a token-store query, an
introspection call — runs inside the RPC's budget, and a stalled credential
lookup ends as `DeadlineExceeded` instead of holding the request until the
client gives up. `Extras` run after Auth and cannot do that, which is why it
is a slot.

**The default applies without any edit, in both chains.** One rule holds for
`serverkit.ChainDeps.Deadline` and `observe.Deps.Deadline` alike: nil is
`serverkit.Deadline{}`, `serverkit.NoDeadline` opts out, and anything else is
used as given. A project that composes with `observe.Chain` directly — an
explicit-collaborator `serve.go`, like control-plane's — gets the default
too. A nil `Deadline` is:

- every unary RPC is bounded by `serverkit.DefaultUnaryDeadline` (30s);
- streams are unbounded;
- a unary RPC still running after `DefaultSlowAfter` (10s) is logged at WARN
  (`rpc still in flight`), again every `DefaultSlowRepeat` (30s), and once
  more when it completes (`rpc completed slowly`).

The server budget is a ceiling: a client's shorter deadline still applies; a
longer one no longer lifts it.

`(forge.v1.method).timeout` used to be documented and read by nothing. It is
now projected into `pkg/middleware/procedures_gen.go` as
`middleware.ProcedureDeadlines` on every `forge generate`.

### What to do

1. **Find unary RPCs that legitimately run longer than 30s** — exports,
   long provisioning calls, anything waiting on a person (an OAuth sign-in).
   Declare each one's budget in its proto, next to the rpc:

   ```protobuf
   rpc Export(ExportRequest) returns (ExportResponse) {
     option (forge.v1.method) = { timeout: { seconds: 120 } };
   }
   ```

   A negative timeout (`{ seconds: -1 }`) is no server deadline.

2. **Pass the projection to your chain.** `serve.go` / `providers.go` are
   yours, so forge will not make this edit:

   ```go
   chain, err := serverkit.NewChain(serverkit.ChainDeps{
       Auth:     ...,
       Logger:   logger,
       Deadline: serverkit.Deadline{Procedures: middleware.ProcedureDeadlines},
       ...
   })
   ```

   On `observe.Chain` it is the same field:

   ```go
   interceptors := observe.Chain(observe.Deps{
       Logger:   logger,
       Deadline: serverkit.Deadline{Procedures: middleware.ProcedureDeadlines},
       Auth:     ...,
   })
   ```

   A Deadline that names no `Logger` logs its slow calls through the
   chain's `Logger`, in both chains. A chain that already passes a
   `serverkit.Deadline` keeps exactly what it set.

3. **Delete any hand-rolled timeout or slow-call interceptor**, in particular
   one merged into the `Auth` slot so that it would wrap authentication. Its
   budgets go in the proto (step 1) or in `Deadline.Procedures`; its default
   in `Deadline.Unary`; its thresholds in `SlowAfter` / `SlowRepeat`; a
   metric it bumped in `Deadline.OnSlow`, which replaces the WARN log (log
   and count in the one func).

4. Run `forge generate`, then `go build ./...`.

To keep the old behaviour — no server deadline at all — set
`Deadline: serverkit.NoDeadline`. That leaves a wedged handler holding its
request until the client gives up, with nothing reporting it.

## 2. The composition graph: off Infra, NewComponents and ServeSpec

Use when `internal/app` declares `type Infra struct` or `func NewComponents`
(the legacy composition), or when a graph project moves to `binary_config`.

### What changed

A project composes through the composition graph
(`forge skill load architecture`, "The composition graph"): every part of a
process is a component built from its Deps, providers in `providers.go` replace
the eager `OpenInfra`, `compose.go` holds one constructor method per node, and a
**root** (`//forge:root <name>`) selects what a process builds — one command per
root, run by `runRoot` in `serve.go`. Each root's config is its reach
(`config.LoadReach`, KCL `ROOTS`), `forge lint --graph` proves reach and
audience, and the least-knowledge rules judge every component's Deps.

A project still on the legacy composition generates exactly as before, and
none of that applies to it. Nothing migrates it implicitly: run the codemod.

### Step 1 — the codemod

Commit first; the codemod replaces files you own.

```bash
forge fix composition-graph --dry-run   # what it would do, or what blocks it
forge fix composition-graph
forge generate && go build ./... && forge lint --graph
```

It is all or nothing. On the composition forge scaffolded and reconciled it:

- re-scaffolds `internal/app/providers.go` (Infra's stock fields become the
  graph's inputs and providers: `infra.Log` → `g.Log`, `infra.Cfg` → `g.Cfg`,
  `infra.DB` → `g.DB().DB`, `infra.ORM` → `g.ORM()`, `infra.DefaultClient()` →
  `g.HTTPClient()`) and `cmd/<bin>/cmd/serve.go` (as `runRoot`);
- turns each `NewComponents` construction into a `func (g *Graph) new<X>()`
  method in `compose.go`, keeping every Deps value you set (`c.Tasks` and a
  producer local become `g.Tasks()`); a compose.go holding only the
  conventional Logger/Config values is re-scaffolded instead;
- deletes `lifecycle.go`, `server.go`, `mounts_services_gen.go` and
  `cmd/<bin>/cmd/{services,workers,operators}/`; rewrites `root.go`
  (`root.AddCommand(rootCommands(deps)...)`, no group factories) and
  `main.go` (`cmd.Execute()`); retypes `APIKeyValidator(*Infra)` to `*DB`; and
  points the e2e harness at `server`.

The one root afterwards is the generated default, `server`: every service,
worker and operator — what `<bin> server` ran. The retired per-component
subcommands are listed; a workload in `deploy/kcl` that runs one is named
too.

Anything it cannot map without deciding for you is listed with its file:line,
and **nothing is written**. A conversion that compiles but changes what a
process serves — collapsing two deployment groups into one `server`, say — is
worse than none. Clear each item, then run it again.

### Step 2 — clearing what it lists

| It lists | What to do |
|---|---|
| `Infra.<F>` is infrastructure forge did not scaffold | Make it a provider in `providers.go`: `func New<F>(<F>Deps) (T, error)`, built from the `OpenInfra` lines that set it, with what those lines read as `<F>Deps` fields. A root that does not reach it never opens it. Give two values of one type distinct named types (`type ReadPool struct{ *sql.DB }`). |
| a declaration in `providers.go` / `serve.go` / `server.go` / a command group | Move it to a file of its own first (a provider if it opens something; a chain or edge option belongs in `NewUserChain` / `NewAPIListener`). |
| `OpenInfra` takes options; a `ServeSpec.<Knob>` | Reach — see the worked example below. |
| `NewComponents` wiring forge did not scaffold (sub-builders, locals, conditionals) | Write each component's `new<X>` method in `compose.go` by hand: one node per method, its Deps values naming other nodes (`g.Repo()`), the graph inputs (`g.Cfg`, `g.Log`, `g.Ctx`) or providers. A conditional value is the method's body (`if g.Cfg.X == "" { … }`). A true cycle is a `func() T` field (a lazy edge). |
| supervision forge did not scaffold (`lifecyclekit.Singleton`, a wrapper) | A component of its own with `Run` (and `Drain`/`Close`): `type LeasedReaper struct{ … }`, `func NewLeasedReaper(LeasedReaperDeps) (*LeasedReaper, error)`. The graph supervises it from its methods. |
| `RunOperators` sets `operatorkit.Options.<X>` | The generated controller manager does not take it from the project: run the operators under a root of their own with a hand-written manager node. |
| `<X> is retired by the graph` (wire files, tests, `cmd.Serve` callers) | Rewrite over the graph: a test builds the node it tests from its constructor, or a `Graph{…}` and its accessor; a command that called `Serve` with its own spec is a root. |
| a per-component subcommand with flags or setup | A root (`//forge:root <name> command=<name>`), with the setup in `runRoot` (branch on `root.Name`) or a provider. |

### Worked example: control-plane's ServeSpec knobs

control-plane's `cmd/control-plane/cmd/serve.go` adds three `ServeSpec`
fields, and four commands hand `Serve` a spec: `public-api`, `admin-api`,
`workers`, `workspace-controller`. No process serves both service groups.
Under the graph each command is a root, and each knob is whether that root
reaches the component it switched:

- **`WorkerLeases`** opened `Infra.Lease` only for `workers`. The lease
  connection becomes a provider (`func NewLease(LeaseDeps) (*leasekit.Locker,
  error)`); each singleton worker becomes a component taking it
  (`LeasedBillingEnforcement{Lease, Worker}`, `Run` = acquire, then run). Only
  the `workers` root reaches them, so only that process dials the lease.
- **`NoKubernetesAPI`** stopped `admin-api` constructing a Kubernetes client.
  The client becomes a provider (`NewK8sClient`); the components that read it
  take it in their Deps. The `admin-api` root reaches none of them, so it never
  builds one — and `TestServeProcessesProbeKubernetesOnlyWithAMountedToken`
  becomes a reach assertion over the generated roots.
- **`OperatorListener`** selected the fail-closed operator validator chain. That
  is a second trust domain: an `OperatorAuth` provider marked
  `//forge:audience operator`, an `OperatorChain`, and an `AdminListener` serving
  `OperatorRoutes` — every admin service declares
  `option (forge.v1.service) = {audience: "operator"}`. `forge lint --graph` and
  the listener's boot check then prove what control-plane's hand-written
  `deployments.go` asserts today: no end-user listener reaches an admin rpc.

```go
// internal/app, conventionally roots.go — yours
//forge:root public-api
type PublicAPI struct{ API *APIListener }

//forge:root admin-api
type AdminAPI struct{ Admin *AdminListener }

//forge:root workers
type Workers struct {
	BillingEnforcement *LeasedBillingEnforcement
	// … one field per worker; field order is stop order
}

//forge:root workspace-controller
type WorkspaceController struct{ Operators *Operators }
```

The command names stay, so `deploy/kcl` keeps running the same args.

### Step 3 — deploy

`deploy/kcl` is untouched by the codemod and keeps running the same commands.
To give each workload exactly what its root reaches, bind it to the root and
project its env from the root's reach (this changes the rendered env, which is
the point — a worker no longer receives the API's secrets):

```kcl
api = fw.Workload {
    name = "api"
    build = forge.GoBuild {cmd = "./cmd/<bin>", output_name = "<bin>"}
} | config_gen.root_of("server")
# and in each env:
w | {env = config_gen.workloadEnvMap(appcfg.app_config, w) | w.env}
```

A graph project that moves to `binary_config` (per-command configs) needs three
edits by hand:

1. Each env file passes `appcfg.<bin>_process` where it passed
   `appcfg.app_config`.
2. `deploy/kcl/<env>/config.k` is re-emitted with
   `forge project rescaffold <path>` — deleting it records that you took
   ownership, so `forge generate` will not bring it back.
3. The scaffolded `_api` workload is rebound to a root that exists
   (`config_gen.root_of("<root>")`); rendering fails on an unknown root.

## 3. Configurable probe paths

Use when `cmd/<bin>/cmd/serve.go` builds `&serverkit.Probes{` with no
`Paths:`, or when a platform probes the process on a path other than
`/healthz` / `/readyz`. `serverkit.Config.Probes` names the probe paths
(`liveness_paths` / `readiness_paths` config fields; default `/healthz` and
`/readyz`), and the KCL lowering's default probe reads the same values. Load
`forge skill load migrations/v1.0.0/probe-paths` for the `serve.go` change
and the config fields.

## 4. Pods no longer get service-link env vars (`enableServiceLinks: false`)

Use when a workload reads `<SERVICE>_SERVICE_HOST`, `<SERVICE>_SERVICE_PORT` or
a `tcp://` `<SERVICE>_PORT` it never declares, or a config key was shadowed by
hand to dodge one (`TEMPORAL_PORT = "7233"`). Every forge-rendered pod now sets
`enableServiceLinks: false`; `enableServiceLinks = True` on a `fw.Workload`
opts one back in. Load `forge skill load migrations/v1.0.0/service-links`.

## 5. Explicit transaction isolation: no default level

Use when Go code calls `RunTx(ctx, fn)`, `RunTxWithOptions`,
`RunTransaction(ctx, fn)`, `RunTransactionWithOptions`, or an `*orm.Client`'s
`BeginTx`. pkg/orm's openers now take a policy that names the level and
carries the attempt budget (`orm.Serializable`, `orm.ReadCommitted`); the old
calls no longer compile, and an unset level is rejected at run time.
`forge fix tx-isolation` (run by `forge project upgrade`) spells every old
call out exactly as it ran — then the level choice is yours. Load
`forge skill load migrations/v1.0.0/tx-isolation` for the table, the
codemod's report and the review step.

## 6. Hand-built Connect errors → `svcerr`

Use when a handler package calls `connect.NewError`: a `*connect.Error` is
never redacted, so `connect.NewError(code, err)` publishes `err.Error()`.
`forge fix connect-errors` (on request) rewrites each site an svcerr spelling
reproduces exactly and lists the rest for `svcerr.WrapOr`;
`forgeconv-handler-connect-error` warns on each. Load
`forge skill load migrations/v1.0.0/connect-errors`.

## 7. Local alert delivery: an Alertmanager and an evaluator beside ClickStack

Use when `deploy/kcl/dev/main.k` declares `_observability` but no `alertmanager_port`.
`forge env up` now evaluates the env's `forge.Alerts` continuously (an `alerts`
host workload) and delivers them through a compose `alertmanager` configured by
the control plane's own renderer. Load
`forge skill load migrations/v1.0.0/local-alerting` for the compose service, the
two KCL ports and workloads, and the check.

## 8. Dev images no longer pull from Docker Hub

Use when `docker-compose.yml` has an `image:` with no registry host
(`postgres:17-alpine`, `clickhouse/clickstack-local:…`, `otel/…`). Docker
Hub's anonymous limit is per IP, so a shared address fails `forge env up`
with `toomanyrequests`. Load `forge skill load migrations/v1.0.0/image-registries`
for the references.

## 9. Config protos are project-unique

Use when `proto/config/v1/*.proto` declares `package config.v1`: run
`forge fix config-proto-package`, then load
`forge skill load migrations/v1.0.0/config-proto-package`.

## 10. One test double per interface: the harness installs forge's mocks

Use when a handler test names a `Stub<Field>` (`StubRepo`). For a Deps field
whose interface forge mocks in a `mock_gen.go`, the harness installs that
`Mock<Role>` and declares no stub. `forge fix harness-mocks` (run by
`forge project upgrade`) renames each reference; an unset method answers
`contractkit.MockNotSet`. Load `forge skill load migrations/v1.0.0/harness-mocks`.

## 11. The activity and audit caller is never guessed

Use when Go calls `activity.Interceptor` or `NewChain` cannot read the caller:
load `migrations/v1.0.0/activity-caller`.

## 12. The library Button: `loading`, a denser size scale, icon slots

Use when an installed `button.tsx` (under `frontends/` or `packages/`)
declares `isLoading?: boolean`. The component is `overwrite: once`, so an
existing project keeps the old one until `forge component install button`
replaces it; then `isLoading` becomes `loading` and the size names move to a
24–40px scale (`md` is 32px, not 40px). Load
`forge skill load migrations/v1.0.0/button` for the table, the rename and
the size mapping.

## 13. `pgtest.StartEmbedded` returns `*pgtest.Embedded`

Use when Go code calls `pgtest.StartEmbedded` and keeps the result as a
`*embeddedpostgres.EmbeddedPostgres`: load
`forge skill load migrations/v1.0.0/pgtest-embedded`.

## 14. pnpm scaffold default (optional adoption)

Existing npm projects keep working. To adopt pnpm 12.15.0, load
`migrations/v1.0.0/pnpm`: import the lockfile, move overrides/build approvals,
fix undeclared dependencies, update owned CI/Docker, and use a mount-local store.

## 15. The shape bound is 512 KiB

Use when a column storing a shape has a size CHECK: load
`migrations/v1.0.0/shape-bound`.
