---
name: migration-v1.0.0-composition-graph
description: Move a project off the legacy Infra / OpenInfra / NewComponents / lifecycle.go / ServeSpec composition onto the composition graph — adopt one root at a time with `//forge:root <name> adopt`, run `forge fix composition-graph`, clear each shape it lists (Infra fields become providers, NewComponents wiring becomes compose.go constructor methods, ServeSpec knobs become reach, with control-plane's as the worked example), then bind deploy workloads to roots.
relevance: migration
---

# v1.0.0 — the composition graph: off Infra, NewComponents and ServeSpec

Use when `internal/app` declares `type Infra struct` or `func NewComponents`
(the legacy composition), or when a graph project moves to `binary_config`.

## What changed

A project composes through the composition graph
(`forge skill load architecture`, "The composition graph"): every part of a
process is a component built from its Deps, providers in `providers.go` replace
the eager `OpenInfra`, `compose.go` holds one constructor method per node, and a
**root** (`//forge:root <name>`) selects what a process builds — one command per
root, run by `runRoot` in `serve.go`. Each root's config is its reach
(`config.LoadReach`, KCL `ROOTS`), `forge lint --graph` proves reach and
audience, and the least-knowledge rules judge every component's Deps.

A project still on the legacy composition generates exactly as before, and
none of that applies to it. Nothing migrates it implicitly: run the codemod —
or, when converting everything at once is too big a step, adopt one root at a
time first (below).

## Adopting one root at a time

Mark a root struct `adopt` and only that root comes onto the graph; the legacy
composition stays, and is generated exactly as before:

```go
// internal/app/roots.go (yours)

// PublicAPI serves the public rpcs from the graph.
//
//forge:root public command=public-api adopt
type PublicAPI struct {
	Listener *PublicListener
}
```

`forge generate` then writes, beside the legacy files:

- `internal/app/graph_compose.go` (yours, reconciled like compose.go): one
  constructor method per node the adopted roots reach, and nothing else;
- `internal/app/graph_gen.go` and `routes_gen.go`: the graph of exactly that
  reach — no default `Server` root, since the legacy server is still the server;
- `cmd/<bin>/cmd/roots_gen.go`: one command per adopted root, once the command
  tree declares `runRoot` (generate says what to add) and `root.go` adds
  `rootCommands(deps)`.

Give the nodes providers of their own (`func New<X>(<X>Deps) (T, error)` in
an internal/app file). An adopted root may not reach anything that builds the
legacy composition: a provider or constructor that names `Infra`, `OpenInfra`,
`NewComponents` or `Components` fails generate, naming the root and the node,
so no process ever builds both. A `//forge:root` without `adopt` stays inert
beside the legacy composition, as before.

Not yet projected for adopted roots: `deploy/kcl`'s `ROOTS` and
`pkg/config/roots_gen.go`, so a workload keeps the legacy env until the project
is in graph mode. When every process is a root, run the codemod: it asks for
graph_compose.go's methods to move into compose.go, which it rewrites.

## Step 1 — the codemod

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

## Step 2 — clearing what it lists

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

## Worked example: control-plane's ServeSpec knobs

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

## Step 3 — deploy

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
