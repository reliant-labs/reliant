---
name: services
description: Scaffold and wire new services, packages, and frontends in a Forge project.
---

# Adding New Components to a Forge Project

Use this skill whenever you need to introduce a new network-facing service, internal package, or frontend into a Forge mono-repo. Every scaffolded service comes with production middleware baked in — structured logging, distributed tracing, auth, rate limiting, and graceful shutdown — so you focus on business logic from day one.

## Choosing the Right Command

| I need…                                      | Command                        | What it creates                          |
| -------------------------------------------- | ------------------------------ | ---------------------------------------- |
| A new network-facing API (Connect RPC)       | `forge scaffold service <name>`     | Proto definition, generated stubs, Go service skeleton |
| A background worker                          | `forge scaffold worker <name>`      | Worker with Start/Stop lifecycle |
| A cron-scheduled worker                      | `forge scaffold worker <name> --kind cron --schedule "..."` | Worker with cron scheduler |
| A one-shot step that must finish before something else starts | declare a `kind = "job"` workload in `deploy/kcl/workloads.k` and bind it per env (see below) | Ordering primitive — no scaffold needed |
| An internal Go package with interface contract | `forge package new <name>`   | Package directory with contract interface and default implementation |
| A Next.js web frontend                       | `forge scaffold frontend <name>`    | Next.js app wired into the project |
| A React Native mobile frontend               | `forge scaffold frontend <name> --kind mobile` | Expo app with Connect-web transport |

## The fast path: scaffold everything the protos imply

The per-component commands above are the granular path. When you author the proto directly, there is a one-command batch alternative: mark each entity message with a leading `// forge:entity` comment, add your custom RPCs, then run **`forge scaffold`**. It births every marked entity (missing CRUD quintet injected + owned create-table migration) and runs generate, which emits a pb-through handler stub for every custom RPC — in one visible, phased run (`--dry-run` plans; a re-run with nothing missing is a clean no-op). In dev the app boots alive: `forge env up` auto-seeds a fresh DB from the applied schema. The entity/seed halves live in the `db` skill, the pb-through RPC handlers in `api`, and the standalone domain package (when you extract one) in `service-layer` / `contracts`.

## One-shot steps: the `job` workload kind

When something must **run once to completion before something else starts** —
register an OAuth client against the IdP, provision a bucket, load a fixture —
that is a `job`. Every other workload kind is long-running: `service` /
`worker` / `operator` run forever, `cron` runs on a schedule forever, and a
`tool` is never scheduled at all. `job` is the one that ends.

Declare it ONCE in `deploy/kcl/workloads.k`, like every workload, and bind
it in each env that runs it. Ordering (`before`) is part of the declaration;
WHERE it runs is the env's binding:

```kcl
# deploy/kcl/workloads.k
provision_idp = fw.Workload {
    name = "provision-idp"
    kind = "job"
    build = forge.GoBuild {cmd = "./cmd/myproj", output_name = "myproj"}
    args = ["provision-idp"]  # a subcommand of the project binary
    before = ["api"]          # the API does not start until this exits 0
}

# deploy/kcl/dev/main.k — one binding line
_workloads = [
    _on_host_job(wl.provision_idp)
    _on_host(_api)            # the binary's `server`: every service
]
```

`before` is the ordering declaration and it is the point — a one-shot with no
ordering is just a cron that fires once. Each runtime honours it:

| runtime | lowering |
| ------- | -------- |
| cluster (`forge.OnCluster`) | an **init container** on each workload named in `before`, rendered by `pkg/deploy.RenderWorkloads`. It is the only ordering Kubernetes enforces itself, so it holds under any applier. A gating job renders **no** standalone `Job` — that would run the command a second time, unordered. |
| cluster, `before` empty | a standalone `batch/v1` Job, pre-rollout by default (`forge env deploy` waits for it before applying Deployments); `deployPhase = "post-rollout"` defers it. |
| hosted (`forge.OnHosted`) | the control plane runs it the same way, before the workloads it gates. |
| host (`forge.OnHost`) | `forge env up` runs `go run ./cmd/<p> <args>`, waits for exit 0, and only then launches the dependents. Fail-closed: a job that exits non-zero stops the up. |
| compose (`forge.OnCompose`) | the compose file owns it: `restart: "no"` plus `depends_on: {<job>: {condition: service_completed_successfully}}` on each dependent. |

**The job's command must be idempotent.** The k8s lowering fans it out to one
init container per dependent, and init containers re-run on every pod start,
every replica and every restart. That is the same contract the deploy-time
migration step already keeps (migratekit takes a postgres advisory lock),
and a one-shot that is not safe to repeat is unsafe under any retrying
orchestrator.

Things forge rejects at load time rather than at 3am: a `before` naming a
component that does not exist, a job with no `command`, a job carrying a
`schedule` (use `kind = "cron"`), a workload an env lists without binding a
runtime, and — on the host path — a cycle in the job graph.

## Wiring Cycle

Follow this sequence every time you scaffold a new component:

1. **Scaffold** — run the appropriate `forge scaffold` or `forge package new` command.
2. **Define the contract** — edit the `.proto` file (services) or the interface (packages).
3. **Generate** — run `forge generate` to produce Go code from protos and contracts (handler stubs, mocks, Connect clients).
4. **Implement** — write the business logic on the `*Service` methods in `internal/handlers/<svc>/` using `s.deps` (a custom RPC is a pb-through method on `*Service`; CRUD delegates via `handlers_crud.go`). For a reusable, transport-free domain layer, scaffold a standalone package (`forge scaffold package <name>`) and call it from the handler.
5. **Compose it into a binary** — a binary serves a service because `NewComponents` constructs it and the serve path mounts it. Add the constructor call to the composition (see below); selection is code, not a string table.

## Deploying it: declared once, bound per env

`forge scaffold operator|binary` appends the workload to
`deploy/kcl/workloads.k` — build, the component's subcommand in `args`. A
service or worker on the composition graph gets NO workload of its own: the
binary has no command per service or worker (see "Commands are roots" below),
so a stanza with `args = ["<service>"]` would crash-loop on "unknown command",
and `forge lint` warns about one (`component-drift/no-such-command`). Every env — dev (a host process under air, hot
reload) and staging/prod (hosted on Reliant as scaffolded) — runs every
service and worker in ONE workload, `_api`: the binary's `server`, every
service on one Connect mux, so the browser reaches them all at one origin. A
new one needs no binding line (`_api` is bound if it was not). An operator
binds `_on_k3d` in dev and `_on_cluster` in deployed envs, since hosting
refuses it; see `deploy/hosting`. Probes need nothing: a service
forge builds gets `/readyz` + `/healthz` on its `http` port on every runtime.
See the `deploy` skill for the runtimes and how to rebind one workload (to a
cluster you operate, say).

A project that runs its components in processes it composes itself — one
binary whose own subcommands (`public-api`, `admin-api`, `workers`) each mount
several, so most components of a kind have no workload of their own — gets
no workload for a new one: the scaffold prints which processes it found and
the stanza, and leaves the choice to you. Mount it in the subcommand that
should run it and add it to that workload's `serves`, or paste the stanza to
give it its own. `forge lint`'s component-drift reads the file the same way.

## Port Assignment

In a container every service owns `:8080` (the `http` port). In the dev loop
each host process gets its own port from `plugin.resolve_port` in
`deploy/kcl/dev/main.k`. Do not hard-code port numbers elsewhere.

## Rules

- **Always use `forge scaffold` or `forge package new`** — never copy-paste an existing service or package directory.
- **One service per proto package** — keep proto definitions focused on a single domain.
- **Run `forge generate` after any proto or contract change** — generated code must stay in sync.
- **Service names canonicalize** the same way worker names do: lowercase snake_case (hyphens → underscores, PascalCase boundaries split). `forge scaffold service admin-server` keeps `admin-server` as the `forge.yaml` `name:` display key, but the on-disk leaf, Go package decl, and `forge.yaml` `path:` leaf are all `admin_server` (`internal/handlers/admin_server/`, `package admin_server`, `path: internal/handlers/admin_server`). See the `workers` skill Naming section for the full rule and the migration gotcha; see `architecture` for the cross-ecosystem naming-conventions table.
- **Service code lives under `internal/handlers/<svc>/`** — `service.go` (Deps/New), the rpc methods that hold its logic, and the generated CRUD ops co-located in ONE directory, with no `contract.go`. The `handlers/` role subtree is under `internal/`, not top-level; a service is app-internal, imported by nobody external.

## Serving a service = composing it (the composition root)

**What a binary serves is the set of constructors it calls — not a string row in a registry.** The explicit composition is split across two files under `internal/app/`, **both yours**: `providers.go` (`Infra` + `OpenInfra`) and `compose.go` (`Components` + `NewComponents(infra *Infra) (*Components, error)`). A binary serves a service because `NewComponents` constructs it and the serve path mounts its handler:

```go
// internal/app/compose.go (yours; reconciled — newly discovered components and
// Deps keys are injected, your value expressions are kept)
func NewComponents(infra *Infra) (*Components, error) {
    c := &Components{}
    c.Users = user.New(user.Deps{Repo: infra.Repo})
    c.Bill  = billing.New(billing.Deps{Users: c.Users})  // dep by INTERFACE, in-process default
    // two-phase (bill.WithReliantAPIKeyIssuer(infra.LLM)) → just add it here; it is kept
    return c, nil
}
```

The serve path (`cmd/<bin>/cmd/serve.go`) then applies the typed `Mount<Svc>` method values onto a `serverkit.Server` and calls `srv.RequireMounted(...)`.

- **Per-service handler options are a value, passed explicitly.** Every service shares the serve path's interceptor chain. To give one service more (an extra interceptor, its own read limit, its own HTTP stack), build the generated `app.MountOptions` — one `[]mountkit.Option` field per service — in owned code and name one of its methods as the mount: `ServeSpec{Mount: app.MountOptions{Worktree: []mountkit.Option{mountkit.WithHandlerOptions(connect.WithInterceptors(wake))}}.MountAll, ...}` in `server.go`, or `.MountWorktree` in that service's subcommand. `mountkit.WithHandlerOptions` comes after the shared chain, so the shared interceptors wrap it.

- **Selection is code.** To stop serving a service, stop constructing it in `NewComponents` and stop mounting it. To serve it elsewhere, construct it in that binary's composition. No `forge.yaml` edit, no string match.
- **The `Deps` interface is the seam.** A consumer's field is `Users user.Service`, never the concrete type — it can't tell whether it got the in-process instance, a Connect client, or a mock. Splitting a service into its own Deployment later is a one-line swap in `NewComponents` (`billing.New(billing.Deps{Users: userclient.New(conn)})`) with the consumer untouched.
- **Per-binary singletons are natural.** A dep that must be one instance within each process (e.g. `enforcement` in both the server and the workspace-proxy) is one field on the owned `Infra`, opened once in `OpenInfra` and shared across the components.
- **A `// forge:unmanaged` service is not a component.** Its proto declares it but its handlers live outside `internal/handlers/` (in-place adoption — see `migration`), so forge emits no `Mount<Svc>`, compose entry, inventory row or subcommand for it. Mount its handler yourself, onto the same `srv.Mux` with the same handler options, and the generated open-procedure set still covers its public rpcs. `RequireComplete` checks exactly `app.Inventory`, so it neither demands nor blocks on an unmanaged service. Deleting the marker adopts it: the next `forge generate` scaffolds it like a new service.
- **`serverkit.Run` takes a composed server with typed mounts, not service-name strings.** It runs handlers + workers + operators + lifecycle; *which* of each is decided by what `NewComponents` constructed and the serve path mounted. There is no `args`/`names` variadic, no name-matched wiring.
- **The interceptor chain and the listener are components.** `serve.go` builds the chain with `serverkit.NewChain(serverkit.ChainDeps{Auth: …})` (every slot defaulted except `Auth`, which is required) and hands it to `serverkit.Server.Chain`; `serverkit.Run` serves one `serverkit.Listener` over it. A process that serves a second kind of caller — daemons, operators — builds a second chain and a second `serverkit.NewListener(ListenerDeps{Config, Chain, Routes})` on its own address, and a listener refuses to start when a mounted service's `(forge.v1.service).audience` differs from its chain's. Depth: `auth` (audiences), `services/shutdown` (running listeners without `Run`).
- **Shutdown is Run's, and it drains.** Readiness flips, the pre-stop pause runs, then unary requests finish while long-lived streams (SSE, websockets, Connect streams) are ended inside the budget. Mark a plain-HTTP streaming route with `serverkit.LongLived(h)`. Depth: `forge skill load services/shutdown`.

## Commands are roots, not one per service

A project on the composition graph has no per-service, per-worker or
per-operator subcommand. A process is a **root** — a `//forge:root` struct in
`internal/app` whose fields select the subgraph it builds — and every root gets
one generated cobra command in `cmd/<bin>/cmd/roots_gen.go`, which runs it
through `runRoot`, the owned process policy in `serve.go`. With no root
declared the one command is `server`, running every service, worker and
operator. To run a service alone, declare a root that holds only it; the
`architecture` skill ("Roots and commands") has the worked example. Custom
NON-root subcommands go in the user-owned `cmd/<bin>/cmd/commands.go`
(`userCommands(deps)`) — see the `binaries` skill.

A project still on the legacy `Infra` / `NewComponents` composition keeps its
generated `cmd/<bin>/cmd/services/<name>.go` per-service commands
(`cmd.Serve(c.Context(), deps, cmd.ServeSpec{Mount: (*app.Components).MountBilling})`)
until it migrates.

## Inventory (introspection only)

A data-only inventory (`{Name, ConnectPath, Mount}`) survives for `forge project map`/`forge project audit` and CLI listing. Names there are for **display only** — never a lookup key for construction. `forge project audit` flags orphan stubs (a generated `Unimplemented` service nobody composes) and `forge lint` flags unresolved non-optional Deps and narrow-interface silent drops.

## When This Skill Is Not Enough

- **Simple utility packages** — create a directory under `internal/` and write plain Go. No scaffold needed. (`pkg/` is reserved for code with real external importers.)
- **CLI-only projects** — use `forge project new <name> --mod <module>` without `--service` to create a Cobra CLI binary with no server bootstrap.
- **One-off scripts or CLI tools within existing projects** — add a subcommand under `cmd/` (same image, opt-in config/OTel via the cmdkit paved path), or `forge scaffold binary <name>` if it needs its own deploy lifecycle. A parallel `cmd/<name>/main.go` outside the cobra root is invisible to forge build/deploy — avoid it.
