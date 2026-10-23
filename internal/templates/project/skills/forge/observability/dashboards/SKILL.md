---
name: observability/dashboards
description: The dashboards forge generates from your services (overview, per-service RED per rpc, frontend), the HyperDX files and the renderer-neutral spec they come from, how to override one, why alerts are not provisioned, and how a deliverer binds source names.
---

# Observability: generated dashboards

`forge generate` (step "observability dashboards") derives dashboards from the
project's own metadata: the proto services and rpcs, the frontends and whether a
database exists. It runs for service projects with observability on and at least
one service.

| File                                                          | What                                                                                                                                                                                             |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `deploy/observability/dashboards/<project>-overview_gen.json` | requests, server errors, p95/p99, request rate and error rate per service, RED table, top errors, DB latency, recent error logs                                                                  |
| `.../<project>-service-<svc>_gen.json`                        | per service: a requests / error-rate / latency row for EVERY rpc, a RED-by-rpc table, DB calls, Go runtime (memory, GC goal, goroutines, allocation), error logs and logs scoped to that service |
| `.../<project>-frontend_gen.json`                             | with a web frontend: JS errors, sessions, page views, failed API calls, LCP/INP/CLS p75, page-load percentiles, browser API latency and calls by URL                                             |
| `.../<project>-service-<svc>_gen.ndjson`                      | per service: its error-log saved search                                                                                                                                                          |
| `deploy/observability/spec/*_gen.json`                        | the renderer-neutral model all of the above come from: `project_gen.json` and one `service-<svc>_gen.json` per service                                                                           |

Every per-service output is its own file, so two branches that each add a
service merge without a conflict, and the merge is already what `forge generate`
writes.

Every dashboard has `service.name` and `deployment.environment.name` filters.

## What the panels match

Panels are keyed on what the runtime really emits, not on a convention:

- RPC edge: the otelconnect server interceptor opens a **server** span named
  `<proto package>.<Service>/<Method>` (no leading slash), e.g.
  `orders.v1.OrdersService/GetOrder`. Status is `Error` only for server faults
  (Internal, Unknown, Unavailable, DeadlineExceeded, Unimplemented); a client
  error such as NotFound is not an error span, so the error rate is a **server**
  error rate.
- Log lines carry `procedure` WITH a leading slash, which is how a service's
  logs are scoped (`LogAttributes['procedure']`).
- DB: the ORM's bun hook opens client spans with `db.system` and `db.operation`.
- Runtime: `go.memory.used`, `go.memory.gc.goal`, `go.memory.allocated`,
  `go.goroutine.count`. These are per PROCESS, and one process serves every
  service, so on a service dashboard narrow with the `service.name` filter.
- Browser: spans from the HyperDX SDK carry the `rum.sessionId` resource
  attribute; `documentLoad`, `webvitals` and fetch/XHR spans (`component`).
  An uncaught error, an unhandled rejection and `console.error(err)` are
  **error spans** (`onerror`, `unhandledrejection`, `console.error`) with an
  `exception` event; `console.error("text")` is an error span with **no**
  exception event, so it is searchable but is neither an issue nor counted by
  `FrontendJSErrorSpike`. `webvitals` spans carry one metric each, as an
  attribute named for it (`lcp`, `inp`, `cls`, `fcp`, `ttfb`).

## Panel kinds

`number`, `line`, `table` and `search` are queries and draw anywhere. Three
more are drawn **only by the Reliant console**, from its own services rather
than a query over series, so they take no `series`, `columns` or `groupBy` and
read browser spans (`signal: traces`):

| kind | extra field | draws |
|---|---|---|
| `issues` | | grouped errors, new and regressed first |
| `sessions` | `hasError: true` narrows to sessions that hit an error | browser sessions, with a replay link when one was recorded |
| `funnel` | `funnel: {steps: [event, ...], window: "1h", by: "session"}` | product events (`track()` names) in order; 2 to 10 distinct steps, a window like `30m` / `1h` / `7d`, counted per `session` or `user` |

The HyperDX renderer **skips** them (HyperDX's own UI already plays replay and
searches spans), so a local `forge env up` stack shows the frontend dashboard
without them.

Three more kinds, and one field, are queries the console draws and HyperDX
has no equivalent for, so HyperDX skips them too:

| kind / field | takes | draws |
|---|---|---|
| `bar` | `groupBy` (required), `series`, `limit` (default 10) | top-N bars: one group per `groupBy` value, the `limit` largest by the first series |
| `heatmap` | one series: `agg: count` plus a numeric `field` (`span.duration`); `signal` traces or logs | time across, value buckets up, each cell the number of events in it |
| `sql` | `sql`: one read-only SELECT; no `series`, `where` or `groupBy`; `signal` optional | the rows; a first `DateTime` column draws a time series |
| `formula` on `line` / `number` | `"A / B * 100"`: series by position (A, B, ... J), numbers, `+ - * /`, parentheses | that one expression instead of the series; `x / 0` is 0, as in `ratio` |

`limit` (1 to 200) also keeps the top rows of a `table`. A `sql` statement runs
under the alert-rule guard and limits (one SELECT; no writes, DDL, `SETTINGS`,
`FORMAT`, `system.*` or outside table functions; a timeout and a row cap) as the
viewer's own read-only database user, and binds the dashboard's scope as
ClickHouse parameters: `{from:DateTime64(9)}`, `{to:DateTime64(9)}`,
`{step:UInt32}`, `{env_ids:Array(String)}`, `{envs:Array(String)}`,
`{services:Array(String)}`.

A `table` or `number` series may carry `thresholds: {good, poor}`
to grade its value like Core Web Vitals do; renderers that cannot colour ignore
it. The frontend dashboard's **Web vitals by route** table uses it for the p75
of LCP, INP and CLS, and **JS errors by release** splits the error spans by
`service.version` (the build id).

## Product events

A **product event** is a named thing a user did or that happened to them
(`project_created`, `subscription_started`). On the hosted platform they land in
the per-environment ClickHouse `product_events` table, which keeps them far
longer than logs or traces. Dashboards read it with `sql` and `funnel` panels.

### The contract

An event is one OTel **log record** at INFO (never a span: server traces are
sampled) carrying these attributes. This is the seam between the emitters and the
table; all of them follow it.

| attribute | meaning |
|---|---|
| `event.name` | `product.<snake_name>`; required, and the only thing the table selects on |
| `user.id` | the authenticated user id (the auth subject); empty when anonymous |
| `visitor.id` | the anonymous visitor id, when known |
| `session.id` | the client session id, when known |
| `app.client` | `backend`, `web`, `electron` or `landing` |
| `app.version` | the emitting app's version |
| `product.<key>` | one property per key; stored in `Props` with the `product.` prefix stripped |

Bounds, enforced by both emitters and again by the table's view: at most 32
properties, each value at most 256 characters (longer is truncated). Product
events are not a way to keep logs longer. Browser events keep their existing
path (HyperDX `custom-action` spans into the same table); the web runtime
stamps `app.client` and `visitor.id` on them.

### Emitting from Go

```go
observe.Track(ctx, "project_created", slog.String("template", "go"), slog.Int("repos", 2))
```

`observe.Track(ctx, name, attrs...)` writes the record through the context's
logger (`observe.FromContext`), sets `app.client="backend"` and `app.version`
(from `observe.Config.ServiceVersion`, or `observe.SetAppVersion`), and takes
`user.id` from the claims forge's auth interceptor put in `ctx`: a handler
needs no extra step, a background job has no user and so emits an anonymous
event. A name that is not snake_case (`^[a-z][a-z0-9_]{0,63}$`) is dropped with
a warning. Property values become strings; keep them low-cardinality and
never put secrets or personal data in them. A `slog.Group` flattens to
`group.key`.

### Emitting from the browser

```ts
import { track, setTelemetryUser, withAttribution } from "@reliantlabs/forge-web-runtime";

track("onboarding_completed", { plan: "pro" });   // after initBrowserTelemetry
setTelemetryUser(session.user.id);                // also emits user_identified, once per user
```

`initBrowserTelemetry({ appClient })` stamps `app.client` (`electron` inside
Electron, else `web`; pass `landing` from a marketing site) and `visitor.id` on
every event, and reads `rid` and `utm_source|medium|campaign|term|content` from
the URL into storage. The first `setTelemetryUser(id)` for a user emits one
`user_identified {visitor_id, utm_*}`, which is how a funnel joins a visitor to a
user. A marketing site wraps its outbound links in `withAttribution(url)` so the
app sees the `rid` and utm values. All of it is behind the same consent gate as
`track`: nothing is stored or appended until `initBrowserTelemetry` runs, and
not after `disableTelemetry()`.

### Retention

`forge.Observability.retention.product_events_days` (1 to 400, default 90) is
how long the environment asks the platform to keep product events. The platform
may grant less (a plan can cap it); a new value applies to rows written after
it, not to rows already stored.

### Panels over `product_events`

Columns: `Timestamp`, `ServiceName`, `EnvId`, `SessionId`, `UserId`, `EventName`
(the name without the `product.` prefix) and `Props` (a map, `Props['plan']`).
`DESCRIBE product_events` lists what else the table keeps. A `sql` panel binds
the dashboard scope (`{from:DateTime64(9)}`, `{to:DateTime64(9)}`,
`{step:UInt32}`, `{envs:Array(String)}`); a first `DateTime` column draws a time
series.

Daily active users:

```json
{
  "id": "dau",
  "title": "Daily active users",
  "kind": "sql",
  "sql": "SELECT toStartOfDay(Timestamp) AS day, uniqExact(UserId) AS users FROM product_events WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} AND UserId != '' GROUP BY day ORDER BY day"
}
```

Events by name, and a property breakdown (swap in a property you emit):

```json
{
  "id": "events-by-name",
  "title": "Events by name",
  "kind": "sql",
  "sql": "SELECT EventName, count() AS events, uniqExact(UserId) AS users FROM product_events WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} GROUP BY EventName ORDER BY events DESC LIMIT 50"
}
```

```json
{
  "id": "subscriptions-by-plan",
  "title": "Subscriptions started by plan",
  "kind": "sql",
  "sql": "SELECT toStartOfDay(Timestamp) AS day, Props['plan'] AS plan, count() AS started FROM product_events WHERE EventName = 'subscription_started' AND Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} GROUP BY day, plan ORDER BY day"
}
```

A funnel counted per user:

```json
{
  "id": "activation",
  "title": "Activation funnel",
  "kind": "funnel",
  "signal": "traces",
  "funnel": { "steps": ["user_identified", "project_created", "onboarding_completed"], "window": "7d", "by": "user" }
}
```

Put them in a dashboard file you own (`deploy/observability/dashboards/<name>.json`,
see Overriding); `forge obs dashboards check` validates it.

## Overriding

Never edit a `*_gen.json`: forge rewrites it and refuses a hand-edit as drift.
Copy `x_gen.json` to `x.json`, give it a new `name`, edit it. While `x.json`
exists forge stops writing (and removes a pristine) `x_gen.json`. Any other
`*.json` in the directory is yours and is never touched.

## Dashboards in the release

`forge env build <env> --release <v>` puts the dashboards into the release bundle, so the Reliant console shows the set that belongs to the release that is running (and a rollback shows the older one):

| In the bundle                                      | What                                                                                                                        |
| -------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `observability/dashboards/<project>-<id>_gen.json` | each dashboard of the spec, plus `<project>-custom-metrics_gen.json` drawn from the build's metrics schema                   |
| `observability/dashboards/<name>.json`             | a dashboard you own                                                                                                         |
| `observability/metrics-schema.json`                | the schema, `forge.metrics/v1`                                                                                              |

They ride in the manifests layer, outside every `manifests/<cluster>/` tree, so nothing is applied from them.

A file you own is one dashboard object in the spec vocabulary (an `id`, a `title`, `filters`, `annotations` and a `panels` array; the shape of a `dashboard` in `spec/service-<svc>_gen.json`). Name it like a generated dashboard to replace that one: `shop-overview.json` replaces `shop-overview_gen.json`. A file with `tiles` is HyperDX's format; it is for the local stack, ships nowhere and replaces nothing. Ad-hoc console edits are saved as code by writing the dashboard JSON here and committing it.

Every file is validated at the cut, and `forge obs dashboards check` runs the same checks and lists what would ship. Errors read `deploy/observability/dashboards/x.json:12:7: panels[2].kind: "cube" is not one of: number, line, table, search, bar, heatmap, sql, issues, sessions, funnel`.

`annotations: [{"source": "releases"}]` on a dashboard asks the console to draw a marker for every promotion on its time-series panels. Generated dashboards declare it.

The _Workloads_ dashboard (CPU/memory against requests and limits, restarts and exit reason, image versions, Kubernetes events) and the _Releases_ dashboard (request, error and latency by `service.version`) are generated for every project. They read node-derived and release data that a local `forge env up` does not have, so they ship in the spec and the bundle and are not written to `deploy/observability/dashboards/`.

## Alerts

Dashboards embed no alerts (HyperDX 2.40 validates an alert declared in a provisioned file and never evaluates it, hyperdx PR #3233). Alerting is declared in KCL instead: `forge.AlertRule`, with generated defaults (telemetry, customer-metadata and platform) in `alert-defaults_gen.json`, and each service's entry in its `spec/service-<svc>_gen.json`. Depth: `observability/alerts`.

## The seam: spec, then renderer

`internal/obsdash` builds a `Spec` (dashboards > panels > series with a signal,
an aggregate and predicates on OpenTelemetry field names; no ClickHouse or
HyperDX names) and renders it. `RenderHyperDX` is one renderer; the Reliant
console draws the same Spec with uPlot. A hosted deliverer pushes the rendered
JSON through the HyperDX API.

Tiles name their source (`Logs`, `Traces`, `Metrics`). The file provisioner
stores that verbatim but the UI resolves sources by random per-database id, so
the delivery step swaps names for ids: locally the compose entrypoint does it
(and upserts the saved searches); through the API, use `GET /api/v2/sources`.
