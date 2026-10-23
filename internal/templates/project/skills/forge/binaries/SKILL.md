---
name: binaries
description: Non-server long-running binaries — when to use `forge scaffold binary` vs `forge scaffold worker` vs `forge scaffold service`, plus the lifecycle and deploy story.
---

# Binaries

A "binary" is a non-server long-running process that ships its own Deployment. Examples: a reverse proxy, a workspace gateway, an off-service NATS consumer, an authentication sidecar. Forge scaffolds binaries via `forge scaffold binary <name>`.

A binary is its own thin `package main` cobra root under `cmd/<package>/main.go` (devspace idiom — each binary gets its own `cmd/<bin>/` tree), delegating to a small owned composition root in `internal/<package>/`. It does NOT share the server's `internal/app/compose.go` `NewComponents` — that bag constructs the server's handlers/workers; a standalone binary owns its own narrow `Deps`/`Service`/`New` contract. The binary owns *which* runtime loop it composes; it reuses the same typed `internal/config` flag set as the server.

Binaries sit alongside services, workers, and operators as the four long-running shapes forge knows how to scaffold. The decision tree below shows when to pick which.

## When to use a binary vs a worker vs a service

| Shape | What it is | Picks this when |
|---|---|---|
| **service** | Connect-RPC server mounted via its `MountX` method onto the server's `serverkit.Server`. | You're exposing typed RPCs to clients (frontend, other services, daemons). |
| **worker** | In-process goroutine supervised by serverkit. Constructed in the same `internal/app/compose.go` `NewComponents` as services; `WorkerList(components)` adapts it and the cmd serve path starts it when the server boots. | You need background work that *belongs to* the server's lifecycle (queue consumer, periodic sweep, cron). One process, no separate Deployment. |
| **binary** | Standalone process with its **own** `cmd/<bin>/main.go` cobra root and its own owned `Deps`/`Service`/`New` composition root in `internal/<package>/`, with its own Deployment. | You need a process with its own deploy lifecycle: a reverse proxy, a workspace gateway, a webhook receiver isolated from the API server. Also covers **one-off operational binaries** — backfills, data migrations, ad-hoc scripts. |
| **operator** | Kubernetes controller-runtime manager with CRD reconcilers. | You're reconciling Kubernetes resources (CRDs). Operators are a specialised flavour of binary; pick this only when CRDs are involved. |

The litmus test for binary vs worker is: **does this need its own Deployment, OR is it a run-once operational tool?** If either — it's a binary. If neither — it's a worker.

Resist the temptation to hand-roll a `cmd/<name>/main.go` from scratch. `forge scaffold binary` scaffolds the `cmd/<package>/main.go` entry point AND the `internal/<package>/` composition root for you, registers it in `forge.yaml` under `binaries:`, and gives it an image tag — so it stays visible to `forge generate`/`build`/`deploy`. A from-scratch `main()` that doesn't go through `forge scaffold binary` re-implements config/flag loading and shutdown plumbing the scaffold already solves, and is invisible to deploy.

## The cmd/ surface

The server binary's cobra surface is real, owned Go — one symbol per command you can jump to — not a string-projection of a registry.

- **`cmd/<server>/main.go`** (yours) — the server cobra root with the full command tree.
- The server's `serve` command applies a typed `(*app.Components).Mount<Svc>` method expression (or `MountAll`) onto a `serverkit.Server`; named subsets compose the typed `app.MountByName` function values.
- **`cmd/<binary>/main.go`** (yours, from `forge scaffold binary`) — a separate thin `package main` cobra root for each standalone binary, NOT a subcommand under the server root. It loads config, constructs the binary's `Service` via `internal/<package>.New(Deps{...})`, and calls `svc.Run(ctx)`.

There is **no** `cmd/services_gen.go` string-projection, **no** `RegisteredServices` constructor table, and **no** `userCommands()` catch-all. A data-only `Inventory` registry survives for introspection (`forge project map`/`audit`, CLI listing) — names there are for display, never a construction lookup key.

## Adding a binary

```bash
forge scaffold binary workspace-proxy
forge scaffold binary auth-sidecar
```

This creates:

```
cmd/<package>/main.go                # thin package main cobra root — load config, New(Deps), svc.Run(ctx)
internal/<package>/contract.go       # Deps + Service interface + New(Deps) (Service, error) + validateDeps
internal/<package>/<package>.go      # Runner struct + Run(ctx) runtime body
internal/<package>/<package>_test.go # construction + lifecycle tests
```

Nothing is written to `forge.yaml` — it is global-only. The binary is
discovered from its `cmd/<package>/main.go`, and that discovery is what makes
deploy emit a Deployment for it.

The hyphenated CLI name (e.g. `workspace-proxy`) becomes the Go package name with hyphens replaced by underscores (`workspace_proxy`), so `cmd/workspace_proxy/main.go` and `internal/workspace_proxy/` line up with `package workspace_proxy`.

## Binary composition root

A binary owns its **own small typed composition root** in `internal/<package>/contract.go`: a `Deps` struct of interface-typed fields **resolved by type, never by string name**, gated by `validateDeps()` at construction, and a `New(Deps) (Service, error)` constructor. The thin `cmd/<package>/main.go` builds the `Deps` (logger, typed config, plus any DB/NATS/k8s/HTTP dependencies) and hands them to `New`. This is the binary's analogue of the server's `internal/app/compose.go` `NewComponents` — but local to the binary, not the server's `Components` bag.

```go
// internal/workspace_proxy/contract.go
type Deps struct {
    Logger *slog.Logger
    Config *config.Config
    // Add binary-specific deps below, each as a narrow interface:
    Enforcement Enforcer    // interface seam: real in-proc here, a Connect client if split out later
    Users       UserSource  // a local interface, not the concrete type
}

func New(deps Deps) (Service, error) {
    if err := deps.validateDeps(); err != nil {
        return nil, err
    }
    return &Runner{deps: deps}, nil
}
```

Notes:
- **In-process is the default.** A cross-binary dep is the one-line swap: fill its interface with a Connect client instead of the in-proc instance; the consumer is untouched.
- **Per-binary singletons are natural.** A dep that must be one instance within *each* process is constructed once in `cmd/<package>/main.go` and passed into `Deps`.
- **Two-phase wiring is first-class.** Construct, then inject via setters (`x.WithY(z)`) for near-diamonds — plain method calls after both ends exist, in `main.go`.
- **Scalars are config, not dependencies.** A `string`/`int`/`bool`/`Duration` lives in a `<Component>Config` proto block consumed as one typed `Cfg *config.<Component>Config` Deps field — never a naked scalar Deps field.

## Lifecycle — Run blocks until ctx is cancelled

The generated `cmd/<package>/main.go` wires `SIGINT`/`SIGTERM` into a context via `signal.NotifyContext`, constructs the binary's `Service` via `New(Deps{...})`, and calls `svc.Run(ctx)`. The binary's `Runner.Run(ctx)` blocks until ctx is cancelled, then returns cleanly. Graceful shutdown is the binary's own responsibility *inside* `Run` (close servers, drain consumers) — the cobra adapter just propagates cancellation.

```go
// internal/<package>/contract.go
type Service interface {
    Run(ctx context.Context) error // blocks until ctx cancel or fatal error
    Name() string
}
```

For a binary that is purely an HTTP server (proxy, gateway), `Run` starts `srv.ListenAndServe()` in a goroutine and calls `srv.Shutdown` on `<-ctx.Done()`. For a loop (NATS consumer, ticker), `Run` selects on `ctx.Done()` directly. The scaffold's placeholder `Run` body blocks on `<-ctx.Done()` so a freshly added binary compiles and exits cleanly on Ctrl-C.

### Interceptor ordering

If your binary mounts Connect handlers, the interceptor chain (otel-outermost → rate-limit → auth → audit) is composed **explicitly** in the handler assembly inside `Run` — never implied by registration order. Preserve it.

### One-off scripts (backfills, migrations, ad-hoc tools)

A one-off binary's work-loop is allowed to finish: do the work, log the summary, return `nil`. Declare its workload `kind = "job"` instead of `service`/`worker` in `deploy/kcl/workloads.k`, so no runtime restarts it. Same scaffold, same composition root; the only difference is the `kind`.

## Config — the cmdkit paved path

Binaries do **not** hand-roll `os.Getenv`, ad-hoc `slog.Logger`s, hardcoded timeouts, or hand-rolled shutdown. The single typed `internal/config` — generated from the proto config blocks (`AppConfig` + `<Component>Config` with `(forge.v1.config)` annotations) — serves server, CLI, and standalone-binary kinds alike via the **cmdkit paved path**: DB open, logger, flag/env binding, and report envelope, with the logger injected through `observe.WithLogger`/`FromContext`.

Per-env config (logging, env vars) lives directly in `deploy/kcl/<env>/` — not in a redundant second YAML. `forge.yaml` stays strictly top-level (project identity, features, deploy provider, the `binaries:` list).

### How one field resolves

Later layers win: the proto `default_value`, then the config file (`--config`), then the field's `env_var`, then its flag when given on this invocation. A malformed value is a startup error, never a silent fallback to the default.

**An env var set to `""` is unset, for every field type.** `TEMPORAL_HOST=` keeps the file's or the default's value; it does not load `""`. A deploy template rendered from a missing key, compose's `X: ${X}` and `export X=` all mean "not configured". An explicitly empty value comes from a flag (`--pprof-addr=` switches pprof off; `--allowed-registries=` clears a defaulted list). A required field whose variable is set but empty fails with an error that says so.

**Aliases replace a hand-written fallback chain.** When a value has setters you do not control — another tool's convention, or an old name a deployment elsewhere still exports — list the extra names instead of keeping `if v := os.Getenv(A); v != "" { … } else if …`:

```protobuf
string url = 1 [(forge.v1.config) = {
  env_var: "RELIANT_CONTROL_PLANE_URL"
  env_var_aliases: [{name: "CONTROL_PLANE_API_URL"}, {name: "CONTROL_PLANE_BASE_URL", deprecated: true}]
}];
string environment = 2 [(forge.v1.config) = {env_var: "RELIANT_ENV", env_var_aliases: [{name: "NODE_ENV"}]}];
```

- The loader reads `env_var`, then each alias in order; the first one set (non-empty) wins. All of them sit in the env layer: below the flag, above the file and the default. A malformed value in the winning name is an error; it never falls through to the next name.
- `deprecated: true` prints one stderr line at load whenever that alias is set, naming `env_var`. Leave it off for a permanent alias like `NODE_ENV`, which is read silently.
- forge's own setters use `env_var` alone. The KCL env map, the secret store key and `--help` name it (help also lists permanent aliases as `[env: RELIANT_ENV, NODE_ENV]`), so every manifest forge renders converges on one name.
- `generate` refuses an alias that repeats a name another field of the same process reads, an alias without an `env_var`, and aliases on a frontend config field.
- The typed-config guardrail flags `os.Getenv` whatever the name, so a raw read of an alias is a finding exactly like a read of the primary.

### Per-binary config (no project-global AppConfig)

A project can move config OWNERSHIP off a single shared `AppConfig` onto each binary's own message — annotate a config message with `option (forge.v1.binary_config) = {binary: "<cmd/name>"};` (see `forge skill load db` / the config proto's own header for the full mechanics). Doing this for EVERY config message — deleting `AppConfig` entirely — is a legitimate end state, not a half-migration: forge still emits a complete Go/KCL/env surface per binary.

This does not change how a `forge scaffold binary` composition root or a `forge scaffold service`/`worker` Deps struct is written. `config.Config` keeps meaning exactly what it always meant — "this component's config type" — regardless of whether the project has a root `AppConfig` or has moved entirely to per-binary configs:

- **With a root `AppConfig`** (the common case): `config.Config` aliases it directly.
- **All-binary-annotated, no root `AppConfig`**: `config.Config` aliases the config message bound to the project's PRIMARY binary — the one with the full `cmd/<bin>/cmd` command tree (`server`, `db migrate`, etc.), i.e. the binary a fresh `forge project new` scaffolds. Every service, worker, and internal package mounted into THAT binary's composition graph (`internal/app/compose.go`) keeps reading `Config *config.Config` unchanged.
- **A secondary binary** (`forge scaffold binary <name>`) that wants its OWN config type, distinct from the primary binary's, reads it through the per-binary alias forge always generates alongside `Config` — `config.<MessageName>` / `config.Load<MessageName>(cmd)` — and takes that type on `Deps.Config` instead of `*config.Config`. A freshly scaffolded secondary binary's `Deps.Config *config.Config` compiles either way (it is reading the primary binary's config, same as any other component) until you point it at its own message.

The primary binary MUST have a ROOT config message (no `command`) bound to it once the project goes all-binary — `forge generate` refuses, naming the primary binary, if none is. There is nothing else for `config.Config` to mean once the root `AppConfig` is gone.

`binary` must name a `cmd/<name>` directory, and one message per binary: `forge generate` refuses anything else, with the `proto/<file>:<line>` of each offending annotation. A binary's root config registers EVERY leaf (composed blocks included) as a persistent flag: `config.Register<Msg>Flags(root)` on the binary's root command, `config.Load<Msg>(cmd)` from any of its commands.

### One binary, several processes: `command`

When a binary's subcommands run as SEPARATE PROCESSES (`app server api`, `app server worker`, `app daemon`) — one image, one Deployment per role — scope a config to a subcommand instead of splitting the binary:

```protobuf
message AppConfig {                       // the binary-wide surface: every process
  option (forge.v1.binary_config) = {binary: "app"};
  LogConfig log = 1;
}
message ApiServerConfig {                 // only the `app server api` process
  option (forge.v1.binary_config) = {binary: "app", command: ["server", "api"]};
  int32 grpc_port = 1 [(forge.v1.config) = {env_var: "GRPC_PORT", flag: "grpc-port", default_value: "9090"}];
  string auth_api_key = 2 [(forge.v1.config) = {env_var: "AUTH_API_KEY", sensitive: true}];
}
```

The binding is keyed on `(binary, command)`. A process loads its binary's root config, every SUBTREE config over its path (below), and the ONE config declared for its exact command path:

```go
config.RegisterAppConfigFlags(root)            // persistent: every subcommand inherits
config.RegisterApiServerConfigFlags(apiCmd)    // local: `app server api --help` lists exactly these
// in apiCmd's RunE:
base, err := config.LoadAppConfig(cmd)
api, err := config.LoadApiServerConfig(cmd)    // refuses any command but `server api`
```

- `generate` checks each `command` element is one command name. It cannot check the path EXISTS — a cobra tree is built at runtime — so `Load<Msg>` checks it exactly against the real tree and errors, naming both paths, when called from another command.
- A root config and a command config of one binary may not declare the same env var or flag (one process would read it twice), nor the same top-level field name (one config file serves the process: `binaries/config-files`); `generate` refuses the pair. Compose a shared block onto the root config, not onto each command config, when EVERY process reads it; a subtree config (below) when several commands do.
- KCL: every config keeps its own `<msg>EnvMap`, and a binary with command configs also gets `config_gen.<bin>ProcessEnvMap(cfgs, argv, config_secrets)`, which projects one workload's env from its own `command + args` — root config plus the matching command config, longest declared path first, `config_secrets` routed to whichever config declares each name. config.k scaffolds the bundle as `<bin>_process`:

  ```kcl
  env = config_gen.appProcessEnvMap(appcfg.app_process, (w.command or []) + (w.args or []), w.config_secrets) | w.env
  ```

  The subcommand path is read after the argv token naming the binary (`app`, `./cmd/app`, `/app/app`), or from all of argv when none does (`args = ["server", "api"]` on the project image), and ends at the first flag.

### Several commands, not every process: `subtrees`

A setting many commands read but the server roles must not carry (the CLI's server URL and token; a guard policy `daemon start` and `open` share) is a SUBTREE config, `subtrees: [{path: ["daemon"]}, {path: ["open"]}]`: loaded at or below each path, by no other process. Rules, registration and the KCL projection: `binaries/subtrees`.

### Config files

Every config also loads from a YAML or JSON file: `--config <path>`, or the binary's own `<BINARY>_CONFIG` (`RELIANT_CONFIG` for `cmd/reliant`; the project-global `AppConfig` reads `<PROJECT>_CONFIG`). Precedence is defaults < file < env < flags. One file serves one process, flat across its root and command config. The file's shape, the variable's override and its failure modes: `binaries/config-files`.

Splitting into separate binaries instead (`cmd/app-server` + `cmd/app`) is right when the roles share nothing. When they share a binary — one image, a monolith mode that runs every role in one process, a CLI on the same root — `command` keeps one binary and still gives each process only its own config and credentials.

## Errors

If your binary mounts handlers, every handler maps errors with `svcerr.Wrap(err)` from `forge/pkg/svcerr` — never a raw `connect.NewError(connect.CodeInternal, err)` (it flattens NotFound/InvalidArgument/PermissionDenied) and never a hand-rolled per-binary `mapServiceError`/`toConnectError`. Domain failures use `svcerr` sentinels (`svcerr.NotFound("workspace")`). Skip per-RPC `if deps.X == nil` checks for non-optional deps — `validateDeps()` gates them at construction.

## Deploy

A secondary binary is its own program: `forge scaffold binary <name>`
declares it in `deploy/kcl/workloads.k` with its OWN build, as a `tool`
(built into the project image, never scheduled) and binds it
`_build_only(wl.<name>)` in every env:

```kcl
# deploy/kcl/workloads.k
workspace_proxy = fw.Workload {
    name = "workspace-proxy"
    kind = "tool"
    build = forge.GoBuild {cmd = "./cmd/workspace_proxy", output_name = "workspace_proxy"}
}
```

To RUN it as a long-lived process, make it a `service` (or `worker`) and
name its binary as the `command` — the project image's ENTRYPOINT is the
primary binary, and a workload's `command` replaces it:

```kcl
workspace_proxy = fw.Workload {
    name = "workspace-proxy"
    kind = "service"
    build = forge.GoBuild {cmd = "./cmd/workspace_proxy", output_name = "workspace_proxy"}
    command = ["/app/workspace_proxy"]
    ports = [fw.Port {name = "http", port = 8080, expose = True}]
}
```

then bind it per env like any workload — `_on_cluster(wl.workspace_proxy)`,
`_on_host(wl.workspace_proxy)` (the host runtime runs `command` verbatim), or
`_hosted(...)`. Replicas, resources and env are refinements on the binding
(`_on_cluster(wl.workspace_proxy) | {replicas = 2}`), never a second copy of
the declaration.

A subcommand of the PRIMARY binary needs no build of its own: `args =
["<subcommand>"]` with the project's GoBuild selects it on every runtime.

## Common patterns

A binary's body is `Runner.Run(ctx)` — the placeholder blocks on `<-ctx.Done()`; you replace it with one of these shapes. `Run` owns its own listen/shutdown or loop, propagating cancellation from the ctx the cobra adapter wires SIGINT/SIGTERM into.

### Reverse proxy / gateway

`Run` starts the proxy `http.Server` in a goroutine and calls `srv.Shutdown` on `<-ctx.Done()`. You write the handler and the listen/shutdown dance against the cancelled ctx.

### Off-service NATS consumer

`Run` subscribes, then blocks until ctx cancellation, unsubscribing on the way out:

```go
func (r *Runner) Run(ctx context.Context) error {
    sub, err := r.deps.NATS.Subscribe("events.>", r.handle)
    if err != nil { return err }
    defer sub.Unsubscribe()
    <-ctx.Done()
    return nil
}
```

### Periodic ticker

`Run` selects on a `time.Ticker` and `ctx.Done()` — a plain loop in the binary's own `Run`, no external supervisor.

## Testing

`internal/<binary>/<binary>_test.go` ships:

1. **Construction test** — calls `New(Deps{...})` with every dep mocked through its generated `mock_gen.go` mock, and asserts `validateDeps` accepts a complete `Deps` and rejects missing required fields. Because every dep is an interface filled in one place, "spin up this binary with X mocked" is a few-line, one-call operation against `New`.
2. **Lifecycle test** — start `Run(ctx)` in a goroutine, cancel ctx, assert it returns cleanly; keep it so shutdown regressions stay caught.

Binaries that don't mount RPCs don't use `tdd.RunRPCCases`; ones that do mount handlers test those the same way services do.

## Lint

`forge lint` / `forge project map` check that:

- Every `binaries:` entry has a matching `cmd/<package>/main.go` and `internal/<package>/contract.go`.
- The `Path` in forge.yaml matches the scaffolded `cmd/<package>/main.go`.
- Names don't collide with reserved cobra subcommands (`server`, `version`, `db`).
- The composition graph is acyclic and has no narrow-interface silent drops or unresolved non-optional deps.

Run `forge generate` after editing `binaries:` to keep the scaffold and config in lockstep.

## See also

- `forge skill load services` — Connect-RPC service shape, `MountX`, when to pick this instead.
- `forge skill load workers` — in-process workers supervised by serverkit.
- `forge skill load deploy` — KCL deploy story, including how binaries become Deployments.
- `forge skill load architecture` — the server's explicit composition (`internal/app/providers.go` `Infra`/`OpenInfra` + `internal/app/compose.go` `NewComponents`), serverkit lifecycle, the interface seam — the wiring the server participates in; a standalone binary owns its own narrow `Deps`/`Service`/`New` analogue.
