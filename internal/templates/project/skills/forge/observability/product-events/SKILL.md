---
name: observability/product-events
description: Product events — the contract (one INFO log record, event.name product.<name>), emitting from Go (observe.Track) and the browser (track, setTelemetryUser), the browser's cookieless analytics tiers (off < aggregate < identified < full, identified by default; grantAnalyticsConsent, optOutOfAnalytics, GPC/DNT), automatic page_viewed with its referrer/utm/click_id props, page_engaged (engaged time and scroll depth per view), flag exposures (trackExposure, forgeExposureHook), the reserved event-name prefixes, the standard auth events the console counts users from (observe.TrackAuth, trackAuthEvent, identifyUser; the record for any other language), the automatic <entity>_created|_updated|_deleted events generated CRUD emits, their off switch (signals.product_events) and log-ingest cost, the KCL forge.Analytics block (ANALYTICS_MODE, goals, activation, channel rules, Funnel.by visitor/person), retention, and sql/funnel panels over the hosted product_events table.
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
stamps `app.client` on them, and `visitor.id` only in the consented `full`
analytics tier (below).

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

`observe.TrackState` emits a fact about a user that is not activity (a
snapshot, a marker); the user counts skip it. Load
`observability/product-events/user-counts` for it and for the project's own
rules (`not_activity`, `exclude_users_with`).

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
import { track, setTelemetryUser } from "@reliantlabs/forge-web-runtime";

track("onboarding_completed", { plan: "pro" });   // after initBrowserTelemetry
setTelemetryUser(session.user.id);                // the opaque subject, never an email
```

`initBrowserTelemetry({ appClient })` stamps `app.client` (`electron` inside
Electron, else `web`; pass `landing` from a marketing site) on every event.
Everything is behind one gate: nothing is sent until `initBrowserTelemetry`
runs, and nothing after `disableTelemetry()`.

### Analytics tiers: cookieless by default

What the browser sends and keeps is decided by its **tier**, ordered `off` <
`aggregate` < `identified` < `full`: the lowest of the app's `analytics` mode
(the runtime config's `ANALYTICS_MODE`; absent means `identified`), the
consent the app signalled, and the browser's own privacy signals:

| tier | when | stored on the device | ids on browser events |
|---|---|---|---|
| `off` | `analytics: "off"`, or the person called `optOutOfAnalytics()` | only the opt-out flag (`forge.analytics_opt_out`) | none: no product events, no user id (errors and traces still flow) |
| `aggregate` | mode `aggregate`, and always under Global Privacy Control / Do Not Track | **nothing**: no cookie, no localStorage, no sessionStorage (the RUM session id is kept in memory) | none, even when signed in; the platform's ingest edge counts visitors with a daily-salted hash it forgets |
| `identified` | **the default** mode, once a user is named (`identifyUser` / `setTelemetryUser`, or the generated session identity), no GPC/DNT | **nothing**, as aggregate | the opaque user id |
| `full` | `analytics: "full"` **and** `grantAnalyticsConsent()` | `forge.visitor_id`, `forge.attribution`, the `__rum_sid` session cookie | user id + persistent `visitor.id`; `user_identified`; replay when configured |

```ts
import {
  grantAnalyticsConsent, revokeAnalyticsConsent, optOutOfAnalytics, analyticsTier,
} from "@reliantlabs/forge-web-runtime";

// analytics: "full" (runtime config ANALYTICS_MODE = "full"), then, from your
// consent manager, on every page load once the person said yes:
grantAnalyticsConsent();   // the session moves to the cookie, visitor.id starts
revokeAnalyticsConsent();  // back to identified; what full stored is erased
optOutOfAnalytics();       // the person objects to analytics at all: tier off, remembered
```

The runtime never remembers consent; your consent manager does, and calls
`grantAnalyticsConsent()` each page load. Consent given mid-page keeps the
running session (it moves from memory to the cookie) and starts replay on the
next load. A start in a mode that can never reach `full` erases the ids an
earlier page load stored. GPC / DNT cap any mode at `aggregate` **with no user
id**: such a browser is counted, never identified and never stitched. Server
events (CRUD, `Track`, `TrackAuth`) are not gated by any of this.

In the `full` tier only, the runtime reads `rid` and `utm_*` from the landing
URL into storage, and the first `setTelemetryUser(id)` for a user emits one
`user_identified {visitor_id, utm_*}`, which is how a funnel joins a visitor
to a user across days. A marketing site wraps its outbound links in
`withAttribution(url)` (a no-op below `full`) so the app sees the `rid` and
utm values. `initBrowserTelemetry({ attribution })` and `enableAttribution()`
are deprecated spellings that keep working: passing `attribution` at all means
mode `full`, `true` also grants consent, and `enableAttribution()` raises the
default mode to `full` and grants.

### Page views, with where the visit came from

Every generated web frontend (Next.js, Vite) records `page_viewed` for each
route the user lands on, in every tier above `off`:

| prop | value |
|---|---|
| `route` | the pathname only: basePath stripped, id-looking segments as `:id` (`/orders/:id`) |
| `entry` | `"true"` on the first view of a page load (absent after) |
| `referrer_host`, `referrer_path` | the referrer's host and scrubbed path, on the entry view only, and only when it is another site (not this host, its `www.` twin or a `pageViews.siteHosts` entry) |
| `utm_source` … `utm_content`, `ref` | from the URL's query, only when present, at most 128 characters; `ref` is `?ref=` or `?source=`; a value containing `@` is dropped |
| `click_id` | which ad network's click id the URL carries: `google` (gclid, gbraid, wbraid, dclid), `microsoft`, `meta`, `tiktok`, `linkedin` or `other`. **Never the id itself**: it is a persistent cross-site identifier |

So a campaign reaches the console with nothing stored. Country, device,
browser and OS are derived at the ingest edge, not sent by the page. With
native sign-in the frontend also identifies the signed-in user by itself: the
runtime reads the `subject` of `/auth/session` (through `src/lib/api-base.ts`,
as sign-in does), the same id `observe.Track` writes as `user.id`, so browser
and backend events join on `UserId`. An environment turns either off per
frontend with `telemetry = forge.BrowserTelemetry {page_views = False}` /
`{identify_user = False}` (rendered as `PAGE_VIEWS` / `IDENTIFY_USER = "false"`
in its runtime config). How both work: `frontend-runtime/telemetry`.

### Engaged time

Each page view also ends in one `page_engaged`, in every tier above `off`. Its
one identifier, `view_id`, names the view, not a person:

| prop | value |
|---|---|
| `route` | the view's route, named as its `page_viewed` was |
| `view_id` | 16 random hex characters minted when the view starts, kept only in memory, shared by the view's `resumed` reports and by no other view |
| `engaged_ms` | the milliseconds the page was **visible**, the window **focused** (or in use anyway: a wheel over a background window) and the person **active**: a pointer, key, wheel, touch or scroll in the last 30 s. Past that idle cutoff the clock stops until the next input. Capped at 1,800,000 (30 minutes) |
| `scroll_pct` | the deepest the bottom of the viewport reached in the document during the view, 0-100: `(scrollY + innerHeight) / scrollHeight`. A page that fits the viewport is 100. Content scrolling inside an app's own element is not the document's depth |
| `entry` | `"true"` when the view was the first of its page load, as on `page_viewed` |
| `resumed` | `"true"` on a later report of the same view (below) |

It is reported once per view, at the first of a route change (SPAs), the tab
being hidden, or `pagehide` (unload or the back/forward cache: a static
multi-page site's only report). On hide and unload the runtime then asks the
SDK to export at once; the exporter posts with `fetch(…, {keepalive: true})`,
which the browser completes after the page is gone. A view shorter than 500 ms
of engagement (a redirect) is not reported.

A reader who comes back to a view (the tab shown again, a back/forward-cache
restore) keeps adding to it: the next report has the same `view_id`,
`resumed: "true"`, and the view's **cumulative** `engaged_ms` and `scroll_pct`.
So per view, take the **last** report, which is also the largest. Key it on
`view_id`, never on route and time order: a back/forward-cache restore can
resume a view of a route the same visitor has since viewed again.

```sql
SELECT EnvId, Props['view_id'] AS view, any(Props['route']) AS route,
       max(toFloat64OrZero(Props['engaged_ms'])) AS engaged_ms,
       max(toFloat64OrZero(Props['scroll_pct'])) AS scroll_pct
FROM product_events
WHERE EventName = 'page_engaged' AND Props['view_id'] != '' AND …
GROUP BY EnvId, view
```

`page_engaged` is not an interactive event: it neither starts nor extends a
visit, and a view with only `page_engaged` after it is still a bounce.

### Flag exposures

`trackExposure(flag, variant, { provider })` records one
`flag_evaluated {flag, variant, provider}` per person, flag and variant per UTC
day (deduped in memory). An app on any OpenFeature provider registers the hook
once and needs no other code:

```ts
import { OpenFeature } from "@openfeature/web-sdk";
import { forgeExposureHook } from "@reliantlabs/forge-web-runtime";

OpenFeature.addHooks(forgeExposureHook);
```

A failed evaluation is no exposure; an object-valued flag with no variant name
is recorded as `object`, never its contents.

### Reserved event names

The prefixes `auth_`, `page_`, `flag_` and `web_` are forge's own events.
`track()` still sends an app event that uses one, and warns once per page load:
rename it so the console's built-in panels stay exact.

## Auth events: who signed up, signed in, signed out

`auth_signed_up`, `auth_signed_in` and `auth_signed_out` are reserved for a
fixed auth vocabulary the console counts new and active users from. Load
`observability/product-events/auth-events` for the emitters (`observe.TrackAuth`,
the auth middleware's daily heartbeat, `identifyUser`, `trackAuthEvent`, any
other language) and the edge visitor-day hash.

## Project funnels and counts: `forge.ProjectAnalytics`

Funnels and counts over every event (browser, server and CLI alike) that
belong to no one frontend are declared once for the project, on the
environment's observability, and drawn on the built-in Product dashboard:

```kcl
observability = forge.Observability {
    analytics = forge.ProjectAnalytics {
        funnels = [forge.Funnel {
            id = "forge-deploy", by = "org", window = "30d"
            steps = [
                forge.Step {event = "deploy_succeeded", where = {first_for_org = "true"}}
                "claim_completed"
                forge.Step {event = "deploy_succeeded", where = {first_for_org = "false"}, within = "14d"}
            ]
        }]
        counts = [forge.Count {id = "deploys", event = "deploy_succeeded", group_by = ["mode", "first_for_org"]}]
    }
}
```

- A step is an event name or a `forge.Step`: `where` props must match (values
  are compared as strings), and `within` bounds the time since the previous
  step. With any `within`, the funnel counts a sequence (each step within its
  `within`, else the funnel's `window`, of the one before); without, every step
  is within `window` of the first.
- `by = "org"` counts the org each event is about (`org.id`: the claims, or
  `observe.WithActor`), across the project's environments. Project funnels
  count by `visitor`, `person` or `org`.
- A `forge.Count` is a table: the event's occurrences per combination of its
  `group_by` props, with the distinct orgs and users behind them.
- Every environment's declarations are merged, one funnel or count per id, as
  frontends' funnels are.
- `by = "org"` and the counts' org column read `product_events.OrgId`, which
  needs the hosted tenant schema v29.

## Declaring product analytics in KCL: `forge.Analytics`

Nothing needs declaring: a frontend with no `analytics` block runs in the
cookieless `identified` mode: nothing is stored on the device, and a signed-in
user's events carry their opaque id. The block sets the browser's mode and adds goals,
an activation event and channel rules to the project's built-in product
dashboards. It is per frontend and per environment, beside the frontend's
other telemetry:

```kcl
forge.Frontend {
    name = "web"
    telemetry = forge.BrowserTelemetry {
        analytics = forge.Analytics {
            mode = "identified"                # "off" | "aggregate" | "identified" (default) | "full"
            goals = [
                forge.Goal {id = "checkout", event = "purchase"},
                forge.Goal {id = "pricing", title = "Saw pricing", route = "/pricing"},
            ]
            activation = "order_created"       # unset: the user's first <entity>_created
            activation_window = "7d"           # unset: 7d; at most 90d
            channels = [forge.Channel {host = "news.ycombinator.com", channel = "Organic social"}]
            # dashboards = False               # drop the built-in product dashboards
        }
        funnels = [forge.Funnel {id = "buy", steps = ["view_product", "purchase"], by = "person", window = "7d"}]
    }
}
```

| Field | Meaning | Checked at render |
|---|---|---|
| `mode` | the MOST the app allows a browser. A browser runs in the lowest of it, the consent the app signalled and its privacy signals (GPC/DNT caps it at `aggregate`: counted, never identified). `off`: no browser product events. `aggregate`: strictly anonymous, an opt-down: nothing stored on the device, a visitor counted only by the platform's daily edge hash, no user id on browser events. `identified` (the default): also cookieless, nothing stored on the device, and a signed-in user's browser events carry their opaque user id. `full`: once the app calls `grantAnalyticsConsent()`, a persistent visitor id and multi-day attribution too. Server events (`observe.Track`, CRUD, sign-ins) are never gated by it | one of the four |
| `goals` | conversions the Product dashboard counts: persons, total, and conversion over visitors. Exactly one of `event` (a product event) and `route` (a `page_viewed` route, id segments as `:id`) | unique ids; at most 50; a route is a path with no query or fragment |
| `activation` | the event that marks a user activated | a product event name |
| `activation_window` | how long after signing up a user has to activate | `30m`, `1h`, `7d`…, at most `90d` |
| `channels` | rules tried in order **ahead of** the built-in channel table; a visit whose first page view matches every condition a rule sets (`host`: the referrer host or a subdomain of it; `source`/`medium`: `utm_source`/`utm_medium`, case-insensitive) is credited to `channel`, a built-in name or a new one | at least one condition; a host name, not a URL; no two rules with the same conditions |
| `dashboards` | `False` drops the built-in Users, Product and Acquisition dashboards and the Overview's Traffic and Users sections; collection still follows `mode` | |

`mode` reaches the browser as `ANALYTICS_MODE` in the runtime config document
(`config.js`, or a hosted site's StaticSite spec), so one build promotes across
environments and each sets its own. Only a non-default mode renders the key:
absent means `identified`. When the environment switches browser telemetry off
(`signals.browser = False`), no mode is derived and any `ANALYTICS_MODE` you set
in `runtime_config` is forced to `off`; otherwise your own `runtime_config`
entry outranks the derived one.

`forge.Funnel.by` counts a journey by browser `session` or `user` (a `funnel`
panel on the frontend dashboard, over browser spans), or by `visitor` (the
cookieless daily visitor, so a window of at most `1d`) or `person` (the
signed-in user, or the same-day visitor one sign-in links to them). The last two
read `product_events` and belong to the built-in Product dashboard.

The dashboards are the project's, while each env declares its frontends, so
`forge generate` merges every env's and frontend's declaration: an equal goal or
channel rule collapses, a goal id or channel conditions declared two different
ways is refused naming both, `activation` and `activation_window` must agree
wherever they are set (unset defers), and any `dashboards = False` drops them.

What the dashboards draw from these declarations, and every definition they
count by (visitor-day, visit, channel table, same-day stitch, person, sign-up
attribution, activation): `observability/product-analytics`.

## Deleting a user's events: `observe.RequestPurge`

On account deletion or a data-subject request, `observe.RequestPurge(ctx,
requestID, observe.PurgeSubject{UserID: ..., VisitorID: ...})` asks the
platform to delete the subject's product events in this environment. Load
`observability/product-events/purge` for how it is authenticated and what it
covers.

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
the built-in product dashboards do (`observability/product-analytics`, which
also lists the edge columns `DayVisitor`, `Country`, `DeviceType` …).

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
