---
name: v1.4.0
description: Upgrade a project to forge v1.4.0. (1) Session replay is a per-frontend, per-environment switch, forge.BrowserTelemetry {replay = True}, which renders the frontend's OTEL_REPLAY and opts the env in to replay ingestion; the env-wide Observability.signals.replay is deprecated (it still opts in) and an explicit runtime_config OTEL_REPLAY outranks the new switch. (2) Browser analytics are cookieless by default: the web runtime stores nothing on the device (no visitor id, no session cookie, no stored campaign) and runs replay only with consent; ANALYTICS_MODE = "full" plus grantAnalyticsConsent() restores the persistent visitor id, multi-day attribution and replay. Use when a deploy/kcl env declares ObservabilitySignals {replay = …} or a frontend's runtime_config OTEL_REPLAY, or when a web frontend has browser telemetry (src/lib/otel_gen.ts, or code calling initBrowserTelemetry / enableAttribution / withAttribution). (3) The Next.js dev API proxy (/_api) now outlives the API's deadline (experimental.proxyTimeout), so the browser gets the server's deadline_exceeded instead of a bare HTTP 500; use when a frontend's next.config.ts proxies /_api and sets no proxyTimeout.
version: v1.4.0
detection: grep -rqsE 'ObservabilitySignals *\{[^}]*replay *=|OTEL_REPLAY *=' --include='*.k' deploy/kcl && exit 0; ls frontends/*/src/lib/otel_gen.ts >/dev/null 2>&1 && exit 0; grep -rqsE --exclude-dir=node_modules --include='*.ts' --include='*.tsx' 'initBrowserTelemetry|enableAttribution|withAttribution' . && exit 0; for f in frontends/*/next.config.ts; do test -f "$f" && grep -qs 'devApiProxy' "$f" && ! grep -qsE 'proxyTimeout[[:space:]]*:' "$f" && exit 0; done; exit 1
---

# Upgrading to v1.4.0

## 1. Session replay moves onto the frontend

**Use when** an env's KCL (`deploy/kcl/<env>/main.k`) declares
`observability = forge.Observability {signals = forge.ObservabilitySignals {replay = …}}`
or a frontend's `runtime_config = {OTEL_REPLAY = …}`. Environments scaffolded
with the `_session_replay` switch before v1.4.0 declare both.

### What changed

Replay is a choice about one frontend in one environment, so it is declared
there:

```kcl
_web_frontend = forge.Frontend {
    name = "web"
    # …
    telemetry = forge.BrowserTelemetry {replay = _session_replay}
}
```

`telemetry.replay = True` renders that frontend's `OTEL_REPLAY = "true"` into
its runtime config (a hosted site's StaticSite spec too, so the platform reads
it live) and opts the env's observability policy in to replay, so the platform
keeps the events. Off renders no key: unset is off. A hosted frontend with it on
needs the env to declare `observability`. With observability or browser
telemetry off, nothing records, whatever is declared.

Nothing breaks: `signals.replay` still opts the env in (it is deprecated, and
switches no frontend on) and a `runtime_config` `OTEL_REPLAY` still reaches the
browser. Upgrading changes no env's behavior and rewrites none of your KCL.

### What to do

In each env that uses `_session_replay` (or replay at all):

1. On each web frontend, set `telemetry = forge.BrowserTelemetry {replay = _session_replay}`
   (in the dev env scaffold: `replay = _observability and _session_replay`).
   An existing `telemetry` block takes the `replay` field instead.
2. Delete the frontend's `runtime_config` `OTEL_REPLAY` entry. **It outranks
   `telemetry.replay`**: an explicit `"false"` left behind keeps that frontend
   off with the switch on.
3. Drop `signals = forge.ObservabilitySignals {replay = _session_replay}` from
   `observability` (leave `observability = forge.Observability {}` if it was
   the only field). The frontend's switch now opts the env in.

Then `forge env render <env>` and check the frontend's `runtime_config` (or
`runtime_config_spec` when hosted) carries `OTEL_REPLAY = "true"` exactly when
`_session_replay` is True, and `observability.policy.opt_in` lists `replay`.

## 2. Browser analytics are cookieless by default (a behavioural break)

**Use when** a web frontend has browser telemetry: a generated
`src/lib/otel_gen.ts`, or your own code calling `initBrowserTelemetry`,
`enableAttribution` or `withAttribution` from `@reliantlabs/forge-web-runtime`.

### What changed

The web runtime runs product analytics in a tier (`off` < `aggregate` <
`identified` < `full`; `frontend-runtime/telemetry`, "Analytics tiers"). A
frontend that sets no mode (no `ANALYTICS_MODE` in its runtime config, no
`analytics` option) now runs in **`identified`**: product events, page views
and a signed-in user's opaque id still flow, but **nothing is stored on the
device**. No API changed signature and server events are not affected, but
three behaviours of v1.3.0 are gone unless you restore them (below):

1. **No visitor id and no visitor→user join.** `initBrowserTelemetry` no
   longer creates, stores or stamps `visitor.id`, no longer captures
   `rid`/`utm_*` into storage, never emits `user_identified`, and
   `withAttribution(url)` returns the URL unchanged. A funnel or dashboard
   that joined anonymous visits to users through `user_identified` /
   `visitor.id` stops getting new rows. (Campaign source still arrives
   without storage: the first `page_viewed` of a page load carries the URL's
   `utm_*`, `ref` and referrer host.)
2. **The first page load after upgrading erases what v1.3.0 stored.**
   Starting below `full`, the runtime deletes `forge.visitor_id`,
   `forge.attribution`, `forge.identified_user`, `forge.auth.seen` and
   `forge.rum.session` from localStorage, `forge.replay.sampled` from
   sessionStorage, and the `__rum_sid` cookie. A returning visitor gets no
   continuity with their pre-upgrade id. The RUM session is then kept in
   memory: one session per page load.
3. **Session replay records only in `full`.** `replay: true` and KCL
   `telemetry = forge.BrowserTelemetry {replay = True}` (OTEL_REPLAY) record
   nothing unless the mode is `full` AND the app called
   `grantAnalyticsConsent()` before telemetry started (consent granted later
   starts replay on the next page load). This includes `forge env up dev`.

Also new: under GPC / Do Not Track a browser's events carry **no user id**
(not even from `setTelemetryUser` / `identifyUser`), and `ANALYTICS_MODE =
"aggregate"` makes browser events strictly anonymous.

### What to do

Nothing, if cookieless analytics without replay are what you want.

To restore all three (persistent visitor id, `user_identified`, multi-day
attribution, the session cookie and replay):

1. Raise the frontend's mode to `full` in each env's KCL that should have
   them:

   ```kcl
   telemetry = forge.BrowserTelemetry {
       replay = _session_replay                     # as before
       analytics = forge.Analytics {mode = "full"}
   }
   ```

   or, equivalently, `runtime_config = {ANALYTICS_MODE = "full"}` on the
   frontend.
2. Grant consent in the app on every page load, BEFORE `initTelemetry()` runs
   (so replay starts on that load): with a consent manager, once it says yes;
   for an internal tool whose users agreed to recording, unconditionally:

   ```ts
   import { grantAnalyticsConsent } from "@reliantlabs/forge-web-runtime";

   grantAnalyticsConsent();   // then initTelemetry(), as providers.tsx / main.tsx already call it
   ```

   Call `revokeAnalyticsConsent()` when consent is withdrawn (it erases what
   `full` stored), and offer `optOutOfAnalytics()` to a person who objects.
3. Hand-written callers of `initBrowserTelemetry`: pass `analytics: "full"`
   and call `grantAnalyticsConsent()`, or keep passing `attribution: true`,
   which still means exactly that (mode `full`, consent granted at start).
   `{ attribution: false }` plus `enableAttribution()` also keeps working.
   Both spellings are deprecated in favour of `analytics` +
   `grantAnalyticsConsent()`.

Then reload a page of the frontend and check, in the browser's devtools,
Application → Cookies and Local/Session Storage: below `full` nothing of
forge's is there; in `full` after `grantAnalyticsConsent()`, `__rum_sid` and
`forge.visitor_id` are, and a replay records.

## 3. The Next.js dev API proxy outlives the API's deadline

**Use when** a Next.js frontend's `next.config.ts` proxies `/_api` to the API
(it declares `devApiProxy`) and sets no `proxyTimeout:` key. Every Next.js frontend
scaffolded before v1.4.0 does. Vite frontends are unaffected: Vite's dev proxy
sets no timeout.

### What changed

Next's dev proxy gives up on an upstream after 30 seconds
(`experimental.proxyTimeout`), the same as the API's default unary deadline
(`observe.DefaultUnaryDeadline`). The proxy's clock starts first, so on a
deadline the proxy hangs up first: the browser gets a bare `HTTP 500`, the dev
log says `Failed to proxy … Error: socket hang up`, and the server's answer —
`504 {"code":"deadline_exceeded",…}` with a `traceresponse` naming the trace —
never arrives. An rpc whose `(forge.v1.method).timeout` is longer than 30s is
cut off at 30s in dev for the same reason.

The scaffold now sets the proxy's timeout to an hour, in the dev-only block, so
the server is always the one that answers. It is an idle-socket timeout: a
stream that keeps sending is never cut by it. Nothing changes in any build:
there is no proxy outside `next dev`.

`next.config.ts` is yours, so `forge generate` does not edit it.

### What to do

In each `frontends/<name>/next.config.ts`:

1. Below `const devApiProxy = …`, add

   ```ts
   const DEV_API_PROXY_TIMEOUT_MS = 3_600_000; // one hour: far past any server deadline
   ```

2. In the `process.env.NODE_ENV === "development" ? { … } : {}` block that
   holds `async rewrites()`, add a key after `rewrites()`:

   ```ts
           experimental: { proxyTimeout: DEV_API_PROXY_TIMEOUT_MS },
   ```

   If the config already has an `experimental` object, put `proxyTimeout` in
   it instead: a second `experimental` key in the spread replaces the first.

Restart `next dev`: Next reads its config only when it starts. To check: an rpc that runs past its deadline now shows the server's
code in the page (`deadline exceeded`, not `HTTP 500`), and
`grep 'socket hang up' .forge/logs/dev/frontend_<name>.log` stays empty.
