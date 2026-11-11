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
`llm_call_completed` is the worker's: `CallLLMActivity` emits one per model call
(success or failure) from the one place that has the provider, the usage and the
latency. Its fields are counts and metadata only, never prompt or reply text:
`provider`, `model`, `input_tokens`, `output_tokens`, `cache_read_tokens` and
`cache_write_tokens` (omitted at 0), `latency_ms`, `success`, `error_type`
(omitted on success), `is_streaming`, `chat_id`, `workflow_id` and `step_name`.
It is an observability event, not a ledger: it is a log record with the
retention above, and spend and quota accounting stays in control-plane usage
records. (#1360 removed the emitter on 2026-10-23, which is why the event
stopped on 2026-10-24.)

`workflow_started` and `workflow_ended` are the worker's too, one pair per run.
`is_child` is true for a run another workflow or agent started (a spawned
sub-agent, or a node of a parent graph) and false for one a user, trigger or API
call started, so a panel counting runs filters `is_child != 'true'`.

`session_start` is the API server's: the first authenticated request after a
user was idle for 30 minutes (the auth interceptor's session tracker, in memory
and per replica). `app_first_session` rides on it: the first `session_start` of a
user EVER, recorded durably in `user_app_sessions` (an `INSERT ... ON CONFLICT DO
NOTHING` that wins), so it fires exactly once per user across replicas and
restarts. It tells "signed up but never opened the app" (a control-plane
`user_signed_up` with no `app_first_session`) from "opened it but did not
onboard". Any authenticated client counts: the web app, the desktop app, or the
reliant CLI; a claimable deploy's anonymous account never reaches this API. Like
every backend event it honours the user's analytics setting, which a new user
cannot have turned off before their first request. Users who had a project or a
chat when the table was created were backfilled, so their next visit is not a
first session.

`page_visited` is the web's: one per navigation, from the router root
(`components/PageVisitTracker.tsx`). `page_name` is the matched route template
(`/project/$projectId`), never the URL, so ids, query strings and hashes (a claim
link's token rides the hash) cannot reach the event's own properties;
`previous_page` is the previous template. (The browser SDK separately stamps the
page URL as `location.href` on every span, product events included; the runtime
leaves scrubbing that attribute to the ingest side.) A search-only change is not a visit. It goes through `trackEvent`, so
the analytics toggle, dev mode and `OTEL_ENDPOINT` gate it like every web event.

`product.app_first_launch` is the desktop app's, once per install, from the
renderer (it owns consent and the telemetry pipeline) with `install_id`,
`installed_at`, `platform`, `arch` and `version`. Electron main keeps the
install record in `userData/install.json` and marks the launch reported only
after the renderer says the event went out, so a first launch made offline or
before consent is sent on a later launch, dated by `installed_at`.

`user_identified` is the forge web runtime's, once per user per browser, at the
first `setTelemetryUser`: it carries `visitor_id` and the captured `utm_*`, and it
is the only join from a landing visitor (`Client = 'landing'`) to a user. The
landing site hands the visitor over as `?rid=<visitor id>&utm_*` on links to the
app. This app starts telemetry only after sign-in, by which time the router and the
sign-in have replaced that URL, so `lib/attributionBoot.ts` holds the handoff at
boot (localStorage `reliant.landing_attribution`, 7 days, until telemetry starts;
`lib/landingAttribution.ts`) and `initTelemetry` gives it to the runtime
(`enableAttribution({search})`), only with analytics consent.
`lib/__tests__/landingAttributionHandoff.test.ts` pins it through the real runtime.

## Auth events (forge's standard vocabulary)

`auth_signed_up`, `auth_signed_in` and `auth_signed_out`, with
`product.provider = supabase`, are forge's standard auth events: the ones the
console's Users card and dashboards count users from. They come from the
browser adapter, the API server's daily sign-in heartbeat and the Supabase
trigger hook, all skipping anonymous sessions and honouring the analytics
opt-out. How each is wired, and the one-time Supabase setup:
`dev-docs/supabase-auth-events.md`.

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

### Who and where (every browser span, not only product events)

Collected with **Crash and Error Reporting**, not analytics, and stopped with it:

| attribute | on | where it comes from |
| --- | --- | --- |
| `userId` | every browser span | the page (`setTelemetryUserId`): display only, never trusted for access |
| `user.id` | the backend's server span | the AUTHENTICATED caller, bound by reliant's own auth (`telemetry.BindUser`, through `interceptors.RequestScope`) on both server spans. forge's `observe.IdentitySpanInterceptor` is in the chain too, but it reads forge claims, which reliant's auth does not set, so it adds nothing here |
| `session.id` | browser fetch/XHR spans; the backend's server span | the RUM session, sent by forge-web-runtime as W3C baggage on every Connect call; shape-checked, never propagated further. The pinned forge reads it onto the server span (`observe.RequestSpanInterceptor`). **Pending:** the installed `@reliantlabs/forge-web-runtime` (0.12) does not send it yet, so until a release that does is installed, only browser spans name the session, and a session's traces are its browser-started ones |
| `geo.country.iso_code`, `geo.region.name`, `geo.locality.name` | browser resource | derived at ingest by control-plane's workspace-proxy from the request's IP (DB-IP City Lite, CC BY 4.0, local); the page's own values are deleted |
| `client.address` | browser resource | **not collected.** One proxy flag (`PROXY_OTEL_INGEST_FORWARD_CLIENT_IP`) would keep it; turning it on requires updating `docs/help/privacy.mdx` first |

Never an email, and never on a metric: these are row attributes only, so no
series is keyed by a person or a place. Design and the GeoIP refresh:
control-plane `docs/observability/geoip.md`.

### Which device, client and build (every browser span)

Collected with **Crash and Error Reporting** too, and stopped with it. Two
sources, never mixed: what control-plane's workspace-proxy derives from the
request's own `User-Agent` and `Sec-CH-UA*` headers (page script cannot set
them, and the gateway deletes the page's own copies), and what the page alone
can know:

| attribute | on | where it comes from |
| --- | --- | --- |
| `user_agent.original` | browser resource | the request's `User-Agent` header (at most 512 bytes), stamped at ingest |
| `user_agent.name`, `user_agent.version` | browser resource | parsed at ingest (ua-parser, uap-core rules): `Chrome 156.0.0`, `Mobile Safari 26.6.1`, `Electron 41.10.7` for the desktop renderer |
| `os.name`, `os.version` | browser resource | parsed at ingest; refined by `Sec-CH-UA-Platform-Version` when the browser sends it (Windows 11, the real macOS version) |
| `reliant.device.type` | browser resource | `desktop`, `mobile`, `tablet` or `bot`, at ingest (`Sec-CH-UA-Mobile`, then the UA). iPadOS Safari presents as macOS, so it is `desktop` |
| `reliant.chromium.version` | browser resource | the `Chrome/` token of a Chromium-based client, the desktop app's included |
| `browser.brands`, `browser.platform`, `browser.mobile` | browser resource | the page: `navigator.userAgentData`'s low-entropy hints, sanitized by forge-web-runtime (GREASE brands dropped; no `getHighEntropyValues`) |
| `app.viewport.width/height`, `app.screen.width/height` | every browser span | the page, CSS pixels, restamped when a resize settles |
| `app.desktop.version`, `app.desktop.electron.version`, `app.desktop.chromium.version` | browser resource; the desktop main process's resource | the desktop app: the preload exposes `window.forgeDesktop` (the app version from main via `--reliant-app-version`, Electron and Chromium from `process.versions`); `./node` stamps the same on main-process telemetry |
| `service.version` | every span | the build: the web bundle's forge build id (`VITE_APP_VERSION`, which source maps are keyed by) or, in the desktop window, `appBuild().version`; a server span's is the server binary's release. The console shows it per service: each trace lists every service with its own version, and the waterfall names a service's build where the trace enters it. A session shows the forge **release** its frontend build shipped in (control-plane's deploy ledger, `frontend_builds`) with the build id beneath, or the build id alone when no release carries it, as for the bucket-hosted web app and the desktop app. (`reliant.build`, a span attribute, carries the web bundle's semantic version, `1.8.17-dev+2cd547cc`; the console does not show it.) |

No device id, model, fonts, plugins or other fingerprinting signal is read.
These are row attributes, never metric labels. Stored in tenant schema v21
(`rum_sessions.Client`/`Viewport`, `error_events.BrowserName`/…); design:
control-plane `docs/observability/client-context.md`.

#### Verifying it live (post-deploy check)

Only a live environment shows the ingest half (control-plane's
workspace-proxy) and the opt-out end to end. The browser half is the published
`@reliantlabs/forge-web-runtime` 0.13.1, built with npm provenance from forge
`5f5293e733e6` (tag `web-runtime/v0.13.1`), the forge `go.mod` pins. After the
release carrying this reaches prod, the person shipping it checks, against the
prod control plane (its release must contain the commit named in
`proto-vendor/controlplane/SOURCE`):

1. Sign in from a browser and from the desktop app, then open a project's
   Observability console. **Sessions**: both new sessions show browser, OS,
   device and build; the desktop one's client panel shows `app.desktop.version`
   and its Electron and Chromium versions.
2. Filter Sessions, Traces and Errors by that browser, OS and build: only
   matching rows remain. From a session, **Traces** (`session-traces`) lists
   its traces with the browser/OS/build filters cleared.
3. Turn **Crash and Error Reporting** off in Settings: the app reloads once the
   switch is saved (`lib/telemetry.ts`, the SDK's own fetch/XHR and error spans
   have no off switch short of a page without it). Use the app: no new session,
   span or error for that user appears after the switch.

A failure in 1-2 is a release regression (fix forward); a failure in 3 is a
privacy defect: roll the release back.

## Claim funnel (the `/claim` page)

A claimable deploy is handed to a human as `…/claim#rcl_<token>`
(control-plane `docs/design/claimable-deploys.md`). The web emits these, all
from `frontends/web/src/components/Claim/useClaimFlow.ts`, and never the token:

| event | when | props |
| --- | --- | --- |
| `claim_link_opened` | the preview loaded, once per link | `result` (`live`, `suspended`, a refusal reason, or `error`), `situation` |
| `claim_started` | "Claim it" pressed (or the upgrade resumed) | `situation` |
| `claim_completed` | `CompleteClaim` succeeded | `claim_path`, `holds`, `free_site` (`none`, `no-conflict`, `claimed-live`, `existing-live`) |
| `claim_free_site_prompted` | the free-site dialog opened (the claimer already has their one free static site) | `source` (`preview`: known up front; `claim_refusal`: the claim asked) |
| `claim_free_site_answered` | the dialog was answered | `answer` (`claimed`, `existing`, `subscribe`, `cancel`) |
| `claim_failed` | any step failed | `stage` (`preview`, `open_session`, `adopt_session`, `complete`), `reason` (the server reason, else the Connect code) |

`situation` is `signed-out`, `resume-upgrade`, `replace-anonymous` or
`existing-account`. The control plane sends its own `signup` event with
`source=claimable_deploy` at completion; the web does not duplicate it.

## LLM usage ledger (not a product event)

`llm_call_usage` is a service record, not telemetry: the analytics toggle does
not gate it, and it lives in Postgres, not in `otel_logs`. It is the durable,
countable record of model usage that product events and gen_ai spans are not
(events are kept 14 days, spans are tail-sampled and kept 7, and
`messages.token_count` is the context size, not what a call used). It is also
the source for per-user usage views (`GetLLMUsage`), including BYOK and
Claude/Codex/Copilot subscription calls that control-plane `usage_events`
never see.

- **One seam for every text request.** CallLLM's turn writes its row through
  `recordLLMCallUsage` (`handlers/llm_usage_record.go`), beside `logLLMUsage`
  and the `llm_call_completed` emitter. Every other model request (compaction
  summaries, chat titles, automation drafts) is made through
  `usageMeter.streamAndAccumulate` in the same file, which streams the request
  and records it. The two are independent of the event: the event is gated by
  the user's analytics choice and expires in 14 days; the ledger row is
  neither. They read the same `llm.TokenUsage`, so their counts agree.
  `purpose` (`chat`, `compaction`, `title`, `draft_automation`) says what a row
  was for.
- **Not recorded.** Image and video generation (their APIs return no token
  counts), `ValidateKey`'s test request, and `ProbeLLMCall` (an operator tool
  in `reliant-dev`). The privacy page names the first two; do not widen its
  "every model request" claim without covering them.
- **A guard for the next call site.** `TestModelRequestsGoThroughTheUsageLedger`
  parses the module and fails when anything outside `internal/llm/**`,
  `call_llm.go`, the meter and the probe calls `StreamResponse`, `SendMessages`
  or `StreamAndAccumulate`. A new request path goes through the meter, or is
  added to that test's allow-list with a reason (and to the privacy page if it
  is not recorded).
- **One row per provider request, counted per attempt.** The row key is
  `(id, attempt)`. The `id` is
  `<workflow id>/<run id>/<activity id>/<purpose>/<ordinal>`: the activity, what
  the request was for, and its ordinal among the requests that activity makes (a
  title or a compaction can make several). `attempt` is the Temporal attempt.
  Temporal re-runs a failed activity and every attempt is a real request that
  may have used tokens (a stream can report usage and then break), so each
  attempt is its own row and its own `calls` in the rollup. Only a repeated
  write of the same `(id, attempt)` is ignored. Purpose and ordinal keep
  requests apart, so none collides with CallLLM's `.../chat/1`.
- **Usage reported before a failure is kept.** A provider can bill a request
  and then drop the connection, and drivers send the final event (the one with
  the usage) either before or after the error event. `accumulator.StreamAndAccumulate`
  therefore returns the partial response next to its error (usage, content and
  tool calls seen so far; nil only when the stream produced nothing), and
  `usageMeter` records that usage while still returning no response to its
  callers. `CallLLM` stops reading at the error, so it reads on for the final
  event for up to one second (`collectUsageAfterStreamError`). A request cut by a
  cancel, a deadline or the progress guard is recorded as `cancelled` or a
  `timeout`/`stalled` error. The limit: a driver only reports usage in its final
  event, so a request cut before that event has no usage to record and is counted
  as a call with zero tokens. Tests: `TestUsageMeter_AFailedStreamKeepsTheUsageItReported`,
  `TestBackgroundRequests_FailedAttemptKeepsItsUsage`,
  `TestCallLLM_UsageLedgerKeepsTokensAFailedAttemptConsumed`,
  `TestUsageMeter_ContextEndedRequestsAreRecordedAsWhatCutThem`.
- **Count-only.** Columns are ids, provider, model, auth mode
  (`managed`/`byok`/`subscription`/`local`), four token counts, outcome, an
  `error_class` from a closed set (a CHECK constraint, so an error string
  cannot be stored), latency, attempt and `created_at`. There is no content
  column, and `TestLLMUsageTablesHoldNoContent` pins the exact column set. A
  new column means changing that test and `docs/help/privacy.mdx` together.
- **Rollup and retention.** `llm_call_usage_daily` sums per UTC day, user, org,
  provider, model, auth mode and purpose. `LLMUsageRetention` (api root) deletes
  per-call rows older than `db.LLMCallUsageRetention` (90 days) hourly, in
  batches; the rollup is kept. `accountpurge` deletes both tables with the
  account and fences the user (`llm_usage_purged_users`, under a per-user
  advisory lock) so a model call still in flight at deletion cannot write usage
  back afterwards; a fence refuses every later write for that user and is pruned after 30 days,
  longer than the 14-day workflow timeout.
- **Visibility (v1).** Only the user's own rows are readable
  (`Repo.ListLLMUsageDaily` takes the user id). Org admins do not see members'
  own-key usage. `org_id` is written empty today: reliant chats carry no org.
