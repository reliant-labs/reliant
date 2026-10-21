# 09 — Roots: reliant's three server roles as composition-graph roots

Status: **plan**, for the implementing agents. Shared context: `00-BRIEF.md`, `PLAN.md`.
Goal: design §6 step 5 of forge's `docs/design/composition-graph.md`. `api`, `worker` and
`gateway` become `//forge:root` structs in `internal/app`, `cmd/reliant/cmd/roots_gen.go`
generates their commands, and `internal/serverapi`, `internal/serverworker` and
`internal/servergateway`'s `Run`s are deleted, along with `internal/grpc.NewAPI`'s
600-line constructor.

Baseline: reliant `00047f78` (#1123), forge `origin/main` `5f5d96ac0` (contains PR 7,
`8e36973aa`). The re-pin to ≥ `8e36973a` is assumed to have landed. It needs a `go mod tidy`:
forge main's `pkg/serverkit/rest.go` doesn't compile against reliant's current `vanguard`
(`undefined: vanguard.Service`, seen in the scratch run below).

---

## 0. What running forge on this tree showed (scratch, not hypothetical)

I copied reliant HEAD and forge main to `/tmp/roots-scratch` (`git archive`) and built forge
from main, with reliant `replace`d onto it:

| Step | Result |
|---|---|
| `git mv cmd/reliant/commands cmd/reliant/cmd`, `package cmd`, fix `main.go` + `tools/docgen/cli` | `go build ./cmd/... ./tools/docgen/...` passes. The only other references are comments: `release.yml:476`, `workflowref/one_rule_test.go:33-34` (file paths in a table, which **are** load-bearing), `daemonruntime/cmd_forge.go:50`, `runenv_test.go:90`, `tokenauthority/authority_test.go:52`, `pkg/config/build_defaults_test.go:62`, `proto/config/v1/cli_config.proto:38`. |
| `forge generate` on the moved tree, no roots | Config gen now detects the primary binary, so it **scaffolds `cmd/reliant/cmd/serve.go`** (the graph `runRoot` template), plus `metrics.go`, `internal/metrics/metrics.go`, `internal/app/{providers,auth,login_broker}.go`. It also changes `pkg/config/config_gen.go` to emit `type Config = configv1.ReliantConfig`, which collides with the hand-written `pkg/config/config.go` (`type Config struct{}`). Once `serve.go` exists, `stepInternalAppComposition` runs, and **generate fails: 104 `Deps` fields with no provider** across all 33 handler components. |
| Same, plus `internal/app/roots.go` declaring only `//forge:root gateway command=server,gateway` | **Still 104 missing providers**, including `Account.Deps.SQL`, `Chat.Deps.Temporal` and so on, none of which the gateway reaches. Resolution covers every discovered node, not a root's closure (§3 F3). |
| The scaffolded `serve.go`'s `serverkitConfig` | Reads `cfg.Port`, `cfg.OtlpEndpoint`, `cfg.PprofAddr`, `cfg.Environment`, `cfg.PreStopDelay` and `cfg.ShutdownTimeout` off `*config.Config` = `ReliantConfig`, which has none of them (it has `log`, `runtime`, `storage_reaper`). `ConfigFields` is the union over **every** config message. It doesn't compile (§3 F1). |
| Same experiment on the un-moved tree | No composition step runs, because there's no `cmd/<bin>/cmd/root.go`, so no `serve.go`. That's why reliant has never seen any of this. |

Consequences for the plan:
1. The cmd move is **not** purely mechanical. It turns forge's command-tree and composition
   machinery on, so PR R2 must take the new state into account (§2).
2. You can't declare one root at a time on today's forge. Forge needs fixes first (§3).

---

## 1. Component map

Notation: **Type** is the node's Go type in `internal/app`. **Deps** are the edges.
**Roots** means A = api, W = worker, G = gateway. **LC** means the lifecycle methods forge
will read off the type (R = `Run(ctx) error`, D = `Drain`, C = `Close`, Rd = `Ready`). "adapter"
means the existing method signature doesn't match (`Start(ctx)`, `Stop()`, `Run(ctx, interval)`,
no error), so a thin owned type in `internal/app/nodes_*.go` gives it forge's signature.
Adapters live in `internal/app` rather than editing `internal/handlers/**`, which P5b lanes edit.

### 1.1 Shared infrastructure (providers in `internal/app/providers*.go`)

| Node | Type | Deps | Roots | LC | Today |
|---|---|---|---|---|---|
| Repo | `*db.Repo` (provider `NewRepo`) | `ServerProcessConfig.database`, `data_dir` | A W G | C, Rd (`Ping`) | `db.NewRepoFromConfig{Migrate: Apply/Wait}` in each `Run`. **Always `MigrateWait`** under the graph. Migration moves to root policy (§4.3). |
| SQL | `*sql.DB` (`repo.DB.SQLDB()`) | Repo | A W G | — | `repo.DB.SQLDB()` inline (account, claimcheck, videojobs, connectorgrant, tokenauthority) |
| RepoUpdateFeed | `*RepoUpdateFeed` (adapter: `repo.SetUpdateNotifiers` → hubs) | Repo, UserUpdateHub, ChatUpdateHub | A W (**not G**) | — | inline in api and worker. The gateway never set notifiers, so it's a separate node and a root field of A and W only. |
| NATS | `*NATSConn{*nats.Conn}` | `ServerProcessConfig.nats` | A W G | C, Rd (`IsConnected`) | `natsutil.Connect` |
| JetStream | `jetstream.JetStream` | NATS | G | — | `jetstream.New(nc)` |
| StreamingHub | `streaming.StreamingHub` wrapper | nats config, `streaming_driver` | A W | C, Rd (`IsConnected`) | `streaming.NewStreamingHub` |
| UserUpdateHub | `streaming.UpdateHub[db.UserUpdate]` (named type) | NATS | A W G | C | `NewNATSUpdateHub("user.updates")`. In G it's publish-only, but it's the same constructor. |
| ChatUpdateHub | `streaming.UpdateHub[db.ChatUpdate]` (named type) | NATS | A W | C | same |
| PayloadStore | `*claimcheck.PostgresStore` | SQL | A W | — | `claimcheck.NewPostgresStore` |
| Temporal | `client.Client` wrapper `*TemporalClient` | `ServerProcessConfig.temporal`, PayloadStore | A W | C, Rd (`CheckHealth`, which the worker checks today. The api does not, so add it to the api too or keep it worker-only via a W-only ready node; see §4.2) | `temporal.NewExternalClient` |
| Vault | `vault.Service` | Repo, control_plane, data_dir, `integrations.vault_key` | A W | — | `db.BootVault` |
| ControlPlaneEndpoint | `controlplane.Endpoint` (value node) | `ServerProcessConfig.control_plane` | A W G | — | `controlPlaneEndpoint(...)` |
| TokenAuthority | `tokenauthority.Authority` | ControlPlaneEndpoint, SQL | A G | — | `tokenauthority.New(DepsFor(...))`. The gateway's is uncached. The api builds it inside `NewAPI` (`resolveTokenAuthority`) and wraps it cached for the interceptor. |
| TLSFiles | `*TLSFiles{Cert, Key}` | `ServerProcessConfig.tls`, data_dir | A G | — | `certs.EnsureCerts` block duplicated in api and gateway |
| ProcessRuntime | `*ProcessRuntime` (adapter, no LC) | Repo, ControlPlaneEndpoint, llm, preview, analytics, sentry, github bot | A W | — | The process globals that need the repo: `drivers.InitializeAPIKeyProvider(repo)`, `drivers.InstallReliantKeyHealer`, `analytics.Start`. Make it the **last** root field so it's built first (§4.4). |
| Pprof | `*Pprof` (nil when off → NilGuard) | api/worker `pprof_port`, gateway `pprof_addr` | A W G | R, C | `debugserver.Start` / `startPprofServer` (fire-and-forget goroutines today) |

### 1.2 gateway root (`internal/servergateway/run.go`, 606 lines)

| Node | Type | Deps | LC | Notes |
|---|---|---|---|---|
| DaemonAuth | `*DaemonAuth` provider, **`//forge:audience daemon`** | TokenAuthority | — | Extracted from `grpc.NewDaemonChain`, which builds `interceptors.NewDaemonAuthInterceptor` itself today. `lint --graph` reads the audience from an authenticator **provider**, so the chain must take it as a Dep. |
| DaemonChain | `*grpc.DaemonChain` | DaemonAuth | — | `grpc.NewDaemonChain`, changed to take the auth |
| ToolsDaemon | `*tools_daemon.Service` (component) | Repo, UserUpdateHub, StatePublisher, ResultPublisher, Config, Logger | adapter `ToolsDaemonSweeper`: R = `Start(ctx)`, C = `Close()` | `tools_daemon.New`. The sweeper adapter is the node that stops last. |
| StatePublisher | `*daemonstate.Publisher` | NATS | — | |
| ResultPublisher | `*toolexec.ToolResultPublisher` | NATS | — | |
| DaemonRoutes | generated `DaemonRoutes` (routes_gen) | ToolsDaemon | — | replaces the hand `RoutesFunc` in `NewDaemonListener` |
| DaemonListener | `*grpc.DaemonListener` | DaemonChain, DaemonRoutes, TLSFiles, `daemon_port`, `bind_address` | R D C Rd | `RedialOnDrain` stays |
| ToolBridge | adapter `*ToolBridge{*toolexec.NATSToolBridge}` | NATS, JetStream, ToolsDaemon | C | constructor: `New…`, `ToolsDaemon.AddConnectionListener(b)`, `b.Start()` |
| LifecyclePublisher, EventPublisher, StatusResponder, UserLivenessResponder | each a registration node (no LC) | NATS / JetStream / ToolsDaemon | — | constructor = build + `AddConnectionListener`. EventPublisher runs `daemonevents.EnsureStream` (10s) first. **The registration order matters** (§4.1). |
| Derivation | adapter `*Derivation` | NATS, Repo, ToolsDaemon (`NotifyLifecycleApplied`) | R | |
| ActivitySubscriber | adapter | NATS, ToolsDaemon | C (`Stop`) | `Start` in the constructor, as today |
| DaemonConnector | `*servergateway.DaemonConnector` | ToolsDaemon, EventPublisher | — | the package keeps `daemon_connector.go`, `managed_reconciler.go` and `flowhealth.go` |
| ManagedReconciler | adapter `*ManagedReconciler` | JetStream, DaemonConnector | R (`Start`), C (`connector.CloseAll`) | |
| FlowHealth | probe-surface route | Repo, ToolsDaemon | — | `/flow-health` on the health port (§3 F5) |

```go
//forge:root gateway command=server,gateway
// Field order is STOP order (first stops first). Matches TestGatewayNodesStopTheHandOverFirst.
type Gateway struct {
	Reconciler   *ManagedReconciler   // outbound dials stop first
	Daemon       *grpc.DaemonListener // make-before-break hand-over
	Activity     *ActivitySubscriber
	Bridge       *ToolBridge
	Derivation   *Derivation
	Registrations *GatewayRegistrations // event/lifecycle publishers + responders, in today's order
	Sweeper      *ToolsDaemonSweeper  // tools-daemon: stops last of the nodes
}
```

`ToolsDaemon` is a dependency of every field, so it would stop after them all even without
`Sweeper`. `Sweeper` is listed explicitly so the `tools-daemon` node keeps its own name and its
`Run`/`Close`.

### 1.3 worker root (`internal/serverworker/run.go`, 702 lines)

| Node | Type | Deps | LC | Notes |
|---|---|---|---|---|
| WorkerDaemonRouter | `*WorkerDaemonRouter{*toolexec.NATSDaemonRouter}` | NATS, Repo, ControlPlaneEndpoint, AutomationCreds | — | **Its own type.** The worker alone has `WithControlPlaneCredentials(automationcred)` + `WithRunResumer`, so the api must never get it (§4.5). |
| RemoteExecutor | `*toolexec.RemoteExecutor` | WorkerDaemonRouter, ServerExecutor, DaemonClientFactory | — | today a two-phase setter (`SetDaemonRouter`, `SetServerExecutor`), which stays in its compose method |
| StoredConfigProvider | `config.StoredConfigProvider` | Repo | — | |
| WorkerChat / WorkerRun / WorkerTrigger | `*WorkerChat{*chat.Service}` and so on | WorkerDaemonRouter, Temporal, Pause, Repo, … | — | Distinct types from the api's handler nodes, because their Deps differ by root (router, trigger has no Senders/Access/ControlPlane). |
| PauseService, RunLifecycle, RunLauncher, AgentRuns | concrete | Temporal, Repo | — | |
| TriggerPollers | `*webhook.Registry` | integrations | — | the api's `inboundRegistry` is the same constructor, so this is one node for both |
| IntegrationCredentials, CatalogSearch, ConnResolver, WriteLedger | as `serverworker/connections.go` | Repo, Vault, ControlPlaneEndpoint | — | `checkExecutors()` becomes a constructor check |
| WorkerToolsFactory | `*WorkerToolsFactory{*tools.ToolsFactory}` | many | — | Distinct type: the api's `ToolsFactory` is catalog-only. |
| AccountRecorder | `*accountlimits.Recorder` adapter | Repo | C | |
| TemporalWorker | `*workersetup.Worker` | everything above | R D Rd | `workersetup.NewWorker` |
| Relay | `*workflowevent.Relay` | Repo, Temporal | R | |
| UsagePoller | `*accountlimits.Poller` | Repo | R | |
| LedgerPrune | adapter (`PruneLoop(ctx, writes)`) | WriteLedger, Repo | R | |

```go
//forge:root worker command=server,worker
type Worker struct {
	Temporal      *workersetup.Worker // stops taking work first; its Drain releases activities
	Relay         *workflowevent.Relay
	UsagePoller   *accountlimits.Poller
	LedgerPrune   *LedgerPrune
	Recorder      *AccountRecorder // listed LAST so it is built first and stops last
	Feed          *RepoUpdateFeed
	Runtime       *ProcessRuntime  // built first: process globals before any constructor
}
```

Without the `Recorder` field, forge would stop the recorder straight after the Temporal worker,
which depends on it. That would be **before** relay/poller/prune and would break
`TestWorkerNodesStopTakingWorkFirst`. The field is how you keep today's order.

### 1.4 api root (`internal/serverapi/run.go` 876 lines + `internal/grpc/server.go` `NewAPI` ~630 lines)

Handlers: the 35 user-audience services in `NewAPI` (`server.go:312-787`) become **one compose
method each**. Their Deps values are exactly today's literals: `Repo: g.Repo()`,
`Router: g.APIDaemonRouter()` (spelled out, because the router type is ambiguous once the
worker's exists), `Temporal: g.Temporal()`, and so on. The route set is the generated
`UserRoutes`, whose `MountOptions` carry `machineWake` (worktree, file_system) and
`forgeAPIDisabled` (forge, when `disable_forge_api`).

| Node | Type | Deps | LC | Notes |
|---|---|---|---|---|
| UserAuth | `*UserAuth` provider **`//forge:audience user`** | `auth.Settings` (from ApiServerConfig, JWT key file read), TokenAuthority (cached introspector) | — | `interceptors.NewAuthInterceptor` + `SetAccessTokenIntrospector`, extracted from `NewAPI` |
| UserChain | `*grpc.UserChain` | UserAuth, `allowed_email_domains` | — | |
| APIDaemonRouter | `*APIDaemonRouter{*toolexec.NATSDaemonRouter}` | NATS, Repo, ControlPlaneEndpoint | C | `daemonRouterOptions` |
| ToolsFactory | `*tools.ToolsFactory` (catalog-only) | Repo, Temporal, VideoJobs, … | — | |
| Connections (`conns`) | `*connections.Service`, `*connections.OAuthHTTP`, token source | Repo, Vault, OAuth clients, PublicURL, CORS | — | `wireConnections` |
| CatalogSearch | `*catalogindex.Service` | Repo | — | |
| InboundRegistry, TriggerInbound | `*webhook.Inbound` | Repo, Temporal, Vault, Registry, Connections | — | |
| GitHubAccess | `*ghaccess.Refresher` **or nil** | Repo, Vault, ControlPlaneEndpoint, integrations | — | **Conditional** (no `RELIANT_GITHUB_WEBHOOK_SECRET` → nil). |
| GitHubAccessLoop | adapter **or nil** | GitHubAccess | R | separate from the Refresher so only api runs the loop (§4.5) |
| TriggerSenders | `map[string]trigger.SenderDirectory` (named type), may be empty | same condition | — | `// forge:optional-dep` on the trigger Deps field |
| ResetGuard, PauseService, WorkflowReconciler | | Temporal, Repo, QueuedDelivery, BackgroundProcessDaemons | adapter R (`StartBackgroundReconciliation`+`<-ctx.Done()`), C (`Stop`) | setters (`SetResetGuard`, `SetQueuedDelivery`, `SetBackgroundProcessDaemons`) move into compose methods |
| QueuedDelivery | `queueddelivery.Service` + adapter R | Repo, APIDaemonRouter, Chat, Temporal, NATS | R | |
| DaemonEventsConsumer | adapter R | Project, NATS | R | |
| WorktreeSweeper | `*worktreesweep.Sweeper` (data) + adapter `SweepLoop` R | Repo, APIDaemonRouter | R | The sweeper is also a Dep of the worktree and inbox handlers. Only the loop has `Run`. |
| ScheduleSyncer, TriggerReconciler, TriggerRedriver, ScheduleSync(startup) | adapters R | Temporal, Repo, Registry | R | |
| KeySweep | adapter **or nil** | ProcessRuntime (healer) | R | **Conditional** (no control plane → nil) |
| PayloadGC | adapter R | PayloadStore, `payload_blob_gc_horizon` | R | |
| APIHTTPRoutes | owned route-set node | terminal WS, attachment content, OAuth routes, trigger inbound, connector MCP, CLI login metadata, `/health` JSON (`auth_mode`, which `web/src/hooks/useAuthMode.ts` reads on the **API** port) | — | `NewAPI`'s `http` routes |
| APIListener | `*grpc.APIListener` | UserChain, `RoutesFunc(UserRoutes ∘ APIHTTPRoutes)`, TLSFiles, ports, CORS | R D C Rd | |

```go
//forge:root api command=server,api
type API struct {
	Listener          *grpc.APIListener
	TriggerRedriver   *TriggerRedriver
	TriggerReconciler *TriggerReconciler
	ScheduleSync      *ScheduleSync
	WorktreeSweep     *SweepLoop
	QueuedDelivery    *QueuedDeliveryLoop
	DaemonEvents      *DaemonEventsConsumer
	GitHubAccess      *GitHubAccessLoop    // nil without a webhook secret
	Reconciler        *WorkflowReconciler
	KeySweep          *KeySweep            // nil without a control plane
	PayloadGC         *PayloadGC
	Feed              *RepoUpdateFeed
	Runtime           *ProcessRuntime
}
```

### 1.5 How forge models absence: answered

Use a constructor that returns `(nil, nil)`. The generated accessor's lifecycle literal is
`NilGuard`ed (`if c == nil { return lifecyclekit.Node{} }`, `graph_gen.go.tmpl`), and
`graphkit` skips an empty node. Consumers of something that may be absent mark the Deps field
`// forge:optional-dep`. **Don't use `//forge:standby`.** It applies to a *provider no root
reaches* that you're keeping for later, and here the node is reached but disabled by
configuration. This keeps `TestAPINodesOmitAbsentLoops` expressible: build the api with no
control plane and no webhook secret, and assert that `KeySweep`/`GitHubAccess` aren't in the
supervised set.

### 1.6 Root policy, not nodes (owned `cmd/reliant/cmd/serve.go`, `runRoot`)

These are per process, and today they run before anything is constructed. They can't become
graph nodes without fake edges (design §8):
- config load + `runenv.Configure(cfg.Runtime.Environment)` (`loadRootConfig`)
- `logging.SetupWithRotation` (per-root file: `reliant.log` / `temporal-worker.log` /
  `daemon-gateway.log`), `observability.Init(service name per root)`, `telemetry.SetReporter`,
  `Policy.Flush` = `telemetry.Flush(5)` then `obsProvider.Shutdown`
- the config-only process globals: `drivers.Configure`, `tools.ConfigurePreviewURLs`,
  `models.InitGlobalRegistryWithUserConfig`, `github.ConfigureBot`,
  `os.Setenv("RELIANT_DATA_DIR")`
- **migration** (api only, §4.3)
- boot probes, the drainer (per-root `DrainConfig`), signal handling

This needs the command configs to be available to `runRoot` **before** the policy runs. Forge
can't do that today (§3 F2).

---

## 2. PR sequence

```
forge:   F-A (F3+F4) ─┐
         F-B (F1+F2+F6+F7) ─┼─▶ reliant re-pin ─┐
         F-C (F5)  ─┘                         │
reliant: R1 config reshape ──────────────────────┤
         R2 cmd move (a) ────────────────────────┼─▶ R3 app skeleton + gateway (b) ─┬─▶ R4 worker (c) ─┐
                                                 │                                └─▶ R5 api (d) ────┴─▶ R6 cleanup + gate (e)
control-plane (owner decision §6): R7 consume ROOTS, after R6
```

Your order (a)→(b)→(c)→(d)→(e) holds with **three changes**:
1. (a) can't be "mechanical, then generate" without one extra step: forge scaffolds `serve.go`,
   which switches composition on for every component. R2 deletes the scaffolded `serve.go`. Forge
   records that as "deliberately deleted" (`checksums.ScaffoldOnceDecision`), so composition stays
   off until R3. This is forge's documented "tree without runRoot" state, and the plan uses it on
   purpose. It's not a trick: `roots_gen.go`'s own comment describes it.
2. A config reshape **R1** comes first. Each role reads its own copy of the shared blocks
   (`database`, `nats`, `temporal`, `control_plane`, `tls`, `observability`, `sentry`,
   `analytics`, `llm`, `preview`, `integrations`, `data_dir`, `streaming_driver`). A shared
   node such as `Repo` has **one** compose method, so it can't read `g.ApiServerConfig` in one
   process and `g.GatewayConfig` in another. Move them into one subtree config over `server`.
3. Forge fixes F1–F5 land before R3 (§3).

### R1: shared server blocks into a `server` subtree config *(can start now)*
- **What:** `proto/config/v1/server_config.proto` gets
  `message ServerProcessConfig { option (forge.v1.binary_config) = {binary: "reliant", subtrees: [{path: ["server"]}]}; DatabaseConfig database = …; NatsConfig nats; TemporalConfig temporal; ControlPlaneConfig control_plane; ServerTLSConfig tls; ObservabilityConfig observability; SentryConfig sentry; AnalyticsConfig analytics; LLMConfig llm; PreviewConfig preview; IntegrationsConfig integrations; string data_dir; string streaming_driver; }`.
  Delete those fields from `ApiServerConfig`, `WorkerConfig` and `GatewayConfig` (pre-launch, no
  compat), and **reserve their tags**. Role-specific fields stay where they are: ports and their
  per-role defaults (`HEALTH_PORT` 8081/8081/8080, which control-plane pins via
  `lib/env.k:402`), `bind_address`, `pprof_*`, `drain` (the worker's budget is allowed to
  differ), and the api's auth/CORS/upload/etc.
- **Where:** `proto/config/v1/{server,api_server,worker,gateway}_config.proto`, `pkg/config`
  (generated), `cmd/reliant/commands/server.go` (the loaders read
  `config.LoadServerProcessConfig`), and the `*_config_test.go` files in `cmd/reliant/commands`.
- **Invariant:** every env var name, flag name and default is unchanged. Flags become persistent
  on `server`, so `reliant server api --db-driver …` still parses. Pin this with a test that
  diffs `reliant server <role> --help` flag sets before and after, and the env-var census per role.
- **Conflicts:** none with P5b. Lane A (#1123) has merged.
- **As landed:** `ServerProcessConfig` (server_config.proto) holds every shared block except
  the database. `DatabaseConfig` (db_config.proto) is a second subtree config, over `server`
  **and** `db migrate`, so the migration commands load the same declaration (the
  `DbMigrate*Config` messages are gone). Its leaves are flat and carry forge's serve-field
  names (`database_url`, `db_max_open_conns`, `db_max_idle_conns`, `db_connect_timeout`), and
  each role's drain budget is flat `pre_stop_delay` / `shutdown_timeout`, so forge's F2
  `serverkitConfig` finds them unaided. Two keep reliant's names, and R3's owned
  `serverkitConfig` maps them: `health_port` (forge's `port` is the API port, which for
  reliant is `grpc_port`; the probe address must also honour `bind_address`) and the `tls`
  block (forge's `tls_cert_path` / `tls_key_path` would serve TLS on the probe address, but
  `HEALTH_PORT` is plaintext). Every env var and flag name is unchanged. Superset only: the
  gateway now also loads temporal, streaming, integrations, sentry, analytics, llm and
  preview, and the worker loads tls. Pinned by `cmd/reliant/cmd/server_role_surface_test.go`.

### R2: (a) the cmd move *(can start now, after the re-pin)*
- `git mv cmd/reliant/commands cmd/reliant/cmd`, then `package cmd` / `cmd_test`. Update
  `cmd/reliant/main.go` (keep `pid1.Init()` and `heavyjob.Dispatch()` first),
  `tools/docgen/cli/main.go`, `workflowref/one_rule_test.go:33-34` (paths), and the comments
  listed in §0.
- `root.go` adopts forge's seams: `type Deps struct{Cmd *cobra.Command; Out, Err *os.File}`,
  `type Factory`, `registerCommand` / `registeredCommands`, `const ServiceName = "reliant"`.
  Forge's `metrics.go` calls `registerCommand` and `Deps`. Keep `metrics.go` +
  `internal/metrics/metrics.go` (it adds `reliant metrics schema`; update
  `command_surface_test.go`), or delete both. My recommendation is to keep them.
- **Delete the scaffolded `cmd/reliant/cmd/serve.go`** and say why in `root.go`'s header: the
  roots arrive in R3, and composition stays off until a `runRoot` exists.
- Delete `pkg/config/config.go`. Its own header says to delete it once P4 lands, and forge now
  emits `type Config = configv1.ReliantConfig`. Handler `Deps.Config *config.Config` and the
  generated harnesses' `&config.Config{}` still compile.
- Delete `internal/app/{auth,login_broker}.go` if forge scaffolds them. They're forge's
  generic sign-in, and reliant has its own. Keep `providers.go` only if composition is on; in R2
  it isn't, so delete it as well.
- Gates: `make forge-generate-check` is a no-op on the second run, `go build ./...`,
  `forge lint`, `make test-affected`, and `reliant --help` / `reliant server api --help` are
  byte-identical (help_text_test, command_surface_test).
- **Conflicts:** every in-flight branch that edits `cmd/reliant/commands/*` has to rebase.
  git follows the rename. Announce it before merging.

### R3: (b) `internal/app` skeleton + gateway root *(needs F-A, F-B, F-C, the re-pin, R1, R2)*
- `forge project rescaffold cmd/reliant/cmd/serve.go`, or write `runRoot` by hand to §1.6.
  `root.go` calls `rootCommands(deps)`. **The transition merge:** `rootCommands` returns a
  generated top-level `server` command. `root.go` finds it and `AddCommand`s the remaining hand
  `newServerAPICmd()` / `newServerWorkerCmd()` under it, so `reliant server api|worker` keep
  working while `server gateway` is generated. Delete `newServerGatewayCmd`.
- `internal/app/providers.go` (owned) holds §1.1's providers: Repo, SQL, NATS, JetStream,
  UserUpdateHub, ControlPlaneEndpoint, TokenAuthority, TLSFiles, Pprof. `internal/app/nodes_gateway.go`
  holds the gateway adapters and registration nodes. `internal/app/roots.go` holds
  `Gateway`, with `servergateway.Run`'s `Long` text as its doc comment, because roots_gen takes
  Short/Long from it.
- `grpc.NewDaemonChain` takes a `*DaemonAuth`. Add a `NewDaemonAuth` provider marked
  `//forge:audience daemon`. The proto audience is **already declared** (#1057,
  `proto/reliant/v1/tools_daemon.proto:18`).
- Delete `servergateway/run.go` (`Run`, `Options`, `gatewayNodes`, `startPprofServer`) and
  `loadServerGatewayConfig`. Port its validations into provider constructors:
  `checkServerPorts`, the `databasePool` refusals, "NATS_URL required".
  `servergateway/nodes_test.go` becomes a stop-order pin over `app.PlanGateway()` (F7).
- `forge.yaml` `lint.rules`: `forge-graph-unreachable: warn` and `forge-graph-unresolved: warn`
  (F4). These come out in R6.
- **As landed:** `internal/app/roots_gateway.go` declares `Gateway` (field order = stop order:
  reconciler, daemon listener, activity subscriber, connection listeners, tool bridge, state
  derivation, tools-daemon sweeper, flow health, pprof). `PlanGateway().Stop()` is pinned
  verbatim by `TestGatewayStopsTheHandOverFirst` (today's six nodes, then user_update_hub,
  nats_conn, db_repo, pprof, probe_listener); the registration order by
  `TestGatewayConnectionListenersRegisterInOrder`. Names are forge's snake_case node names.
  Providers in `providers.go` (DBRepo — not `Repo`, which is the `handlers/repo` node —
  NATSConn, JetStream, UserUpdateHub, TokenAuthority, TLSFiles, Pprof), gateway nodes in
  `nodes_gateway.go`; the config conversions every role shares moved to
  `internal/app/server_config.go`, which the hand api/worker loaders call too.
  `grpc.NewDaemonListener` takes `Routes` (the generated `DaemonRoutes`) instead of the
  service. `runRoot` (owned `serve.go`) keeps reliant's per-root policy in `rootPolicies`
  (service name `daemon-gateway`, `daemon-gateway.log`), reliant's observability.Init and
  its `/metrics`; it never migrates. The probe address is `BIND_ADDRESS:HEALTH_PORT`,
  plaintext; `GatewayConfig` gained env-only `LIVENESS_PATHS` (`/health,/healthz`) and
  `READINESS_PATHS` (`/ready,/readyz`); `/flow-health` is `g.Probes.Handle` from
  `newFlowHealth`. `/health` now answers `ok` instead of JSON (status codes unchanged), and
  `/ready`'s checks are named by node (`db_repo`, `nats_conn`, `daemon_listener`). NATS is
  now a wait, not a crash: the first connect retries (`serverkit.RetryConnect`, 5m) with
  `nats` pending in the boot `/ready`. `forge-graph-unresolved` did not exist to set; only
  `forge-graph-unreachable: warn`. Forge fixes it needed, in forge #1067: root help from the
  root's doc comment, root-config flags not re-registered on root commands when `root.go`
  homes them, forge's optional `nil` placeholders re-resolved (they would otherwise freeze
  the `system`/`catalog`/`daemon` handlers' optional deps at nil for R5), and a nil guard on
  `g.Probes` not counting as serving the probes. Still open for R5–R7: the reach analysis
  does not see `runRoot`'s reads through `root.Configs` (the gateway's `OTEL_*` /
  `PROMETHEUS_ENABLED` are missing from `ROOTS`), and the generated `server` group command's
  help is root.go's (`mergeServerRoles`) until forge can declare a group's help.

### R4: (c) worker root *(needs R3)*
- Providers and nodes from §1.3, plus the shared Temporal, StreamingHub, ChatUpdateHub,
  PayloadStore, Vault and ProcessRuntime (first used here). `//forge:root worker command=server,worker`.
- Delete `serverworker/run.go` and `loadServerWorkerConfig`. `serverworker/connections.go`
  (the credential/catalog wiring) moves to `internal/app/providers_worker.go`, and its tests
  move with it. Delete the hand `newServerWorkerCmd`.
- **Conflicts:** P5b lanes that change `chat`/`run`/`trigger` Deps edit `serverworker/run.go`
  today. After R4 the reconciler injects those keys in compose.go by type. A lane adding a new
  Deps type adds its provider in its own `internal/app/providers_<lane>.go`, which avoids
  conflicts in `providers.go`.
- **As landed:** `internal/app/roots_worker.go` declares `Worker` (`migrate=wait`;
  field order = stop order: temporal worker, workflow-event relay, account-usage poller,
  write-ledger prune, account recorder, then the infrastructure as fields of their own —
  streaming hub, Temporal client, the update feed, the debug server — and last `Runtime`
  and `Settings`, so they are built first). `PlanWorker().Stop()` is pinned verbatim by
  `TestWorkerStopsTakingWorkFirst` (today's five nodes, then streaming_hub,
  temporal_client, chat_update_hub, user_update_hub, nats_conn, debug_server, db_repo,
  probe_listener); readiness (db_repo, nats_conn, temporal_client, streaming_hub,
  temporal_worker) by `TestWorkerReadiness`. The infrastructure needs root fields too,
  not only the recorder: reached first through the Temporal worker or a loop, it would
  stop between them. Shared providers added to `providers.go`: IntegrationSettings (one
  parse of IntegrationsConfig), ProcessSettings (the config-only globals:
  drivers.Configure, preview URLs, model registry, GitHub bot, RELIANT_DATA_DIR — a node
  rather than `runRoot` policy, so the reach sees their config), ProcessRuntime (key
  provider + healer, analytics, Sentry), PayloadStore, TemporalClient (the first dial
  retries like NATS; `/ready` names `temporal` meanwhile), StreamingHub, ChatUpdateHub,
  RepoUpdateFeed, Vault, PauseService, TriggerPollers, DebugServer (debugserver's
  handler as a node with Run/Close). The worker's own are in
  `providers_worker.go` (with `serverworker/connections.go` and its tests); its variants
  are distinct types marked `//forge:root-only worker` (WorkerDaemonRouter,
  WorkerChat/Run/Trigger, WorkerToolsFactory, WorkerCatalogSearch,
  WorkerIntegrationCredentials, RemoteExecutor, the loops, the Temporal worker). That
  marker is forge #1095: a distinct type alone did not keep the delegated-token router
  out of the api's handlers — resolution fills an interface-typed Deps field with any
  single assignable node, and generate had written `g.WorkerDaemonRouter()` into the
  daemon, terminal and background handlers. `WorkerConfig` gained env-only
  `LIVENESS_PATHS`/`READINESS_PATHS`; the probe address is `:HEALTH_PORT` (the worker
  has no BIND_ADDRESS). `runRoot` flushes Sentry before OTel for every root. The shared
  config conversions (integrations, LLM, preview, analytics, Sentry, Temporal) moved from
  `cmd` to `internal/app/server_config.go`. Re-pinned to forge `2472900c` (#1095, and
  #1096: what `runRoot` reads is each root's reach, so `ROOTS` now carries the
  observability vars and the worker's HEALTH_PORT; `//forge:group server` in
  `internal/app/roots.go` is the `server` command's help, so `mergeServerRoles` only
  adds `server api`).

### R5: (d) api root *(needs R3; develop in parallel with R4, land after it)*
- Everything in §1.4. `internal/grpc/server.go`'s `NewAPI`, `Config`, `validate` and
  `resolveTokenAuthority` are deleted. `server.go` keeps only the HTTP helpers
  (`newCORSHandler`, `securityHeaders`, `listenerConfig`, `connectorMCPRoutes`,
  `bindEventStreams`, `connectorTokenValidator`).
- Delete `serverapi/run.go`, `loadServerAPIConfig` and `validateServerAPIAuth` (validation moves
  into the UserAuth constructor). `daemon_router.go`, `github_access.go`, `connections.go` and
  `background_process_daemons.go` move to `internal/app/providers_api.go`, and their tests move too.
- **This is the P5b collision.** `NewAPI` holds the 35 handler Deps literals that P5b's
  consumer-interface lanes edit. Land R5 **after** the P5b lanes that change handler Deps
  merge. If P5b runs long, land R5 first and give the remaining lanes the
  `providers_<lane>.go` rule. Their `NewAPI` edits become no-ops, because the reconciler injects
  by type. R5's author owns that rebase either way.
- R4 and R5 both edit `compose.go` (owned, reconciled) and `roots.go`. To keep their merge
  conflicts trivial, put one root per file: `roots_api.go`, `roots_worker.go`. compose.go
  conflicts are append-only. Rebase, then rerun `forge generate`.
- **As landed:** `internal/app/roots_api.go` declares `API` (`migrate=apply`; field order
  = stop order: the API listener, the ten loops in today's order, then the infrastructure
  as fields — the api's daemon router, the update feed, streaming hub, Temporal client,
  the debug server — and last `Runtime`, `Settings` and `APISettings`, built first).
  `PlanAPI().Stop()` is pinned by `TestAPIStopsTakingCallsFirst`: today's eleven nodes
  verbatim (forge's names: `api_listener` … `payload_gc`, `git_hub_access_loop` for
  `github-access`), then api_daemon_router, chat_update_hub, user_update_hub,
  streaming_hub, nats_conn, temporal_client, api_debug_server, db_repo, probe_listener —
  today's deferred closes but for the streaming hub now closing before NATS (its first
  connect waits for NATS, R4's edge) and the debug server, which nothing used to stop.
  Readiness is db_repo, temporal_client (**new on the api**: a Temporal that does not
  answer now takes an api replica out of rotation), nats_conn, streaming_hub,
  api_listener. Each of the 35 handlers is a compose method whose Deps are today's
  literals; every Router is `g.APIDaemonRouter().NATSDaemonRouter` (the wrapper's
  `Close(ctx)` shadows the router's `Close()`, so it no longer satisfies
  `toolexec.DaemonRouter` itself). The route set is the generated `UserRoutes` with
  `apiMountOptions` (machinewake on worktree/file_system, `grpcserver.ForgeAPIDisabled()`
  on forge); the plain-HTTP routes are `grpcserver.APIHTTPRoutes` (node `http_routes`),
  composed with `UserRoutes` in `NewAPIListener`. `grpcserver.UserAuth` is the user
  audience's authenticator (`//forge:audience user` on `NewUserAuth`). Absent loops are
  constructors returning `(nil, nil)` (KeySweep, GitHubAccess, its loop, TriggerSenders,
  TriggerAlerting), pinned on a real graph by `TestAPIOmitsAbsentLoops`. Config
  validation moved into `APISettings` (`CheckAPIServerConfig`), the api root's last
  field, so a bad value still stops the process before anything is dialled — but after
  the boot probes bind and after migrations. The ResetGuard is its own node that installs
  itself on the shared PauseService: the worker's pause service still has none.
  Migration is `runRoot` policy: `migrateSchema` runs `db.ApplyOnBoot` through
  `serverkit.AutoMigrateContext` (connect retried within DB_CONNECT_TIMEOUT) on
  `MigrationURL`, and `TestOnlyTheAPIMigrates` pins the roots' generated `Migrate` /
  `AwaitMigrations`. `ApiServerConfig` gained env-only `LIVENESS_PATHS`/`READINESS_PATHS`;
  the probe address is `BIND_ADDRESS:HEALTH_PORT`, as before. `serverapi` is gone
  (`input_options.go` moved too); its tests are `internal/app/providers_api_*_test.go`,
  and `TestServerAPIDoesNotBuildMCPManager` checks internal/app. The grpc tests that
  drove `NewAPI` mount only what each calls (`startTestServer(t, routes...)`); the mount
  set and mount options are pinned in `internal/app/roots_api_routes_test.go`.

### R6: (e) delete the leftovers, gate the graph *(needs R4 + R5)*
- Delete the `lint.rules` graph overrides, so `forge lint --graph` gates through `forge lint`'s
  `graph lint` step (`gates: true`, already in the default lane, `lint_steps.go:495`). Remove the
  `server` merge shim from `root.go` and drop `serverapi`/`serverworker` if anything is left.
  Update `research/forge-migration/PLAN.md` and `forge.yaml`'s header comment. Delete the
  `forge:exclude-contract` markers that referenced "P3c".
- `main.go` becomes `pid1.Init(); heavyjob.Dispatch(); cmd.Execute()`. `cmd.Execute` exits by
  itself, matching forge's template.

**Parallel:** F-A ∥ F-B ∥ F-C (forge, separate files). R1 ∥ R2 ∥ the forge PRs. R4 ∥ R5 in
development, landing sequentially. **Conflicts:** R2 renames under every cmd branch. R4/R5 share
compose.go/providers. R5 collides with P5b on `NewAPI`. Nothing here edits `internal/db/**` or
`internal/store/**` except reading `db.NewRepoFromConfig` / `db.Migrate`.

---

## 3. Forge gaps: fix forge first

| # | Gap (evidence) | Why reliant needs it | Proposed design | Blocks |
|---|---|---|---|---|
| **F1** | The graph `serve.go`'s `serverkitConfig` reads `cfg.<Field>` for names collected from **every** config message (`ConfigFieldNamesFromMessages(messages)`, `generate_middleware.go:633`). For reliant that's `cfg.Port`, `cfg.PprofAddr`, … on `ReliantConfig`. Scratch-verified: it doesn't compile. | Any `binary_config` project whose root config isn't flat gets a `serve.go` that won't compile. | Compute `ConfigFields` from the **primary binary's root config message** (the one `type Config =` aliases), its own top-level fields only. Add an e2e fixture: binary_config root config with nested blocks only. | R3 (R2 deletes `serve.go`, so it isn't blocked) |
| **F2** | `runRoot` builds `ServerCfg` (addr, drain budget, TLS, pprof) and the logger from the root config **before** `root.Run` assigns the command configs (`roots_gen.go.tmpl`: `Run: func(g …){ g.<Msg> = …; return g.Run<Root>() }`). | Each of reliant's roles keeps its ports, `HEALTH_PORT` and `DrainConfig` in its command config, and the policy (log file, service name, migration) is per root. | `rootSpec` gains `Configs app.RootConfigs`, a generated struct of the command and subtree configs this root's command loaded. `runRoot` assigns it to the Graph **before** policy, and the scaffolded `serverkitConfig(cfg, rc, root.Name)` reads them. `Run` stops assigning configs. | R3 |
| **F3** | `assembleGraph` resolves **every** discovered node. With only `Gateway` declared, generate still fails on all 104 api handler Deps. Adopting forge's cmd tree is all-or-nothing. | Design §6 promises "partial adoption needs no fork". Without this fix, R3–R5 become one PR of about 4.7k lines. | Resolve each root's closure strictly: a missing producer **inside** a closure fails generate, as today. An unresolvable node **outside** every closure is left out of `graph_gen`/compose reconciliation (no accessor, no injected method; owned methods kept) and reported by `lint --graph` as a new `forge-graph-unresolved` error. A resolvable unreached node keeps its accessor (for hand mains, as §6 promises) and stays `forge-graph-unreachable`. | R3 |
| **F4** | `runGraphLint`/`collectGraphJSON` print raw `GraphFinding`s and never pass through `lint.rules` severity or `suppress` (`lint_graph.go:23-75`). | Staging: while R3/R4 are on main, the api handlers are unreached, and `forge lint` (gating) would fail. | Route graph findings through the same rule-severity path as forgeconv (`lint.rules: {forge-graph-unreachable: warn}`). | R3 |
| **F5** | Probes are fixed at `/healthz`/`/readyz`/`/metrics` (`Probes.Front`, `StartBootProbes`). Any compose read of `g.Probes` sets `ServesProbes` (`markRootReach`), which drops the probe listener. | control-plane's `RELIANT_PROBES` (`workloads.k:174`: `/ready`, `/health` on 8081), the GKE `HealthCheckPolicy` (`prod/main.k:1243`: `/health`:8081), the dev ingress `/flow-health` → 8081 (`dev/ingress.k:80`), and docker-compose `wget /health` must all keep working **unchanged**. The gateway also needs `/flow-health` and `/metrics` on the probe port. | `serverkit.Config.Probes = ProbePaths{Live, Ready []string}` (default `/healthz`, `/readyz`), honoured by **both** boot probes and `Probes.Front`, so reliant sets `Live: ["/health","/healthz"], Ready: ["/ready","/readyz"]`. Add `Probes.Handle(path, h)` for extra probe-surface routes, addable after construction like `AddReadyCheck`. `ServesProbes` should be true only when `g.Probes` reaches a **listener's `Front`**, not on any read. reliant sets `ServerCfg.Addr` = the health address, because its listeners take their own ports, so no `HealthAddr` is needed. | R3 |
| **F6** | `rootSpec.Migrate` is inferred as "reaches the DB", and the template runs `migratekit.AutoMigrate` only with an `AutoMigrate` config field. | reliant: **api applies, worker and gateway wait** (`db.MigrationPolicy`, `connect.go:50`). All three reach the DB. Migration goes through the goose bridge (`db.Migrate`). | Root option `migrate=apply\|wait\|none` (default: `apply` if it reaches the DB, today's behaviour). `rootSpec.Migrate` becomes that enum and is projected into `ROOTS`. The owned `runRoot` decides *how*. Fallback if it slips: `runRoot` branches on `root.Name`. That works, but hides a declarative fact. | should-have, R3 |
| **F7** | The start plan exists only as a comment on `Build<Root>`; `Built()` exists only after a real build. | Today's stop-order pins (`Test{Gateway,Worker,API}Nodes…`) are plain unit tests over the node lists. A graph root can't be built in a unit test without NATS/Postgres. | Emit `func Plan<Root>() []string` (static, from generate) plus each node's lifecycle methods (`PlanNodes<Root>() []PlanNode{Name, Run, Drain, Close, Ready bool}`). | R3 (or the stop-order pins are lost) |
| F8 | Every root's service name is `ServiceName` (`"reliant"`). | Dashboards and alerts key on `reliant-api-server` / `reliant-temporal-worker` / `daemon-gateway`. | Root option `service=<name>` on `rootSpec`. Today the owned `runRoot` can branch on `root.Name`. | could-have |
| doc | "Reach implies run": a node with `Run` that's reached by two roots is supervised in both. | `ghaccess.Refresher` as a trigger Dep would start the GitHub loop in the worker. Per-root variants (router, chat, tools factory) need distinct types. | Add a migration-skill section on per-root node variants: distinct types, root policy, and side-effect splitting. Add a lint warning when a compose method branches on `g.<CommandConfig> != nil` (role sniffing). | — |

Recommended forge PRs: **F-A** = F3 + F4 (`composition_graph_resolve.go`, `_lint.go`,
`cli/lint/lint_graph.go`); **F-B** = F1 + F2 + F6 + F7 (`config_gen.go`, `roots_gen.go.tmpl`,
`cmd-tree-serve-graph.go.tmpl`, `graph_gen.go.tmpl`, `composition_graph.go` root directive);
**F-C** = F5 (`pkg/serverkit/{probes,boot}.go`, `composition_graph_commands.go`). All three can
run in parallel. F-B is one author's, because they share the rootSpec shape.

---

## 4. Risks

### 4.1 Drain and stop order must stay the same
The pins today: `internal/servergateway/nodes_test.go` `TestGatewayNodesStopTheHandOverFirst`,
`internal/serverworker/nodes_test.go` `TestWorkerNodesStopTakingWorkFirst`,
`internal/serverapi/nodes_test.go` `TestAPINodesStopTakingCallsFirst` +
`TestAPINodesOmitAbsentLoops`. The behaviour pins: `internal/grpc/server_drain_test.go`
(`TestDrainEndsOpenStreamsAndReturnsPromptly`, `TestDrainStillDrainsUnaryRequests`),
`internal/grpc/daemon_listener_drain_test.go` (`…WaitsForTheDaemonToMove`,
`…EndsAStreamThatDidNotMoveWithUnavailable`), `internal/grpc/drain_logging_test.go`,
`internal/grpc/daemon_listener_test.go` (audience), `internal/handlers/tools_daemon/{rollout,tools_daemon_drain}_test.go`,
`internal/handlers/terminal/terminal_ws_drain_test.go`, and `internal/handlers/streaming/streaming_ephemeral_test.go` (drained announcements).

Under the graph, stop order = reverse construction order, and siblings follow root-field order
("last field first" when forcing). The root structs in §1.2–1.4 are laid out to reproduce
today's lists exactly. Each stop-order test moves to `internal/app` and asserts
`reverse(PlanX())` (F7) against today's list, **verbatim**. Two traps:
- **The worker's account recorder.** It's a dependency of the Temporal worker, so it would stop
  right after it, ahead of the loops. Listing it as the last root field keeps it last.
- **Gateway connection-listener registration order** (`toolBridge`, `lifecyclePublisher`,
  `eventPublisher`, `statusResponder`, `userLivenessResponder`). Under the graph this becomes
  construction order. Put all five in one `GatewayRegistrations` node whose constructor
  registers them in today's order, and pin it with a test over the recorded listener order.
- Closing infra after the nodes (router, hubs, NATS, Temporal, repo, done by `defer` today)
  becomes `Close` on the provider nodes, which run after every dependent. The order among infra
  changes slightly: NATS closes after the hubs that publish on it, as today, because the hubs
  depend on it. Nothing pins the order among infra; R3 adds an assertion that hubs close before NATS.

### 4.2 Probes before the first node and after the last
Today the health server binds **after** construction (DB open, **migration**, Temporal dial),
so during a long api migration :8081 refuses connections. It's shut down after `Supervise`
returns. The graph does better: `runRoot` starts boot probes on `ServerCfg.Addr` (= the health
address) before anything is touched. The probe listener takes over the address before the first
node and stops after the last (the `Run<Root>` template). Readiness = `Drainer` + every node's
`Ready`. That means the Repo/NATS/StreamingHub/Temporal provider types need `Ready` methods to
preserve today's `db`/`nats`/`nats-streaming`/`temporal` checks. The worker checks Temporal and
the api doesn't. Keep it that way by giving the api's Temporal no `Ready`? That would mean
per-root types, which is overkill. I recommend adding the Temporal check to the api as well and
calling it out in R5. **Body change:** `/health` on the health port returns `ok\n` instead of
JSON. Its consumers (kubelet, the GKE health check, `wget --spider`) only read the status code.
The JSON `/health` with `auth_mode` is on the **API** port, which the web client calls
(`useAuthMode.ts`), and that one is unchanged. This needs F5.

### 4.3 Migrations
Today: **api = `MigrateApply`** (`serverapi/run.go:284`, the only schema owner); **worker =
`MigrateWait`** (`serverworker/run.go:212`); **gateway = `MigrateWait`** (`servergateway/run.go:175`);
plus `reliant db migrate up`. All go through `db.Migrate` (goose bridge → migratekit, on
`MigrationURL`). Under the graph, the Repo provider is always `MigrateWait`, which returns at
once when the schema is at head. The **api's `runRoot` runs `db.Migrate` before building**
(after boot probes, so `/readyz` reports `migrating`). With F6 this is declared: `migrate=apply`
on api, `migrate=wait` on worker/gateway. Forge's own `migratekit.AutoMigrate` path must
**not** run. reliant's config has no `auto_migrate`, so the template omits it; confirm this in
R3's generated `serve.go`.

### 4.4 Process globals and construction order
`drivers.Configure`, the model registry and the rest run in `runRoot` before Build (§1.6).
Globals that need the repo live in `ProcessRuntime`. That's the **last** root field, so it's
built first, and `Build` finishes the whole closure before any `Run` (graph_gen: "eager within a
root"), so no request can see them unset. A constructor that reads a model global while it's
being *constructed* would also be covered, because `ProcessRuntime` is built first. Pin this with
a test that `PlanX()[0] == "process_runtime"` (or whatever the first node is named).

### 4.5 Per-root node variants ("reach implies run")
These nodes are wired differently in api and worker: the daemon router (the automation-credential
fallback is **worker-only**, a privilege boundary), chat/run/trigger, and the tools factory.
They get distinct types (`APIDaemonRouter`, `WorkerDaemonRouter`, …). The design says identity
is the type, so this is by the book. Ambiguous consumers are resolved by owned compose
expressions. The Repo update notifiers and the GitHub access loop are split into their own nodes
so the gateway and worker don't start them. **Review rule for R3–R5:** every node with `Run`
that two roots reach must run in both today.

### 4.6 control-plane's `RELIANT_PROBES` and workload commands
`workloads.k:684-762` runs `["reliant","server","api"]`, `["reliant","server","worker"]`, and
the gateway image's ENTRYPOINT `reliant server gateway`. The dev env runs
`go run ./cmd/reliant server api|worker` (`dev/main.k:2793+`). `command=server,<role>` keeps
those strings. The flag and env surface is unchanged (R1 invariant). `HEALTH_PORT` stays
per-role with today's defaults. The probes stay as they are (F5). Nothing in control-plane
changes until R7.

### 4.7 Smaller risks
- **Flag double registration:** `roots_gen` calls `config.RegisterFlags(cmd)` on each root
  command, and reliant's `root.go` already registers `ReliantConfig` flags as persistent on the
  root. Verify in R3 that cobra doesn't panic on "flag redefined" and that `--help` is unchanged.
  If it's a problem, drop the persistent registration for the server subtree.
- **Help text:** roots_gen's Short/Long come from the root struct doc. Copy today's text
  verbatim; `help_text_test` pins it.
- **`forge:exclude-contract`** on `serverapi`: once the packages are deleted, check that forge
  doesn't start demanding `contract.go` for `servergateway` (it keeps exported types).

---

## 5. Verification per PR

| PR | Must pass |
|---|---|
| R1 | `go test ./cmd/reliant/...` (`*_config_test.go`, `help_text_test`, `command_surface_test`); a before/after diff of `reliant server {api,worker,gateway} --help`; `make forge-generate-check`; `forge lint`; boot each role on the `forge env up dev` stack (control-plane) and grep `.forge/logs/dev/reliant-*.log` for a clean start. |
| R2 | As R1, plus `forge generate` twice (the second run reports `nothing to do`), `go build ./...`, `make test-affected` (everything under `cmd/reliant`), `go test ./internal/workflow/workflowref/`. |
| R3 | `go test -race ./internal/app/... ./internal/grpc/... ./internal/servergateway/... ./internal/handlers/tools_daemon/...` (drain + rollout + audience); the new `PlanGateway` stop-order test and the registration-order test; `forge lint --graph` (gateway clean, the rest at warn). **Boot probe proof:** with NATS stopped, `reliant server gateway` returns `/health` 200 and `/ready` 503, naming the pending dependency. Start NATS and `/ready` turns 200. On SIGTERM, `/ready` is 503 `draining` until exit, and `/health` answers to the end. Log `drain:` phases in today's order. `forge env smoke dev` (`/flow-health`). |
| R4 | `go test -race ./internal/app/... ./internal/workersetup/... ./internal/serverworker/...`; the `PlanWorker` stop-order test; worker boot against an **un-migrated** DB waits (`MigrateWait`), then proceeds once the api migrates; `/ready` includes `temporal`. control-plane's `e2e/worker_canary` on the e2e env. |
| R5 | `go test -race ./internal/app/... ./internal/grpc/...` (server_drain, cors_web_client, public_procedures, forge_api_disabled, token_e2e, model_endpoint_e2e, localmodel_e2e, cleartext_http2); the `PlanAPI` stop-order test; an absent-loops test (no control plane, no webhook secret → no `key_sweep`/`github_access` in the supervised set); `TestServerAPIDoesNotBuildMCPManager` moved to the graph; the api migrates on boot and the worker and gateway don't; DB-backed packages with `DATABASE_URL` set to your own database (never 5434). |
| R6 | `forge lint` with the graph lane gating and no overrides; `make test-affected TEST_FULL=1`; control-plane `task ci` against this reliant pin (`reliant-vendored-proto`, `reliant-release-config` steps) and `task ci:k3d` (the k3d deploy smoke: the probes and commands are unchanged). |

---

## 6. Cross-repo: how control-plane consumes reliant's `ROOTS`. **Owner decision.**

What exists today: control-plane pins reliant as a **Go module** (`go.mod:36`,
`v1.8.3-0.2026…-f629da0d9cdc`), and `task pin:reliant` → `scripts/pin-sibling.sh reliant`
moves that pin together with the `reliant-web` `GitSource` ref in `prod/main.k`. control-plane's
`deploy/kcl/kcl.mod` has **no dependencies**. `reliant-vendored-proto` and
`reliant-release-config` are gate steps that run node scripts in **reliant's checkout**
(`../reliant` or `RELIANT_DIR`) and check control-plane's tree. Note the direction: reliant
vendors control-plane's protos, not the other way round. reliant is a single Go module
(no nested `go.mod`), so the module zip at the pin **contains `deploy/kcl/*.k`**. reliant has no
`deploy/kcl` today. Once the roots and binary_config land, forge emits `deploy/kcl/config_gen.k`
with `ROOTS`, `root_of` and `workloadEnvMap`.

| Option | How | For | Against |
|---|---|---|---|
| **A. Vendor at the pin (recommended)** | `pin-sibling.sh reliant` also copies `$(go list -m -f '{{.Dir}}' github.com/reliant-labs/reliant)/deploy/kcl/config_gen.k` into control-plane `deploy/kcl/third_party/reliant/`. A new gate step, `reliant-roots-kcl`, checks that the vendored copy is byte-equal to the pinned module's (it reads the module cache, so no sibling checkout is needed). `workloads.k` binds `reliant_api_server = fw.Workload{…} \| reliant.root_of("api")`, and its env comes from `reliant.workloadEnvMap(cfg, w)`. | **One pin**: the KCL is the same commit as the Go code and the image, which is the drift `pin-sibling.sh` exists to prevent. The render works offline and in the sandbox (no git fetch). It reuses the auth path that already works (`GOPRIVATE` + `go mod download`). The diff is reviewable in the pin PR. | A copied file. The vendored module imports forge's KCL lib, which then resolves at **control-plane's** forge pin, so it breaks if the two repos' forge pins skew across a KCL schema change. Mitigation: the gate also checks that the forge pins agree, or forge emits a lib-free data-only `roots_gen.k`. |
| B. `kcl.mod` git dependency | `[dependencies] reliant = {git = "https://github.com/reliant-labs/reliant", commit = "<sha>"}`, with `pin-sibling.sh` keeping `<sha>` equal to go.mod's | Native KCL. No copy. | A **second pin** of reliant, because KCL doesn't read go.mod. Every render does a private-repo git fetch (CI, `forge env render`, the sandbox), which needs credentials. It pulls the whole repo. It has the same forge-lib skew as A. |
| C. Status quo + assertion | control-plane keeps hand-typed `command`s and its 72 `forge.EnvVar`s. Add a gate test that every reliant workload's `command` is a root's args (from `reliant <cmd> --help` at the pin) | Nothing to build | You lose the least-privilege point of P2: the worker keeps receiving the api's secrets, and the env names drift silently. |

**Recommendation: A.** The parent's "reliant emits a KCL module, control-plane imports it at its
reliant pin" works, but only if "imports" means **vendor from the Go module cache at pin time**.
A `kcl.mod` dependency can't follow go.mod's pin. Two further points for the owner: (1) A only
wires `args` + env *names*. Turning control-plane's 72 hand `EnvVar`s into a typed reliant
config value per env is D4's larger step, and A is what unblocks it. (2) A is easier if forge
also emits a lib-free `roots_gen.k` (data only: args/env/secrets). That's a small forge
follow-up, worth filing alongside F-B.

---

## 7. Open questions
- Should the api gain the Temporal readiness check the worker has (§4.2)? Recommended: yes.
- In R2, keep `reliant metrics schema` (forge's `metrics.go`) or delete it? Recommended: keep it.
- The order between R5 and the P5b consumer-interface lanes is decided when R4 lands, based on
  how many lanes are still open.
