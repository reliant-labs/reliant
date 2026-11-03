---
name: observability
description: Observability — the local ClickStack (HyperDX) that forge env up runs by default, forge env status runtime checks, querying logs/traces/metrics, dashboards.
---

# Observability

Every Forge project ships a full observability stack for the dev loop: **ClickStack** (ClickHouse, the HyperDX UI and an OTLP collector) as the `clickstack` service in `docker-compose.yml`. Nothing to register, no key to copy. It is **on by default**: `forge env up dev` starts it and lists the **HyperDX UI** under **Compose services**.

It is one container (HyperDX's no-auth *local mode* image), about 0.8 GB resident. Every port is published on `127.0.0.1` only; ClickHouse is not published and runs behind a generated dev-only user, not the passwordless `default`.

## How the data gets there

| Signal | Path |
|---|---|
| Traces, metrics | Host processes started by `forge env up` get `OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<port>` and `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf` and push OTLP/HTTP to the collector. Compose services use `http://otel-collector:4318`. The app never learns the backend, only the standard variables. |
| Profiles | The runtime profiles itself and **pushes** OTLP profiles to `OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT`, which the env's declaration (`profiles_port`) points at the `otel-profiles` collector. See *Continuous profiles*. |
| Logs | The collector tails `.forge/logs/<env>/<service>.log` — the files `forge env up` already tees every host process into — parsing the runtime's JSON lines. `service.name` is the file name (the workload name); `trace_id` / `span_id` become the log's trace link. This is the **one** local log path (the runtime does not also export logs over OTLP). The pipeline is `deploy/observability/otel-collector.yaml` — yours to edit. |

A value you set yourself (shell, `config.k`, a workload's `env`) wins: `forge env up` fills `OTEL_EXPORTER_OTLP_ENDPOINT` and `_PROTOCOL` as a pair, only when neither is set. Frontend dev servers get the endpoint (for their `/_otel` proxy) and the env name; `frontend_*.log` is not tailed, as the browser SDK ships the console.

**To turn it off**, set `_observability = False` in `deploy/kcl/dev/main.k`: no container starts and no endpoint is exported. Trace context still propagates. Profiles go with it (see *Continuous profiles*).

**To send it somewhere else**, declare a destination: local ClickStack is just the default one. A `forge.Observability` on the Bundle names an OTLP endpoint, a headers *secret reference*, which signals, and sampling; forge renders the collector for the runtime (a compose forwarder locally, a node-agent DaemonSet on a cluster) and apps are unchanged. Depth: `forge skill load observability/destination`.

## forge env status — the runtime checks

Verify the entire observability pipeline is working:

```bash
forge env status dev                    # Table + every runtime check
forge env status dev --signal traces    # Check only traces
forge env status dev --signal metrics   # Check only metrics
forge env status dev --signal logs      # Check only logs
forge env status dev --signal profiles  # Check only pprof
forge env status dev --signal app       # Check only /healthz + /readyz
forge env status dev --json             # Machine-readable output
forge env status dev --verbose          # Show evidence for passing checks
```

Checks: compose infra, app health, pprof (and that the process answering it IS
the app), traces, metrics (gauge, sum and histogram) and logs from the last 15
minutes in ClickHouse, each naming the services that sent them, and Delve.

They read ClickHouse itself (`docker compose exec` into `clickstack`, with its
own credentials), so a pass means rows are in the store, not that a UI answers.
With ClickStack off they SKIP and say so.

**Runtime checks use `forge env status`, not `forge doctor`.** Status renders
the environment's KCL and overlays bound ports; doctor checks project structure
and takes no `--env`. Missing runtime evidence is **UNDETERMINED** (`?`), not
PASS or SKIP. Run status after `forge env up dev` before investigating.

**The runtime checks verify the telemetry pipeline, NOT app-flow correctness.** They (like `forge env smoke`) are green when containers, endpoints, and signal ingestion are healthy — they can be green while the actual app flow is broken (e.g. a cross-cluster dial failing). To prove an app-flow invariant holds, use a declarative, exit-coded app-health assertion (model: a project `doctor:<flow>` task) plus a full `task test:e2e`. They tell you observability works; they do not certify the app does.

## Accessing HyperDX

`forge env up dev` and `forge env status dev` list the **HyperDX UI** URL under **Compose services** (its port is declared once in `deploy/kcl/dev/main.k`, `_hyperdx_port`, default 8180). There is no sign-in. Three sources are pre-created — **Logs**, **Traces**, **Metrics** — so search works on first open.

### Dashboards

HyperDX dashboard JSON dropped into `deploy/observability/dashboards/` is loaded automatically, within about 15 seconds and without a restart (HyperDX's file provisioner, `DASHBOARD_PROVISIONER_DIR`). A provisioned dashboard is read-only in the UI and replaced by name on the next load: edit the file, not the browser copy. A dashboard you build by hand in the UI is separate and never overwritten. `forge generate` writes an overview, a dashboard per deployed process/role and a frontend dashboard into it as `*_gen.json`; every other file there is yours. Depth, and how to override a generated one: `observability/dashboards`.

**Alerts** are declared in KCL, not in HyperDX: `forge.AlertRule` (a native ClickHouse SQL `expr`; every returned row is a firing instance), `forge.AlertRoute` (labels to a receiver name) and `forge.AlertDefault` (tune or disable the generated defaults: telemetry, customer-metadata and platform), under `forge.Observability.alerts`. Render refuses a malformed rule; the resolved rules land in the release shape for the control plane to evaluate. Depth: `observability/alerts`. Uptime checks of a hosted env: `observability/uptime`.

## Querying Logs

In HyperDX → Search → Logs, filter on the service (the workload name), severity, or any attribute the log line carried:

```
ServiceName:api SeverityText:error
ServiceName:api LogAttributes.procedure:"/services.users.v1.UsersService/Create"
TraceId:abc123
```

Logs are structured JSON with consistent attribute keys (`procedure`, `request_id`, `trace_id`, `duration_ms`, `user_id`, `status`, `code`, `error_class`). Emit the same attribute keys from your own log sites so dashboards stay queryable. A line that is not JSON (air's rebuild output, a panic) is kept as plain text under the same service.

Or ask from the shell, one JSON document per command (`forge obs status`, `issues`, `logs`, `trace`, `endpoints`, `query --sql`; local ClickStack or, hosted, the control plane). Depth: `observability/query`.

```bash
forge obs logs dev --service api --severity error --json
```

## Error levels: who has to act

A log level is a routing decision: ERROR is what alerts match, what a Sentry
forwarder sends, what a post-deploy log gate fails on. So forge sets the level
of every error record from **fault attribution** — `svcerr.Classify(err)` —
not from how the call site spelled its log line:

| `error_class` | what it means | example | level | pages (ERROR → Sentry / alerts) |
|---|---|---|---|---|
| `server` | our system failed, or cannot tell that it did not | Internal, Unknown, DataLoss, Unavailable, any unrecognised error | ERROR | yes |
| `server` | server-side, but "retry", not "page" | DeadlineExceeded, Aborted, Unimplemented | WARN | no |
| `user` | the caller or user must act; the system behaved correctly | InvalidArgument, NotFound, AlreadyExists, PermissionDenied, Unauthenticated, FailedPrecondition (InsufficientBalance, Expired), OutOfRange, ResourceExhausted (PlanLimit), or `svcerr.WithClass(err, svcerr.ClassUser)` | INFO (`observe.WithUserErrorLevel`) | no |
| `canceled` | one end went away: the caller, or this replica draining for a rollout | `context.Canceled`, CodeCanceled, `svcerr.ErrServerDraining` / `svcerr.Draining(…)` | same as `user` | no |

A drain is also an **expected outcome** in every logging layer without being
declared: a call this replica ended because it is shutting down (a stream's
`Unavailable("gateway draining")`) is written like a success, with
`expected=true`, never at ERROR. See `services/shutdown`.

Nothing is hidden: a user error is still written, with `error`,
`error_class=user` and `code`, at a level production keeps — it just stops
claiming to be an incident. Its volume stays countable: the RPC error counter
(and `<pkg>.errors` on the component chain) carries `error_class` and `code`.

The policy holds at all three places a record comes from:

1. `rpc failed` / `stream failed` at the RPC edge (`observe.LoggingInterceptor`);
2. component failures on the in-process chain (`observe.LogMiddleware`);
3. **every hand-written `logger.Error`** — `observe.NewErrorClassHandler`
   wraps the process's slog handler (serverkit's `NewLogger` already does) and
   lowers a record carrying a non-server error to the user-error level, adding
   `error_class`. It only ever lowers; it never raises a WARN to ERROR.

**If you forward logs to Sentry (or count them), wrap
`observe.NewErrorClassHandler` OUTSIDE that handler**, so it sees the
classified level — the forwarder keeps gating on `level >= ERROR` and never
needs to know about svcerr:

```go
handler := sentryHandler(serverkit.NewLogger(cfg).Handler()) // forwards >= ERROR
logger := slog.New(observe.NewErrorClassHandler(handler))
```

Marking: most user errors need no marker — return the right `svcerr` kind
(`svcerr.FailedPrecondition`, `svcerr.PlanLimit`, …). Use
`svcerr.WithClass(err, svcerr.ClassUser)` when the CODE and the FAULT disagree
(`Unavailable` is the honest code for "the user's machine is offline", and a
server fault by kind), or when an error never crosses an RPC boundary (a worker
logging a provider's "credential rejected"). `svcerr.WithClass(err,
svcerr.ClassServer)` is the reverse: a 4xx the client must see that is really
our bug. A library error svcerr cannot see into — a workflow engine's
application error that crossed a serialization boundary — is taught with
`observe.WithErrorClassifier(func(error) svcerr.Class)` (return
`svcerr.ClassNone` for "no opinion").

Server faults carry a bounded stack: `observability/error-stacks`.

## Querying Traces

In HyperDX → Search → Traces, filter by:
- Service name (the workload name)
- Trace ID (from log lines — a log with a `TraceId` links to its trace)
- Duration range
- Status code

Trace IDs are injected into every log line written with a span-bearing context, connecting logs to traces.

## The environment contract

Apps never learn the collector's backend. The platform renders the standard OpenTelemetry variables, and `forge/pkg/observe` obeys them:

| Variable | Meaning |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | base URL of the collector (host processes `http://127.0.0.1:4318`) |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` (default, 4318) or `grpc` (4317) |
| `OTEL_EXPORTER_OTLP_HEADERS` | `k=v,k2=v2`, e.g. `authorization=<key>`; per-signal `OTEL_EXPORTER_OTLP_{TRACES,METRICS,LOGS}_{ENDPOINT,PROTOCOL,HEADERS}` override it |
| `OTEL_EXPORTER_OTLP_HEADERS_FILE` | path to a file in the same `k=v,k2=v2` format (a newline also separates pairs), **re-read while the process runs**; per-signal `OTEL_EXPORTER_OTLP_<SIGNAL>_HEADERS_FILE` overrides it. See "Rotating a credential" below |
| `OTEL_SERVICE_NAME` | wins over the compiled-in service name (the caller's default is only a fallback) |
| `OTEL_RESOURCE_ATTRIBUTES` | merged into the resource; carries `deployment.environment.name=<env>` |
| `OTEL_SDK_DISABLED=true` | every export off |
| `OTEL_LOGS_EXPORTER=otlp` | the explicit switch for OTLP logs (off by default) |
| `OTEL_PROFILES_EXPORTER=none` | turns continuous profiling off (the runtime profiles whenever an endpoint is set); the declaration renders it wherever profiles are off (`observability/profiles`); typed equivalent `observe.Config.ProfilesDisabled` |
| `FORGE_PROFILES` / `FORGE_PROFILES_HEAP_INTERVAL` / `FORGE_PROFILES_CPU_INTERVAL` / `FORGE_PROFILES_CPU_DURATION` | which profiles are captured (`cpu,heap` by default; `alloc`, `goroutine`, `mutex`, `block` are opt-in; `none` for nothing), the heap cadence (`2h` by default), the capture cycle (`5m`, at least `30s`) and the CPU window (`5s`, at most half the cycle). A workload's `profiling` block renders them; typed equivalent `observe.Config.Profiles`, which they win over (`observability/profiles`) |
| `OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT` | where profiles go: a FULL URL used as given (`http://127.0.0.1:4328/v1development/profiles`). Unset, profiles follow the base endpoint and gain `/v1development/profiles`. Headers, protocol, timeout and compression follow the per-signal `OTEL_EXPORTER_OTLP_PROFILES_*` variables |

`Config.OTLPEndpoint` (the typed `otlp_endpoint` config field) overrides the endpoint variables; protocol and headers always come from the environment. With no endpoint nothing is exported, but `/metrics` still works and the W3C TraceContext+Baggage propagator is **always** installed, so `traceparent` is honoured and forwarded regardless. With export on, Go runtime metrics are recorded too.

### Rotating a credential without a restart

`OTEL_EXPORTER_OTLP_HEADERS` is read once, so a token that gets renewed would need a pod restart. Point `OTEL_EXPORTER_OTLP_HEADERS_FILE` at a mounted Secret instead (a Secret **volume**, never `subPath`, which the kubelet never refreshes; the file holds e.g. `authorization=Bearer <token>`). All four signals (traces, metrics, logs, profiles; HTTP and gRPC) take their headers from it **per request**:

- The file is re-stat'ed at most every 5s (one stat per interval per process, shared by all signals) and re-read only when it changed. There is no watcher goroutine.
- **Precedence:** a header named in `OTEL_EXPORTER_OTLP_HEADERS` (or the signal's own `…_<SIGNAL>_HEADERS`, which replaces the general one for that signal) is never overridden by the file; the file supplies the rest. Pick one place for the credential.
- A missing, unreadable or empty file means the export goes out **without** the file's headers (the collector decides whether that is acceptable) and logs one `WARN` per minute. It never fails a call, blocks the app or crashes. A removed file stops sending its last token. A file that appears later is picked up.
- Rotate with overlap: have the receiver accept old and new, swap the file, then drop the old. The kubelet needs about a minute to refresh a Secret volume.

On a cluster, the node agent does the same for the **destination** with `destination.token_secret` (below): `bearertokenauth{filename}` reads the mounted token and re-reads it on change (verified against contrib 0.162 with a kubelet-style symlink swap, in `pkg/observe`'s `RUN_HEADERS_FILE_SMOKE=1` integration test).

### Logs: exactly one shipping path

The server writes JSON lines to stdout, each with the process `resource` (`observability/destination`); `InfoContext` with a span adds `trace_id`/`span_id`. Locally a collector reads the `.forge/logs/<env>/*.log` files; on a cluster a node agent reads container logs. The OTLP logs exporter (`OTEL_LOGS_EXPORTER=otlp`) does not replace stdout and is **mutually exclusive with file/stdout collection** — turn it on only where no agent collects the same stream, or every line ships twice.


## Continuous profiles

Every process with an OTLP endpoint profiles itself (`pkg/observe`): a 5s CPU profile every 5 minutes and a live-heap snapshot every two hours (allocation, goroutine, mutex and block profiles are opt-in per workload), **pushed** over OTLP to the `otel-profiles` collector, which writes them into ClickStack's ClickHouse. On by default with the local ClickStack; elsewhere `signals.profiles` on the env's `forge.Observability` governs it. HyperDX draws no flamegraphs: `forge obs profile --service api -o api.pb.gz`, then `go tool pprof -http=: api.pb.gz`. Depth (defaults, cost, tables, trace links, SQL): `forge skill load observability/profiles`.

## Resource identity

`service.name`, `service.version` and `service.instance.id` come from `observe.Config`. Everything else on the OpenTelemetry resource comes from the standard `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` environment variables, which the SDK merges in (`resource.WithFromEnv`) and which win over the `Config` values. The deployment environment is the **forge env name** (`dev`, `staging`, `prod`): the platform renders it as `OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=<env>`. It is deliberately not the app's `environment` config field, which is a CORS/HSTS mode switch and reads `production` in both staging and prod.

## Querying Metrics

In HyperDX → Search → Metrics (or a chart tile), pick the metric by name. The RPC edge is measured by the otelconnect server interceptor: `rpc.server.call.duration` (histogram, attributes `rpc.method` — the full `pkg.Service/Method` — and `rpc.response.status_code`, `OK` or the upper-case Connect code such as `NOT_FOUND`). Other series the app exports: `db.sql.*` / `go.sql.*` for the DB pool and queries, and `<pkg>_calls` / `<pkg>_errors` / `<pkg>_duration` for the per-package in-process method metrics (component chain). Every series carries `ServiceName`.

**Declare your own metrics** in `internal/metrics/metrics.go`: each instrument is declared (name, type, UCUM unit, description, attribute keys, stability, `since`) and the registry returns the standard OTel instrument. An attribute you did not declare panics under `ENVIRONMENT=development` and is dropped (counted on `forge.metrics.rejected_attributes`) elsewhere. The declared set prints with `<bin> metrics schema --json`, `GET /debug/metrics/schema` (the pprof listener) and `forge obs metrics declared [--binary|--url]`; `forge build` writes `bin/observability/metrics-schema.json`, which a release carries as `observability/metrics-schema.json`; `forge obs metrics declared --dashboard` draws the Custom metrics dashboard from it. Adopting it in an older project: `migrations/v0.1.45/metrics-schema`.

## In-process component observability (the middleware chain)

Forge instruments three boundaries: the RPC edge (the interceptor chain built
by `serverkit.NewChain` in `cmd/<bin>/cmd/serve.go`: one span, one metric sample and
one log record per RPC), every internal component→component call (a
generated `middleware_gen.go` decorator routed through the owned
`observe_chain.go` seam), and the ORM (`pkg/orm` registers the `bunotel` hook,
so each query is a child span). Every contract package is decorated by default.
Success-log sampling and opting a component out: `observability/component-chain`.

## Audit log

Every RPC already writes a structured audit record to your logs (`log_type=audit`, message `audit.event`, from `fmw.AuditInterceptor`, the default `Audit` slot of `serverkit.NewChain`). For a DB-persisted audit trail you can query and page through (a compliance record, an admin `ListAuditEvents` RPC) on `forge/pkg/audit`: `observability/audit-log`.

## Outbound instrumentation

- **Plain HTTP downstreams**: pass `infra.DefaultClient()` (providers.go) as
  the adapter's `Deps.HTTPClient` — a fresh client over the shared
  otelhttp-instrumented transport (client spans + W3C propagation + 30s
  timeout). The compose site wires any `HTTPClient *http.Client` Deps field
  to it automatically. If your providers.go predates `DefaultClient`, add the
  method + the `httpBase = otelhttp.NewTransport(http.DefaultTransport)`
  line from a fresh scaffold.
- **Connect clients** (a service split out of the binary): build the stack
  with `observe.NewClientStack` (forge/pkg/observe) — otelconnect client
  interceptor, request-ID forwarding, timeout — then
  `genconnect.NewXClient(stack.HTTPClient, baseURL, stack.ClientOptions...)`.

## Keeping a request in one trace

`forge lint` gates on the places app code cuts a trace: a log call that drops
the `ctx` in scope, a raw `*http.Client` or `sql.Open`, a contract built
without its decorator. `forge lint --tracing` runs them alone, and with
`--fix` rewrites the log calls and the constructions: `observability/tracing-lint`.

## Opting out (hosted observability is opt-out)

Observability is ON by default for every deployed env, and you can switch off
all of it or any category. **Disabled data is never collected or stored, so it is
never billed.** (Daemons are not metered for observability; deployments are.)

```kcl
observability = forge.Observability {enabled = False}        # everything

observability = forge.Observability {                        # per category
    signals = forge.ObservabilitySignals {logs = False, metadata = False}
}
```

| Category (`signals.<x>`) | What stops |
|---|---|
| `traces`, `metrics`, `logs`, `profiles` | the app's SDK exporter: `OTEL_<SIGNAL>_EXPORTER=none` |
| `metadata` | **customer metadata**, collected by the platform's node agent without the app's help: pod CPU/memory against limits, restarts and last exit reason, Kubernetes events, image and version |
| `browser` | the frontend SDK: `OTEL_ENDPOINT=off` in every frontend's runtime config (and its build variable), over anything you set |

**Session replay is the one opt-IN category** (`signals.replay`, default `False`). The
browser SDK records DOM events only when the frontend is built with
`NEXT_PUBLIC_OTEL_REPLAY` / `VITE_OTEL_REPLAY`, and the platform drops them (browser logs of
scope `rum.rr-web`) unless `replay = True`. It is recorded in the shape as
`observability_policy.opt_in = ["replay"]`.

`enabled = False` renders `OTEL_SDK_DISABLED=true` on every workload plus the
browser switch. Author-set `OTEL_*` values still win.

The resolved opt-out is recorded in the release shape as
`observability_policy: {enabled, disabled: [...]}` (omitted when you opt out of
nothing). The platform enforces it **at the source**: it labels the env's
namespace `reliant.dev/obs-disabled: "logs,metadata"`, the node agent drops
those namespaces' logs and customer metadata before export, the operator injects
no OTLP endpoint or token when everything is off, and `/_otel` answers 204
without forwarding. Re-enabling takes effect on the next deploy.

## Rules

- Run `forge env status dev` after `forge env up dev` to verify observability before investigating issues.
- The HyperDX port is declared in `deploy/kcl/dev/main.k` and listed by `forge env up dev` / `forge env status dev`.
- `deploy/observability/otel-collector.yaml` is yours: forge scaffolds it once and never rewrites it. In `deploy/observability/dashboards/` only `*_gen.json` and `*_gen.ndjson` are forge's; everything else there is yours.
- Use the `logevents.go` helpers for structured log events — do not create ad-hoc attribute keys.
- Trace IDs propagate automatically via OpenTelemetry context — no manual instrumentation needed.
