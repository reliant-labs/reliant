# Product events

Product events (funnels, retention, feature use) ride the telemetry pipeline
Reliant already ships. There is no third-party analytics product: Statsig was
removed (owner decision, 2026-10-20), and so was the Supabase "GTM" event
sink that no server role was ever configured to write.

There are two producers, one per tier, and both land in ClickStack.

## Backend: one structured log record per event

`internal/analytics` keeps the typed event vocabulary (`events.go`) and the
per-user opt-out. Each `Track*` call writes **one INFO record through the
process logger** (`log/slog`, which `internal/logging` installs). The record's
message is `product.<event>` and its attributes are:

| Attribute | Value |
| --- | --- |
| `event.name` | `product.<event>`, e.g. `product.message_sent` (the same string as the message) |
| `user.id` | the user the event is about; absent only for an event with no user |
| `app.client` | `backend` on every event this package writes; the web runtime stamps `web`, and the desktop renderer `electron`, on the events it emits |
| `app.version` | Reliant build version |
| `product.<field>` | the event's typed fields, snake_case: `product.chat_id`, `product.input_tokens`, ... |
| `trace_id`, `span_id` | stamped by the trace handler when the request carries a span |

Why a log record and not an OTel span or an OTel Logs API record:

- **It reaches ClickStack in every deployed environment without new config.**
  Server stdout is collected by the node agent (`filelog` →
  `json_parser` → ClickStack `otel_logs`, see control-plane
  `deploy/kcl/lib/otel_agent.k`), and the JSON attributes become
  `LogAttributes`. Forge's OTLP *logs* exporter is off by default, so a record
  written through the OTel Logs API would be dropped; switching it on would
  ship every log line twice.
- **Spans can be sampled and are only exported where an OTLP endpoint is
  set.** A product event must not disappear because its request's trace was
  not sampled.
- **When OTLP log export is switched on** (`OTEL_LOGS_EXPORTER=otlp`), forge
  tees the process logger into the OTel logs bridge, so the same record leaves
  as an OTel log record instead. Exactly one shipping path either way.

Opt-out: `analytics.ForUser(ctx, userID)` reads the user's
`privacy.analytics_enabled` setting. A user who opted out, or whose setting
cannot be read, gets a tracker that emits nothing (fail closed).
`RELIANT_ANALYTICS_DISABLED=true` turns every backend event off.

Duplicate suppression: an identical event for the same user within 5 seconds
is dropped (React StrictMode double effects, retried RPCs).

## Web: `trackEvent` → ClickStack span

`frontends/web/src/lib/analytics.ts` `trackEvent(name, props)` sends the event
through the forge web runtime: `track("product.<name>", props)` — one span from
the browser SDK's `custom-action` tracer, on the same session as the page's
errors and replay. Props are snake_cased and bounded by the runtime. The user is
the `userId` attribute set by `setTelemetryUserId`.

The user's in-app analytics toggle gates it, and it is live only in a deployment
whose runtime config sets `OTEL_ENDPOINT` (`telemetryEndpoint.ts`).

## Who emits what

Each event has exactly one producer, so a count never has to be de-duplicated.
`message_sent` (and `first_message_sent`) are the backend's: it knows the
message id, workflow and thread, sees every client (web, desktop, CLI,
triggers) and is immune to ad blockers. The web has no `message_sent`.
`llm_call_completed` is not a product event: LLM usage lives in control-plane
usage records and metrics.

`product.app_first_launch` is the desktop app's, once per install, from the
renderer (it owns consent and the telemetry pipeline) with `install_id`,
`installed_at`, `platform`, `arch` and `version`. Electron main keeps the
install record in `userData/install.json` and marks the launch reported only
after the renderer says the event went out, so a first launch made offline or
before consent is sent on a later launch, dated by `installed_at`.

## Who a failure belongs to

A server's errors name the user of the request that failed, not the last user
the process saw. The auth layer binds the caller to its request
(`telemetry.BindUser`); the logger stamps `user.id` on records logged in that
request's context, the server span carries `user.id`, and a forwarded ERROR
record reaches Sentry with that user. There is no process-wide Sentry user.

## Retention

Product events keep the retention of the table they land in: backend events
are INFO log records, kept 14 days in `otel_logs`; web events are spans, kept 7
days in `otel_traces` (control-plane `internal/obstenant/schema`, `LogsInfoDays`
and `TracesDays`). That covers funnels over days, not retention cohorts over
months. Longer retention needs its own table (a materialized view of the
`product.%` rows with a longer TTL), and a matching change to the privacy
page's retention table, which currently says "Up to 14 days".

## Querying

Backend events (ClickStack tenant zero, `otel_logs`):

```sql
-- Users who configured a provider key and then sent a first message within
-- a day, over the last 30 days.
SELECT level, count() AS users
FROM (
    SELECT LogAttributes['user.id'] AS user_id,
           windowFunnel(86400)(
               Timestamp,
               LogAttributes['event.name'] = 'product.api_key_configured',
               LogAttributes['event.name'] = 'product.first_message_sent'
           ) AS level
    FROM otel_logs
    WHERE Timestamp > now() - INTERVAL 30 DAY
      AND LogAttributes['event.name'] LIKE 'product.%'
      AND LogAttributes['user.id'] != ''
    GROUP BY user_id
)
GROUP BY level
ORDER BY level;
```

Web events (`otel_traces`): `SpanName LIKE 'product.%'`, user in
`SpanAttributes['userId']`, browser session in
`SpanAttributes['rum.sessionId']`. A backend event logged during a request
carries that request's `TraceId`, which joins it to the browser span (and so
the browser session) that made the call.

## Claim funnel (the `/claim` page)

A claimable deploy is handed to a human as `…/claim#rcl_<token>`
(control-plane `docs/design/claimable-deploys.md`). The web emits these, all
from `frontends/web/src/components/Claim/useClaimFlow.ts`, and never the token:

| event | when | props |
| --- | --- | --- |
| `claim_link_opened` | the preview loaded, once per link | `result` (`live`, `suspended`, a refusal reason, or `error`), `situation` |
| `claim_started` | "Claim it" pressed (or the upgrade resumed) | `situation` |
| `claim_completed` | `CompleteClaim` succeeded | `claim_path`, `holds` |
| `claim_failed` | any step failed | `stage` (`preview`, `open_session`, `adopt_session`, `complete`), `reason` (the server reason, else the Connect code) |

`situation` is `signed-out`, `resume-upgrade`, `replace-anonymous` or
`existing-account`. The control plane sends its own `signup` event with
`source=claimable_deploy` at completion; the web does not duplicate it.
