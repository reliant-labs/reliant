---
name: frontend-runtime/telemetry
description: Browser telemetry in forge frontends — the HyperDX SDK forge scaffolds and initialises (otel_gen.ts, SERVICE_NAME), the same-origin /_otel route and who forwards it, the env knobs, the cookieless analytics tiers (off < aggregate < identified < full; identified by default: nothing stored on the device, the signed-in user's opaque id on events; full only with mode full + grantAnalyticsConsent; optOutOfAnalytics; GPC/DNT caps at aggregate), automatic page views with referrer/utm/click_id, flag exposures, source maps extracted to a registry sidecar and never served, new-project replay on and always masked, reportException for caught errors, and interaction() spans timing a user operation from click to usable.
---

# Browser telemetry (HyperDX SDK)

Forge scaffolds, injects and initialises the **HyperDX browser SDK**
(`@hyperdx/browser`, pinned exactly) in Next.js and Vite frontends. It captures
uncaught errors and unhandled rejections, `console.*`, fetch/XHR spans, document
load, long tasks and web vitals, and ships them as OTLP to ClickStack. There is
no Sentry SDK and no Sentry-protocol adapter. React Native frontends get
nothing yet: there is no DOM to instrument and the browser SDK does not resolve
there.

You write none of the wiring. `src/lib/otel_gen.ts` (regenerated every
`forge generate`; do not edit) reads the env and calls
`initBrowserTelemetry` from `@reliantlabs/forge-web-runtime/telemetry`, and
`providers.tsx` (Next.js) / `main.tsx` (Vite) call its `initTelemetry()` once.
It is idempotent, a no-op without a DOM or while telemetry is off, and never
throws.

The frontend reports `service.name` `<project>-<frontend>` (e.g. `shop-web`),
exported from the same file as `SERVICE_NAME`. The generated frontend dashboard
and frontend alert defaults filter on exactly that value. Code that starts the
SDK itself instead of calling `initTelemetry()` must pass `SERVICE_NAME` as
`serviceName`: any other name is invisible to the generated observability. The SDK is the page's **only** tracer provider: it replaces the old
OTel web wiring rather than joining it, because two providers cannot both
register globally.

## Same-origin `/_otel`, and no secret in the bundle

The browser POSTs OTLP/HTTP to `<basePath>/_otel/v1/{traces,logs}` on its **own
origin**. Something in front of the page forwards that to a collector's OTLP/HTTP
port, and that hop owns any credential. The SDK insists on an `apiKey`, so it is
sent the public placeholder `forge-browser-public`; a collector that enforces
ingestion auth (ClickStack `collectorAuthenticationEnforced`) must have the proxy
strip that header or replace it with the real key. Same-origin also means a CSP
of `connect-src 'self'`, no CORS preflight, and no third-party host for an
ad-blocker to list.

| Where | Who routes `/_otel` | Upstream |
|---|---|---|
| `next dev` | a dev-only `rewrites()` in `next.config.ts` to the runtime's loopback relay (`startOtelRelay`) | `OTEL_EXPORTER_OTLP_ENDPOINT`, default `http://127.0.0.1:4318` |
| `vite` | the runtime's `forgeOtelProxy()` plugin in `vite.config.ts` | same; the `/_otel` prefix is stripped |
| deployed | **the ingress** | a collector's OTLP/HTTP port |

Both dev proxies come from `@reliantlabs/forge-web-runtime` and forward only a
same-origin page's `POST /v1/{traces,logs,metrics}` up to 8 MiB (403 for a
foreign or missing `Origin`, 405 for another method, 404 for another path, 413
above the cap, none of them sent upstream), with only its `content-type` and
`content-encoding`: never its cookies or `authorization`.

A Next.js frontend builds as a **static export** (`output: static`), which has no
server, so `rewrites` do not exist in the built site; a built Vite SPA is plain
files. In a deployed environment the ingress must route `<basePath>/_otel/*` to
the collector. Until it does, leave the frontend's `otel_endpoint` unset and the
production bundle sends nothing, rather than POSTing to a 404.

## Turning it on, and the knobs

A value that differs per environment is read from the runtime config document
(`config.js`): a build variable is inlined at build time and follows that bundle
into every environment it is promoted to.

| Knob | Runtime key (`config.js`) | Declared by | Default |
|---|---|---|---|
| ingest path (`off` or empty disables) | `OTEL_ENDPOINT` | KCL `config.otel_endpoint` or `runtime_config` | `/_otel` in dev and on `forge.OnHosted`, **empty elsewhere** |
| `deployment.environment` | `ENVIRONMENT` | KCL `config.environment` or `runtime_config` | the forge env name |
| product analytics mode, the tier ceiling (`off` / `aggregate` / `identified` / `full`) | `ANALYTICS_MODE` | KCL `telemetry = forge.BrowserTelemetry {analytics = forge.Analytics {mode = "full"}}` (per frontend, per env), or `runtime_config`; forced to `off` when the env switches browser telemetry off | `identified`: cookieless, a signed-in user's events carry their opaque id (rendered only when it differs; see `observability/product-events`) |
| session replay (masked; `full` tier only) | `OTEL_REPLAY = "true"` | KCL `telemetry = forge.BrowserTelemetry {replay = True}` (per frontend, per env), or `runtime_config` | **on in new projects**, off in existing implicit-off envs |
| console capture | `CONSOLE_CAPTURE = "false"` | KCL `telemetry.console_capture = False` | **on** |
| replay sample rate | `REPLAY_SAMPLE_RATE` | KCL `telemetry.replay_sample_rate` | every session |
| `page_viewed {route}` and `page_engaged {route, view_id, engaged_ms, scroll_pct}` product events | `PAGE_VIEWS = "false"` | KCL `telemetry.page_views = False` | **on** |
| signed-in user id on every event | `IDENTIFY_USER = "false"` | KCL `telemetry.identify_user = False` | **on** with native sign-in |
| `service.version` | — (a build fact: `NEXT_PUBLIC_APP_VERSION` / `VITE_APP_VERSION`) | the deploy | the **build id** a deploy injects (12-hex hash of the frontend source and build env), unless you set it |

For a deployed environment, declare `otel_endpoint = "/_otel"` in the
frontend's KCL `config` block. Every web frontend's `config.js` names its
environment, so `ENVIRONMENT` needs no declaration. A literal
`{NEXT_PUBLIC_,VITE_}OTEL_ENDPOINT` / `ENVIRONMENT` / `OTEL_REPLAY` in the
frontend's `env_vars` is carried into the runtime config as well, so older
declarations keep working (on `forge.OnHosted`, only the ingest path: the
control plane writes that document's `ENVIRONMENT`). A build variable only exported in the shell at
`npm run build` is a **deprecated** fallback: used when `config.js` lacks the
key, with one `console.warn` per page load naming the KCL field to move it to;
it is removed in the next major.
The API origin (`API_URL`) feeds `tracePropagationTargets`, resolved as
`connect.ts` resolves it, so a cross-origin API still gets a `traceparent`.
`environment` is emitted as both `deployment.environment.name` and the legacy
`deployment.environment` that HyperDX's default sources key on.

## Analytics tiers: cookieless by default

Product analytics run in a **tier**, ordered `off` < `aggregate` <
`identified` < `full`: the lowest of the app's `analytics` mode
(`ANALYTICS_MODE` from the runtime config in a generated frontend; absent
means `identified`), the consent the app has signalled, and the browser's
GPC / DNT:

- **`identified`** (the default): product events and page views flow with
  **no persistent id, and nothing is stored on the device** — no cookie, no
  localStorage, no sessionStorage — and a signed-in user's events carry their
  opaque id. The HyperDX SDK's `__rum_sid` session cookie is answered from
  memory (`installRumSessionStore`), so spans still group into one session
  while the page is open. Visitors are counted by the platform's ingest edge
  with a daily-salted hash it forgets; the page sends no fingerprinting input.
- **`aggregate`** (mode `aggregate`, and always under GPC / DNT): as
  identified, but strictly anonymous: no user id on browser events, even when
  signed in.
- **`full`**: `analytics: "full"` **and** `grantAnalyticsConsent()`. The
  session cookie, a persistent `visitor.id`, stored campaign attribution
  (`user_identified`, `withAttribution`) and session replay.
- **`off`**: `analytics: "off"` or `optOutOfAnalytics()` (remembered in
  `forge.analytics_opt_out`, the one thing stored below `full`). No product
  events and no user id; errors and traces still flow.

```ts
import { grantAnalyticsConsent, revokeAnalyticsConsent, optOutOfAnalytics } from "@reliantlabs/forge-web-runtime";
```

Call `grantAnalyticsConsent()` on every page load once your consent manager
says yes (the runtime does not remember consent); `revokeAnalyticsConsent()`
erases what `full` stored and keeps the running session in memory;
`optOutOfAnalytics()` is the person's objection to analytics at all. A start
in a mode that can never reach `full` erases ids an earlier page load stored.
Under GPC / DNT the browser's events carry **no user id** (not even from
`setTelemetryUser`), and no auth event is sent. `attribution` /
`enableAttribution()` are deprecated spellings of mode `full` + consent and
keep working. The contract, and what each tier sends:
`observability/product-events`.

**Where a visit came from.** In `full`, the stored attribution keeps
`rid`, `utm_*`, `ref` (the host of the page that sent the visitor, never a URL)
and `lp` (the entry page's path). The first page load that names neither takes
them from itself. `withAttribution(url)` passes all of them on, and the first
sign-in's `user_identified` carries `referrer_host` and `landing_path`. Below
`full`, `withAttribution` passes on only this page load's `utm_*` and the
referrer's host: no id, nothing is stored, and only to a link on the page's own
host or on a site the call names — `withAttribution(url, { sites:
["example.com"] })` admits `example.com` and its subdomains. A link anywhere
else is returned unchanged, so wrapping every outbound link never hands a
campaign or a referrer to a third party.

**The org.** `setTelemetryOrg(orgId | null)` names the org later events are
about (the `orgId` attribute, stored in `product_events.OrgId`). It follows the
user id's rule: only in `identified` and `full`, never under GPC / DNT, never an
email.

## Page views and the signed-in user, automatically

The generated `otel_gen.ts` turns on two runtime options (both **off** when you
call `initBrowserTelemetry` yourself):

- `pageViews` — one `page_viewed {route}` product event per route the user
  lands on (Navigation API where the browser has it, else wrapped
  `history.pushState`/`replaceState` plus `popstate`). `route` is the pathname
  only: no query or fragment, the basePath stripped, and every segment that
  is not a plain route word (lowercase letters joined by `-`/`_`, or `v2`) as
  `:id`, so no id format leaves the browser; consecutive repeats are one view.
  `normalizeRoute(pathname, basePath)` from the barrel names your own events'
  routes the same way. `pageViews: { routeName }` replaces the naming (for an
  all-letters value like `/users/jane`); its result is scrubbed the same way,
  with `:param` / `[param]` placeholders allowed. The first view of a page
  load carries `entry: "true"` and its external referrer (`referrer_host`,
  scrubbed `referrer_path`; `pageViews: { siteHosts }` names hosts that are
  this site); any view whose URL carries them adds `utm_*`, `ref` and
  `click_id` — the ad network's name, never the click id.
  Each view also ends in one `page_engaged {route, view_id, engaged_ms, scroll_pct}`
  (`pageViews: { engagement: false }` turns that alone off): the time the
  page was visible, focused and in use (a 30 s idle cutoff, capped at 30
  minutes) and the deepest scroll, reported at the next route change, hide or
  unload, then exported at once with `fetch` keepalive. Details:
  `observability/product-events`, "Engaged time".
- `identify: { sessionUrl }` — for a frontend with native sign-in
  (`src/lib/auth/native-login.ts` and `src/lib/api-base.ts`), the runtime asks
  the same `/auth/session` `fetchIdentity` asks, resolved through `apiBase` as
  sign-in resolves it (dev proxy and runtime config included), with the
  cookie, and passes its `subject` — never `email` or `name`, and never an
  `@`-shaped subject — to `setTelemetryUser`, so browser events carry the
  backend's `user.id`. A frontend without `api-base.ts` gets no `identify`:
  there is no shared resolution, and guessing the origin can miss the cookie. It asks again on a move into or out of `/auth/*` and
  when the tab becomes visible, never per navigation. Signed out (a 401/403
  included) or an answer naming nobody (404, error, foreign body) clears the
  user it set, so no event is tied to the wrong account; before it has set
  one, such answers change nothing. Mock mode never asks. An app with its own IdP calls
  `setTelemetryUser` and turns this off (`identify_user = False`).

Both start only once the SDK runs and stop on `disableTelemetry()`. The events
land in `product_events` like any `track()` call (`observability/product-events`).

`trackExposure(flag, variant)` and the OpenFeature hook `forgeExposureHook`
(`OpenFeature.addHooks(forgeExposureHook)`) record `flag_evaluated` once per
person, flag and variant per day.

## Source maps are extracted, never served

A deploy builds with `FORGE_SOURCEMAPS=1` (the scaffolded `vite.config.ts` /
`next.config.ts` emit maps only then), then the static stage moves every map out
of the tree it publishes into `<staging>.sourcemaps/` (same relative paths, plus
`manifest.json` naming the project, frontend and build id) and strips the
`sourceMappingURL` trailers. The release digest excludes them, and a guard fails
a deploy whose served tree still holds one. Firebase asset retention never
carries a `.map` forward.

The sidecar is filed in the platform registry
(`<org>/<project>/<frontend>-sourcemaps:<build id>`), where the issues worker
symbolicates errors from. `telemetry = forge.BrowserTelemetry {sourcemaps =
"public"}` is the escape hatch for an app that wants its maps served: the tree
keeps its maps and trailers, and the maps are still copied to the sidecar and
filed, so its errors symbolicate too. `"off"` keeps maps nowhere, and its errors
stay minified.

## Replay defaults on for new projects, always masked

New app and static-site environments declare `_session_replay = True` in
`deploy/kcl/<env>/main.k`. It sets the frontend's
`telemetry = forge.BrowserTelemetry {replay = _session_replay}`, which renders
its runtime `OTEL_REPLAY` and the env's hosted ingestion opt-in.
Set it to `False` and restart/redeploy that environment to stop capture and
remove its ingestion opt-in. Reload open tabs to pick up the new document.
The choice is **per environment, not baked into a promoted artifact**.

Replay belongs to one frontend in one environment, so the switch for it is
`telemetry = forge.BrowserTelemetry {replay = True}` on that frontend in that
env's KCL. It renders the frontend's `OTEL_REPLAY = "true"` (a hosted site's
StaticSite spec too, so the platform reads it live) and opts the env's
observability policy in, so the platform keeps the events. A hosted frontend
with it on needs the env to declare `observability`. An author's own
`runtime_config.OTEL_REPLAY` still wins over it (so `"false"` there keeps that
frontend off). The env-wide `observability.signals.replay` is a **deprecated**
opt-in that keeps working but switches no frontend on, and a
`{NEXT_PUBLIC_,VITE_}OTEL_REPLAY` build variable is a deprecated fallback.
Envs scaffolded before v1.4.0 declare both: `migrations/v1.4.0` moves them.
With observability or browser telemetry off, nothing records.

Replay records a person, so it runs only in the **`full` analytics tier**:
`ANALYTICS_MODE = "full"` in the frontend's runtime config and
`grantAnalyticsConsent()` before `initTelemetry()` runs (consent given later
starts it on the next page load; revoking it stops the recorder). In the
cookieless modes `replay = True` records nothing. An internal tool whose
users have agreed to recording grants consent at startup.

Pin upgrades do not enable replay in existing projects: the SDK and KCL schema
retain their off default. Adding a frontend to an existing project does not
rewrite that environment's owned policy either. To opt in deliberately, set
`telemetry.replay = True` on the frontend;
setting only `OTEL_REPLAY` cannot authorize hosted ingestion. An explicit endpoint `off` or browser telemetry off disables capture. Replay
also respects `navigator.globalPrivacyControl === true`, DNT `1`/`yes` (including
legacy window/MS DNT), before sampling. Check consent before calling
`initTelemetry()` if your product requires it: the scaffold does not invent a
consent policy. On withdrawal call `disableTelemetry()` from the runtime; it
stops the recorder for this page load (already exported data is not recalled,
and ordinary SDK network tracing cannot be undone). A changed deployment
setting requires a page reload.

Dev keeps its normal destination selection. Hosted dev requests a session-bound
replay grant and enables capture only after the server acknowledges it; older
servers leave capture off and print an unavailable notice while ordinary
telemetry continues. Renewal retains the explicit choice; a denied renewal
publishes capture off for future page loads, never silently enables it later,
and asks existing tabs to reload. Tokens authorize ingestion, not the SDK's
capture intent: old grants retain their normal lifetime (currently one hour);
changing the switch is not an immediate revocation of every old token.
A container-free site needs no database or Docker: signed-in
hosted dev provides collection. Offline without a collector, recording is
unavailable and capture stays off. For local collection, supply an OTLP collector
and `observability.local_port`; app dev already declares local ClickStack.

The SDK bundles an rrweb recorder. When enabled, **all text and all inputs are
masked**, canvas/media/fonts are not recorded, and network bodies are off.
There is no unmasked setting. Sessions are sampled at
`frontend.telemetry.replay_sample_rate` (0..1, default 1, so every eligible
session). Sampling does not override a privacy gate. Recording adds browser
work and telemetry volume/storage; review the environment's retention and
access policy before shipping sensitive applications. Masking is defense in
depth, not permission to record without any consent your product requires.

## Reporting a caught error

Uncaught errors, rejections and `console.error` are recorded for you. React
error boundaries and Next's `error.tsx` swallow errors before `window.onerror`
sees them, so they call `reportException(error, attrs)` from the barrel (the
scaffolded `error.tsx`, `global-error.tsx` and `RuntimeErrorBoundary` already
do). It returns `false` when no SDK is running so the caller can fall back to
`console.error`, and it skips an error the SDK has already recorded — one failure
is one record. Use it in your own catch blocks the same way.

```ts
import { reportException } from "@reliantlabs/forge-web-runtime";

try {
  await checkout();
} catch (err) {
  if (!reportException(err, { "checkout.step": "payment" })) console.error(err);
  throw err;
}
```

## Who is signed in

Call `identifyUser(subject)` from the barrel wherever the app learns who is
signed in (the scaffolded auth context, Clerk's `useUser`, Auth0's
`useAuth0`), and `identifyUser(null)` on sign-out. It attaches the opaque id to
every span and replay (so the Sessions view and `forge obs sessions --user`
work) and sends one `auth_signed_in {kind: "resumed"}` per user per UTC day,
which is how the console counts active users. `trackAuthEvent("signed_in" |
"signed_up" | "signed_out", { userId, … })` reports the auth outcome itself, for
an app whose auth never touches a forge server. Both refuse an email-shaped id;
under GPC or DNT they send no auth event and attach no id. Below the `full`
tier the daily marker is kept in memory, never stored. The contract and the server-side
emitters: `observability/product-events/auth-events`.

**On Supabase, Clerk, Auth0 or Firebase**, one line does it: the provider's
adapter subpath maps its callbacks onto `identifyUser` and the sign-out event,
with the provider named on each event (structurally typed, no dependency):

```ts
import { createSupabaseAuthTelemetry } from "@reliantlabs/forge-web-runtime/auth/supabase";
createSupabaseAuthTelemetry(supabase); // next to createClient
// /auth/clerk: createClerkAuthTelemetry(clerk)    /auth/auth0: syncAuth0(client), reportAuth0SignOut(client)
// /auth/firebase: createFirebaseAuthTelemetry((cb) => onIdTokenChanged(auth, cb)), reportFirebaseSignIn(user, info)
```

Anonymous sessions (Supabase `signInAnonymously`, Firebase anonymous auth) are
never identified or counted. An app with its own `onAuthStateChange` passes
`supabaseAuthTelemetryListener()` its `(event, session)` instead.

## Interactions: click to usable

A fetch span times one RPC. To time what the user waits for — several RPCs,
a stream's first frame, a render — open an `interaction` (barrel) on intent
and end it when the screen is usable:

```ts
const op = interaction("chat.open", { "chat.id": id }); // in the click handler
useInteractionEnd(op, loaded); // endAfterPaint() on the first ready commit
```

It is one root span (`interaction.name|scope|outcome`), and every RPC made
while it is open is its child, server spans included — no `context.with`,
which a browser loses at the first `await`. The choice is made when the call
enters the interceptor chain, so a call held by auth's `getToken()` stays in
its own interaction; a chain whose outermost stages are the app's own and
await puts `interactionContextInterceptor()` first. A span the caller has
active when it makes the call still wins. One interaction is open per `scope` (default
`foreground`); a new one ends the old as `superseded`. `endAfterPaint()` waits
two animation frames; ends are idempotent; unended ones end as `timeout` after
60s; a page hidden while open stamps `page.hidden`. `mark(name)` adds a phase
event; `currentInteraction()` finds the open one. With telemetry off it records
nothing but its RPCs still share one trace id; it never throws.

Custom spans: `getTracer("checkout")` from `@/lib/otel_gen`.

Distributed tracing (the traceparent on every RPC) is independent and always
on — it does **not** depend on this module.
