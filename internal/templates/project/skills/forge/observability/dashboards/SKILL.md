---
name: observability/dashboards
description: Process/role dashboards derived from deployed executable and composition commands, metric-backed overview, renderer-neutral sections and targeted filters, safe retirement and overrides, and HyperDX delivery.
---

# Observability: generated dashboards

Every dashboard declares a `group` (overview, services, jobs, product, platform, releases or custom) and optional `tags`, which only a list view reads; `replaces` lists the ids a renamed generated dashboard supersedes, so a reader can carry stars and copies over. A process dashboard is named for the forge workload (`admin-api`), never the rendered Job/command/hash. An env that deploys no process (a static site) gets only its frontend dashboard, and the frontend dashboard follows the frontends that env serves.

`forge generate` writes reusable RPC, frontend and metric dashboard fragments
and alert inputs without claiming process ownership. It scopes the overview to
the service.names and workloads every environment that renders declares (the
same derivation a bundle uses), so the local stack's overview draws your
processes; an environment that does not render is skipped, so a newly scaffolded
operator can still generate while prod's cluster is unbound. The local overview
(`<project>-overview.json`) is yours once written, but while it is still the
untouched render of "nothing deployed" (every panel on
`__forge_no_deployed_process__`) the next generate that finds workloads
replaces it; an edited one is never touched.

`forge env render <env>` validates, and `forge env build <env>` assembles the
process dashboards from the selected environment's already-rendered
`KCLEntities.Workloads`. The exact executable, command/args and resolved
`OTEL_SERVICE_NAME` include environment-only workloads and aliases. No source
`ALL` inventory, AST workload guesses, second render or other-environment validity
is involved. API, worker and gateway roles of one executable remain separate;
workers with no RPCs get runtime/workload panels. Composition route sets and typed
mounts establish RPC ownership, not proto package names.

Per-process assembly is pure and environment-specific. It writes no global process
parts and retires nothing on a failed render. Each release bundle contains exactly
its selected environment's dashboard set, with owned process overrides applied by
stable ID/filename. Old disk-generated process parts never contaminate that set.

| File                                                          | What                                                                                                                                                                                             |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `deploy/observability/dashboards/<project>-overview_gen.json` | Compact unsampled request/error counters and duration histogram health; process comparison, saturation, errors and releases |
| `.../<project>-process-<workload>_gen.json` | One long-running service, worker or operator, named for its forge workload. Panels follow what it does: RPC health only if it serves RPCs, worker runs for workers and operators, database when reached, a `*_limit_utilization` panel only for a declared limit, runtime and logs always |
| `.../<project>-frontend_gen.json`                             | with a web frontend: JS errors, sessions, page views, failed API calls, LCP/INP/CLS p75, page-load percentiles, browser API latency and calls by URL — scoped to the web frontends' `service.name`s (`<project>-<frontend>`, otel_gen.ts's `SERVICE_NAME`; a frontend built from another repository (`source`) bakes that repository's name, so declare it: `telemetry = forge.BrowserTelemetry {service_name = "shop-web"}`) |
| `.../<project>-process-<workload>_gen.ndjson` | Process error-log saved search |
| `.../<project>-jobs_gen.json` (bundle only) | ONE dashboard for every job and cron job: runs (one per Kubernetes Job execution, however many pods it created; a Job recreated under the same name counts again), failures, duration and last run per job from the Kubernetes Job events, restarts, warnings, error logs. Jobs get no dashboard of their own |
| `deploy/observability/spec/*_gen.json` | `project_gen.json`, one `process-<workload>_gen.json` per long-running process and independent `alert-service-<svc>_gen.json` inputs |

A process's aliases across environments share a dashboard. Obsolete generated
service dashboards, spec parts and saved searches retire only when their bytes
still match the recorded checksum. Hand-owned overrides and modified generated
files stay on disk; workspace copies are not generator outputs.

Every dashboard has `service.name` and `deployment.environment.name` filters.
Process RPC procedure filters target metric RPC panels only; RPC service/method
filters target trace drilldowns. Runtime and workload panels are unaffected.
Metrics carry the canonical full `procedure`, not invented RPC attributes.

Optional generic viewer metadata: `sections: [{id, title}]` on a dashboard,
`section` on panels, and `panelIds` on filters. Empty/absent targets retain the
original global behavior; every non-empty target and section must be declared.
Sections are presentation groups, not authorization or implicit filter scope.
HyperDX cannot target a filter to a subset of tiles, so it omits those dropdowns
rather than silently applying them globally.

Optional viewer defaults: `defaultRange` on a dashboard (`"7d"`: a number and
a unit of s, m, h or d, at most 30d) is the range it opens on when the viewer
picked none. A `sql` panel whose statement groups by a fixed bucket
(`toDate`, `toStartOfWeek`) rather than by `{step}` declares `minStep` (`"1d"`):
a range shorter than two such buckets draws one point, so the console widens
that panel's range to hold two and says so. Prefer `{step}` in the statement
when the panel can follow the range.

`scope` on a dashboard is one of `"project"` (the default), `"org"` or `"platform"`. `project` reads the environments selected within the route's project. `org` reads every project of the organization, still narrowed to the selected environments. `platform` says the signals are platform or infrastructure telemetry that belongs to no environment (a database, a message bus, a collector): it carries no `reliant.env.id`, so an environment filter would match nothing, and a reader drops that one predicate and nothing else. The scope is a statement about the data, not an access grant; who may read it, and from which organization's data, stays the reader's decision. Most projects never set it.

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
- Health: `rpc.server.calls`, `rpc.server.errors` (server error class only),
  `rpc.server.duration` (histogram seconds). These are unsampled; trace tables
  are drilldowns, not authoritative request totals.
- Runtime: `go.memory.used`, `go.memory.gc.goal`, `go.memory.allocated`,
  `go.goroutine.count`, scoped to the deployed process exactly once.
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

These kinds, and one field, are drawn by the console and have no HyperDX
equivalent, so HyperDX skips them too:

| kind / field | takes | draws |
|---|---|---|
| `bar` | `groupBy` (required), `series`, `limit` (default 10) | top-N bars: one group per `groupBy` value, the `limit` largest by the first series |
| `heatmap` | one series: `agg: count` plus a numeric `field` (`span.duration`); `signal` traces or logs | time across, value buckets up, each cell the number of events in it |
| `sql` | `sql`: one read-only SELECT; no `series`, `where` or `groupBy`; `signal` optional | the rows; a first `DateTime` column draws a time series |
| `postgres` | `postgres: {database, sql, draw?, time?, values?, group?}`; no `signal`, `series`, `sql`, `where` or `groupBy` | a live read-only query of one of your Postgres databases (`observability/dashboards/panels`) |
| `text` | `markdown`; nothing else | prose: a heading, what the dashboard is for, a runbook link |
| `formula` on `line` / `number` | `"A / B * 100"`: series by position (A, B, ... J), numbers, `+ - * /`, parentheses | that one expression instead of the series; `x / 0` is 0, as in `ratio` |

`limit` (1 to 200) also keeps the top rows of a `table`. A `sql` statement runs
under the alert-rule guard and limits (one SELECT; no writes, DDL, `SETTINGS`,
`FORMAT`, `system.*` or outside table functions; a timeout and a row cap) as the
viewer's own read-only database user, and binds the dashboard's scope as
ClickHouse parameters: `{from:DateTime64(9)}`, `{to:DateTime64(9)}`,
`{step:UInt32}`, `{env_ids:Array(String)}`, `{envs:Array(String)}`,
`{services:Array(String)}`, and, when exactly one environment is in scope,
`{reliant_env_id:String}`, so an alert rule's query runs in a panel as written
(over any other scope a statement that uses it is refused: choose one
environment).

Postgres panels (a live read-only query of your own database), `text`
panels (Markdown) and `display` (area, stacked, bars, pie, donut, gauge and
funnel pictures of the same query) are in `observability/dashboards/panels`.

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

A file you own is one dashboard object in the spec vocabulary (an `id`, a `title`, `filters`, `annotations` and a `panels` array; the shape of a `dashboard` in `spec/process-<workload>_gen.json`). Name it like a generated dashboard to replace that one: `shop-overview.json` replaces `shop-overview_gen.json`. A file with `tiles` is HyperDX's format; it is for the local stack, ships nowhere and replaces nothing. Ad-hoc console edits are saved as code by writing the dashboard JSON here and committing it.

Every file is validated at the cut, and `forge obs dashboards check` runs the same checks and lists what would ship. Errors read `deploy/observability/dashboards/x.json:12:7: panels[2].kind: "cube" is not one of: number, line, table, search, bar, heatmap, sql, issues, sessions, funnel, postgres, text`.

The file checks cannot know what your telemetry holds. `forge obs dashboards check --against <env>` also looks up every metrics panel series in that env over `--since` (24h): a `metricType` that is not the kind the metric is stored as fails the check, and a metric with no data is listed (a misspelt name, one the collector drops or renames, such as the filter processor's `otelcol_processor_filter_logs.filtered`, or a counter that only exists once something fails). `--strict` fails on those too. In the console a series with no data draws an empty panel with a `metric_absent` note, never an error.

`annotations: [{"source": "releases"}]` on a dashboard asks the console to draw a marker for every promotion on its time-series panels. Generated dashboards declare it.

The _Workloads_ dashboard (CPU/memory against requests and limits, restarts and exit reason, image versions, Kubernetes events) and the _Releases_ dashboard (request, error and latency by `service.version`) are generated for every project. They read node-derived and release data that a local `forge env up` does not have, so they ship in the spec and the bundle and are not written to `deploy/observability/dashboards/`.

## Alerts

Dashboards embed no alerts (HyperDX 2.40 validates an alert declared in a provisioned file and never evaluates it, hyperdx PR #3233). Alerting is declared in KCL instead: `forge.AlertRule`, with generated defaults (telemetry, customer-metadata and platform) in `alert-defaults_gen.json`, and each service's independent `spec/alert-service-<svc>_gen.json`. Depth: `observability/alerts`.

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
