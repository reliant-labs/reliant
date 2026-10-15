# P3c — composing reliant's handlers through forge: options

Status: research, 2026-10-15. Pinned forge `fed5bd9f`. Paths relative to the reliant root.
Forge facts come from `forge skill load services|architecture` and
`forge project libraries serverkit mountkit`. None come from forge's source.

## TL;DR

- **One process serves Connect today: `reliant server api`.** `internal/grpc.NewServer`
  is called only from `internal/serverapi/run.go:413`. The gateway serves a separate,
  untrusted `DaemonServer` (`internal/grpc/daemon_server.go:53`, called from
  `internal/servergateway/run.go:361`). The worker serves no Connect at all. There is
  **no all-in-one server path**: `cmd/reliant/commands/server.go:21-31` has only
  `api|worker|gateway`.
- forge's root is `internal/app` (`providers.go` Infra/OpenInfra, `compose.go`
  NewComponents, generated `mounts_services_gen.go`, `lifecycle.go`). serverkit then
  runs it (`serverkit.Run(ctx, cfg, Server)`). reliant has **no `internal/app` yet**.
- **Recommendation: Option B in three PRs.** Adopt `internal/app` + generated mounts +
  `serverkit.Server` for the API role. Keep `serverapi`, `serverworker` and
  `servergateway` as reliant-owned role entrypoints that feed it. File the four forge
  gaps (G1–G4) first: those gaps decide B's size, and they are needed under any option.
  Leave the gateway and worker for P7.

## 1. What reliant does today

### Service construction (`internal/grpc/server.go`, 1452 lines)
- `NewServer(cfg *Config)` at :195 builds all 35 `internal/handlers/<svc>` services with
  `<svc>.New(<svc>.Deps{...})`, from :244 (system) through :848 (connection).
- **Conditional services** are mounted only when a collaborator exists:
  - `router != nil` (NATS daemon router): package_commands :426, forge :602
    (also needs `!forgeAPIDisabled()`), file_system, background and terminal plus the
    terminal websocket :688-745.
  - `*db.Repo` with a raw `*sql.DB` present: account :758, connector :787.
  - `cfg.Connections != nil` → connection :847.
  - `cfg.ControlPlane.Hosted()` changes Deps: :297, :499 (git tokens),
    :518 (`ClipsGrants`), :652.
  - `cfg.TriggerInbound` and `cfg.PauseService` are optional hooks (:360, :636).
- **Cross-service wiring**:
  - `worktreeSweeper` is built at :315 and shared by worktree and trigger (:662).
  - `chatService` and `projectService` are exported back to serverapi through accessors
    (:1030-1047), so the api role can feed JetStream consumers and sweeper loops
    (`serverapi/run.go:486`).

### Shared collaborators
These are built in `internal/serverapi/run.go` and passed in through `grpcserver.Config`
(:413-447):
- the repo (`*db.Repo`);
- the Temporal client (:240) plus `AgentMessageNotifier` and `SpawnStopper` (:258-261);
- one NATS connection (:286), shared by the user/chat update hubs (:298-299) and the
  NATS daemon router (:368);
- the claim-check payload store and its GC goroutine (:234-238);
- the reconciler (:329) and the observability provider (:170);
- typed config, taken from `cmd/reliant/commands/server.go:131-170`
  (`loadServer*Config`).

### Interceptors, in order
- `newHandlerOptions` → `newInterceptors` (`server.go:1136-1200`) uses
  `observe.DefaultMiddlewares` with these Extras:
  1. otelconnect, prepended, with `WithTrustRemote` only on the public server (:1171-1178);
  2. Recovery → RequestID → Logging (with the `errclass` classifier and
     `drain.ErrServerDraining` as expected) → Metrics;
  3. the Extras themselves: SlowRPCWatchdog → Timeout(`forge.ForgeRPCDeadlines()`, :222)
     → Auth (:202, plus the access-token introspector :216) → DomainWhitelist (:217).
- Per-service options on top of that:
  - `StreamDrainInterceptor(streams)` is appended for every service (:227).
  - worktree and file_system get an extra `machinewake.NewInterceptor()`
    (:554-555, :702) from a cloned opts slice.
- The order matters. The trust-remote flag differs per server. The daemon server uses the
  same builder with `trust=false` plus a `DaemonAuthInterceptor`
  (`daemon_server.go:65-71`).

### Non-Connect HTTP routes on the same mux
- attachment content (:832, uses `authInterceptor.AuthenticateHTTP`)
- the terminal websocket `/api/v2/terminal/ws` (:740, bound to the drain)
- `cfg.OAuthRoutes.Register` (:860) and `cfg.TriggerInbound.Register` for integration
  webhooks (:866)
- the connector MCP endpoints (`mountConnectorMCP` :1239-1328, four paths plus root,
  event streams bound to the drain at :1206)
- RFC 8414 metadata (:900)
- JSON `/health` (:906)
- Edge layers: CORS with web-client-tested header lists (:960-1027), then
  `securityHeaders` (:1417).

### Lifecycle
- `Start` (:1052) handles TLS with explicit `http2.ConfigureServer`
  (`MaxConcurrentStreams` 1000) or h2c through `http.Protocols` (:935-944, :1409).
  `ReadTimeout` and `WriteTimeout` stay 0 for long streams (:946-955).
- `Stop` (:1124) runs Shutdown, and `RegisterOnShutdown(streams.End)` ends the
  long-lived streams first (:957).
- The roles drain with reliant's `internal/drain`: flip readiness, run the shutdown
  context, then the ordered stops (`serverworker/run.go:538-564`,
  `servergateway/run.go:454-484`). `drain.go:13` says the order mirrors serverkit's.
- Health and readiness use a separate health server with named `drain.ReadinessCheck`s
  (nats, nats-streaming, temporal).

### Roles
| role | entry | Connect? | owns |
|---|---|---|---|
| api | `serverapi.Run` (673 lines) | 35 services via `grpc.NewServer` | Temporal client, NATS hubs, router, payload GC, trigger schedule converge, sweeper |
| worker | `serverworker.Run` (586) | none | Temporal worker (`workersetup`), workflowevent relay :463, health :472 |
| gateway | `servergateway.Run` (523) | `DaemonServer` (ToolsDaemonService bidi, untrusted) | NATS tool bridge, activity subscriber, flowhealth, managed reconciler |

## 2. What forge's composition root provides and expects

- **`internal/app/providers.go`** (owned) holds `Infra` and `OpenInfra(ctx, cfg, logger)`:
  pools, clients, adapters. It is one function per binary.
- **`compose.go`** (owned, reconciled) holds
  `NewComponents(infra) (*Components, error)`. forge injects newly discovered components
  and Deps *keys*, and never touches value expressions. Two-phase setters are allowed.
- **`mounts_services_gen.go`** (generated) holds the typed `(*Components).Mount<Svc>` and
  `MountAll`, plus a data-only `Inventory`. `RequireComplete(Inventory)` fails boot if a
  service is not mounted.
- **`lifecycle.go`** (owned, reconciled) supervises workers and operators through
  `appkit`/`lifecyclekit`.
- **Serve path**: `cmd/<bin>/cmd/serve.go` and the generated per-service subcommands
  `cmd/<bin>/cmd/services/<name>.go` call `cmd.Serve(ctx, deps, ServeSpec{Mount: ...})`,
  and `server.go` mounts `MountAll`. Then `serverkit.Run`.
- **`serverkit.Server`** exposes `HandlerOpts []connect.HandlerOption` (one global
  chain), `Mux` (extra routes go straight onto it), `HTTPMiddleware`, CORS / security
  headers / request-id factories, `Workers`, `Operators`, `ReadyChecks`, `FlowChecks`,
  `OnShutdown`, and `Mount(path, svc, mountkit.Option...)`.
- **mountkit** has the `HTTPRegistrar` and `WebhookRegistrar` interfaces, so a component
  can register its own non-Connect routes, and `WithHTTPStack` for per-mount HTTP
  wrapping.
- **serverkit.Config** covers TLS paths, CORS, PreStop and shutdown timeouts, readiness,
  write-stall and pprof. serverkit owns the listener, probes, the drain sequence and h2c.

### Where forge fights reliant
- **G1: per-service interceptor options.** `Server.HandlerOpts` is one chain. reliant
  adds `machinewake` to worktree and file_system only. `mountkit.Option` exposes only
  `WithHTTPStack` (HTTP-level). It has no per-mount `connect.HandlerOption` option.
  Needed: `mountkit.WithHandlerOptions(...)`, or the generated
  `Mount<Svc>(srv, opts...)` forwarding it.
- **G2: conditional components.** reliant omits 7–8 services by collaborator presence
  (router, raw SQL, connections, forge-API flag). Two forge contracts collide with this:
  - `MountAll` plus `RequireComplete(Inventory)` assumes every inventoried service is
    mounted. `RequireMounted` fails on a declared-but-absent service.
  - forge has no first-class "optional component".
  Needed: a component that is legitimately absent in this binary (for example a
  `Mount<Svc>` that no-ops on a nil component, recorded as "declined: <reason>" in
  `RequireComplete`). Alternatively, reliant always constructs the services and returns
  `Unavailable` when the collaborator is missing. That is a product change (today the
  client sees "not mounted").
- **G3: owned interceptor chain, not forge's default.** reliant's chain differs from
  forge's default in four ways:
  - otelconnect instead of forge tracing;
  - a custom error classifier;
  - `ErrServerDraining` treated as expected;
  - trust-remote toggled per server.

  `HandlerOpts` is assignable, so reliant can keep `newInterceptors` and assign it. That
  works today. The gap is that forge's serve path builds its own chain plus
  `pkg/middleware` auth. That chain must be replaceable wholesale without forking
  `serve.go`. Verify on a scaffold that `pkg/middleware` lets you supply the auth
  interceptor; reliant's is not forge's `authn`.
- **G4: multiple role binaries with different Infra.** forge has one `OpenInfra` and
  one `NewComponents`, and a subcommand chooses mounts only, not infra. reliant's api
  role needs Temporal + NATS + DB. The gateway needs NATS + DB plus a *different, untrusted*
  Connect server. The worker needs Temporal, and no Connect server. forge's documented
  shapes are:
  - per-service subcommands that share one Infra;
  - `forge scaffold binary` (a separate cmd that gets its own lifecycle).

  Neither lets an api role skip infra it does not need or vary the chain per binary.
  Needed: per-role (per-ServeSpec) infra selection, or documented guidance that a role is
  a `scaffold binary` with its own owned providers.
- **G5: http.Server knobs.** serverkit sets these itself, so reliant's settings must map
  onto it:
  - TLS h2 `MaxConcurrentStreams=1000`;
  - `ReadTimeout` and `WriteTimeout` of 0;
  - `ReadHeaderTimeout` and `IdleTimeout` from `internal/transport`;
  - `RegisterOnShutdown(streams.End)` (long streams must end *before* Shutdown waits).

  `OnShutdown` and `WriteStallTimeout` exist, but whether `OnShutdown` runs before
  Shutdown's wait, and whether `MaxConcurrentStreams` is configurable, cannot be seen
  from the API. Verify with a small pilot, then fix it in forge if it is wrong.
- **G6: `//forge:not-a-component` packages.** About 11 contract packages
  (machinewake, accesstokenclient, ...) are excluded today, and audit lists them as
  `uncomposed_packages`. P3c turns them into Infra fields. The markers come off package
  by package.
- **Not a fight:**
  - extra routes go onto `srv.Mux`, or through `HTTPRegistrar` on a component
    (attachment, terminal ws, MCP, oauth, webhooks);
  - JSON `/health` coexists with `/healthz` and `/readyz`;
  - the named ready checks map to `ReadyCheck`;
  - the workflowevent relay, payload GC and trigger converge map to `Worker`.

## 3. Options

### A — Full adoption: forge `internal/app` + `cmd.Serve` for every role; delete `server.go`, `serverapi/worker/gateway`
- **Changes:**
  - `providers.go` opens repo, NATS, Temporal, router, hubs and payload store.
  - `compose.go` builds the 35 services.
  - Generated per-service subcommands plus `server` replace `reliant server api`.
  - The worker becomes a `serverkit.Worker` (Temporal worker, relay).
  - The gateway becomes a separate `scaffold binary` or role.
  - `internal/drain` is deleted (serverkit drains).
- **Breaks or risks:**
  - The `reliant server api|worker|gateway` CLI and the control-plane KCL args change.
    That is a cross-repo deploy change.
  - Every drain test (`server_drain_test.go`, `drain_logging_test.go`) is re-proven
    against serverkit.
  - The gateway's bidi DaemonServer has no forge shape (an untrusted second listener).
  - It is the largest blast radius on the hottest path.
- **Forge gaps:** G1–G5, plus a gateway shape (a second Connect listener with its own
  chain inside one binary, or a binary whose Infra lacks Temporal).
- **Effort:** about 8–10 PRs.
  1. forge gaps.
  2. `internal/app` skeleton.
  3. API role on serverkit.
  4. CORS and edge.
  5. Routes as registrars.
  6. Worker role.
  7. Gateway role.
  8. KCL/CLI cutover in control-plane.
  9. Delete `drain`, `server.go` and the role packages.
- **Buys:**
  - everything: generated mounts, `RequireComplete`, forge probes and drain, and
    per-service subcommands;
  - `forge env up/deploy` workload declarations become native;
  - removes about 2,700 hand lines.
  - Overlaps P7 (observability/drain) almost entirely.

### B — Hybrid: forge composes the API handler set; reliant keeps its role entrypoints (recommended)
- **Changes:**
  - Create `internal/app/{providers.go,compose.go,lifecycle.go}` with Infra fields for
    *what serverapi already built*. serverapi stays the place that dials NATS and
    Temporal. It builds `Infra{}` and calls `app.NewComponents`.
  - Move the 35 `New(...)` calls from `server.go:244-848` into `compose.go`. Conditional
    services stay plain `if` code there.
  - `server.go` shrinks to edge plus listener. A later PR replaces it with a
    `serverkit.Server`:
    - `HandlerOpts = newInterceptors(...)`;
    - generated `Mount<Svc>` with per-mount machinewake (G1);
    - extra routes on `srv.Mux`;
    - reliant's CORS factory;
    - `OnShutdown = streams.End`.
  - `serverworker` and `servergateway` are untouched, apart from consuming shared Infra
    helpers.
  - The CLI and the KCL do not change.
- **Breaks or risks:**
  - Moderate: the serverkit swap changes listener, TLS and drain behaviour (G5).
  - Mitigate by landing compose first (no behaviour change) and serverkit second, each
    behind the existing e2e tests in `internal/grpc/*_e2e_test.go`.
  - The not-a-component marker removal churns about 11 packages.
- **Forge gaps:**
  - G1 and G2 are hard requirements for generated mounts.
  - G3 and G5 must be verified, and probably need small fixes.
  - G4 is not needed: serverapi is the role and passes Infra in.
- **Effort:** 4–5 PRs.
  1. forge G1+G2 (+ G5 if needed).
  2. reliant `internal/app` + compose.go and a `server.go` diet (pure move).
  3. Generated `Mount<Svc>` + `RequireComplete`, replacing the 40 `mux.Handle` lines
     :801-877.
  4. `serverkit.Server`/`Run` for the API listener; delete Start/Stop and the CORS
     plumbing that duplicates it.
  5. Drop `//forge:not-a-component` markers and `serverapi`'s P3c lint-disables
     (`serverapi/run.go:7-8`).
- **Buys:**
  - forge reconciles new services and Deps keys into compose (no hand edit when a
    service is added);
  - generated typed mounts and boot-time completeness;
  - serverkit probes, drain and write-stall on the main API;
  - about 900 fewer lines in server.go;
  - leaves the role split (a real product constraint: distributed, untrusted daemons)
    in reliant's hands.

  It does not buy per-role `forge env` workload generation. The KCL keeps naming
  `server api`.

### C — Defer: keep `server.go`, declare it the composition root
- **Changes:**
  - Mark `internal/grpc` as reliant's root (or `forge project disown` the app files) and
    keep the markers.
  - Optionally move the services to a reliant-owned `components.go` with no forge
    involvement.
- **Breaks:** nothing.
- **Forge gaps:** none needed. It does hide G1–G5 from forge, against the dogfooding
  rule.
- **Effort:** 1 PR (docs and markers).
- **Buys:** zero risk.
- **Costs:**
  - every new service stays a hand edit in two places;
  - forge's `RequireComplete` and `audit` cannot see mounts;
  - P7 has no root to hang observability on.
  - The PLAN's end state is abandoned.

### D — B for API now, A for worker/gateway in P7
Same as B. Additionally, P7 picks up G4 and the gateway shape, moving the worker and
gateway onto `serverkit.Worker` and a role binary when drain and observability move.
This is B with an explicit second phase. P7's drain work is the same code A would touch,
so it does not get done twice.

## 4. Recommendation

**B, sequenced as D.** Start with the forge gap PRs (G1 per-mount handler options, G2
optional/declined components) and pilot G5 on a scaffold, because the gaps are real
under every option except C.

**Devil's advocate.**
- *B keeps two roots: internal/app plus serverapi's dialing. Is that the half-migration
  the PLAN warns against?*
  Partly. The line is principled, though. Infra *construction* stays per role, because
  forge has one `OpenInfra` and reliant genuinely has three different infra sets (G4).
  Component *composition* and mounting go to forge. A, done now, would force G4 and a
  gateway shape into forge before the API role has proven the root. That is the riskier
  ordering.
- *Is the serverkit listener swap (B step 4) worth its risk at all?*
  It is the riskiest step. TLS h2 tuning, zero read/write timeouts and stream-end-before-
  Shutdown have each caused production incidents that are pinned by tests here. If the
  G5 pilot shows serverkit cannot express them cleanly, stop B after step 3. Generated
  mounts + compose is most of the value, and the listener can wait for P7.
- *G2 might be better solved by always mounting and returning Unavailable.*
  Simpler for forge, but it changes client-visible behaviour on desktop and self-hosted,
  where the router is absent. Owner call (below).
- *Why not C, since nothing is broken?*
  Because the PLAN's goal is dogfooding, and C hides exactly the friction (per-mount
  options, optional components, role split) that forge needs to learn from.

### Decision points for the owner
1. **Is the role split permanent?** Is `api` / `worker` / `gateway` as three binary
   roles with different infra a product constraint forge should model (G4: per-role
   infra / multi-binary composition), or should reliant converge on forge's "one server,
   every service" with the worker and gateway as `scaffold binary`s? This decides
   whether D's second phase is a forge feature or a reliant reshape.
2. **Missing collaborator: unmounted or Unavailable?** A service that lacks its
   collaborator (no daemon router, no raw SQL) can be unmounted (forge grows "declined"
   components, G2) or mounted and returning `Unavailable` (forge unchanged, client
   behaviour changes).
3. **Does serverkit own the API listener and drain now (B step 4), or in P7?** Earlier
   means less hand code sooner. Later means one risky transport change instead of two.
4. **Is the CLI/KCL surface frozen?** Keeping `reliant server api|worker|gateway` (B)
   versus forge's generated subcommands (A) is a control-plane deploy change, and
   acceptable pre-launch, but it couples two repos' releases.
5. **Decided 2026-10-15: no monolith mode.** `reliant server` serves only `api|worker|gateway`; no option has to serve all roles in one process.
