---
name: observability/product-events
description: Product events — the contract (one INFO log record, event.name product.<name>), emitting from Go (observe.Track) and the browser (track, setTelemetryUser), the automatic <entity>_created|_updated|_deleted events generated CRUD emits, their off switch (signals.product_events) and log-ingest cost, retention, and sql/funnel panels over the hosted product_events table.
---

# Product events

A **product event** is a named thing a user did or that happened to them
(`project_created`, `subscription_started`). On the hosted platform they land in
the per-environment ClickHouse `product_events` table, which keeps them far
longer than logs or traces. Dashboards read it with `sql` and `funnel` panels.

## The contract

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

## Emitting from Go

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

## Automatic events from generated CRUD

Every generated Create, Update and Delete rpc records a product event once its
write lands: `<entity>_created`, `<entity>_updated`, `<entity>_deleted`, with
`<entity>` the entity's snake_case name (`OrderItem` is `order_item_created`).
It lives in `forge/pkg/crud`, which every generated CRUD shim delegates to, so a
project gets it from a forge upgrade alone: no regenerate, no edit to the
handler files it owns. The event carries the caller's `user.id` and nothing
else: no primary key and no field values (ids are unbounded cardinality and
values may be personal data). A refused or failed write records nothing. A
write inside a transaction of your own records its event when the statement
succeeds, before your transaction commits.

They are **on by default**. Each one is one INFO log record, which the hosted
platform bills as log ingest like any other log line (a few hundred bytes per
write; `product_events` itself is derived from it and not billed). To switch
them off for an environment:

```kcl
observability = forge.Observability {
    signals = forge.ObservabilitySignals {product_events = False}
}
```

That renders `FORGE_PRODUCT_EVENTS=off` on every workload (set it yourself on a
workload to decide per workload; `observe.AutoProductEvents()` reports the
result). Events your own code emits with `observe.Track` are never switched off.

## Emitting from the browser

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

## Retention

`forge.Observability.retention.product_events_days` (1 to 400, default 90) is
how long the environment asks the platform to keep product events. The platform
may grant less (a plan can cap it); a new value applies to rows written after
it, not to rows already stored.

## Panels over `product_events`

Columns: `Timestamp`, `ServiceName`, `EnvId`, `SessionId`, `UserId`, `EventName`
(the name without the `product.` prefix) and `Props` (a map, `Props['plan']`).
`DESCRIBE product_events` lists what else the table keeps. A `sql` panel binds
the dashboard scope (`{from:DateTime64(9)}`, `{to:DateTime64(9)}`,
`{step:UInt32}`, `{env_ids:Array(String)}`, `{envs:Array(String)}`,
`{services:Array(String)}`); a first `DateTime` column draws a time series. The
table's environment column is `EnvId`, so scope by environment with
`(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))`, as
the generated Product usage panels do.

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
see `observability/dashboards`, Overriding); `forge obs dashboards check` validates it.
