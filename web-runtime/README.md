# @reliantlabs/forge-web-runtime

The forge frontend runtime — the web twin of `forge/pkg`. Framework-agnostic
React/TypeScript mechanism shared by every frontend forge scaffolds (Next.js
App Router and Vite SPA alike).

```ts
import {
  buildRuntimeInterceptors, // Connect transport stack
  ConnectClientError, // typed client errors
  RuntimeShell, // session + error boundary + toast host
  useSession, // the signed-in user
  RouteGuard, // signed-in render gate
  Resource, // the list tristate + cursor pagination
  reportException, // record a caught error with the browser telemetry SDK
  track, // record a product event (funnel step) on the current session
  setTelemetryUser, // attach an opaque user id to every span and replay
  disableTelemetry, // stop recording for the rest of the page load (consent)
  formatMinorUnits, // exact money display; parseMinorUnits for input
} from "@reliantlabs/forge-web-runtime";
```

## What is in here, and what is deliberately not

**In:** mechanism. The interceptor stack (auth, brand headers, W3C
traceparent, error normalization, idempotency-gated retry), the session
derived from JWT claims, the error boundary, the toast queue, the
`<Resource>` tristate/pagination container, trace-context propagation,
the HyperDX browser SDK wiring (`/telemetry` subpath), and integer money / basis-point formatting and parsing.

**Out:** presentation a project is meant to own. The component library under
`src/components/ui`, `globals.css`, the nav and the app layout all stay in the
scaffold, because forking them is the point.

The dependency runs one way. Nothing here imports application code — auth
state, event subscriptions and toast markup all arrive as **props**, so an app
can rename, restyle or delete its own files without breaking the runtime.

## What this package publishes

`dist/` — compiled ESM plus `.d.ts` — and nothing else. `src/` is not in the
tarball.

That is a contract, not a packaging detail. This package used to publish its
TypeScript sources: `types` pointed at `src/index.ts`, so **every consumer
typechecked this package's internals against its own React**. A React 18 app
(Expo SDK 52 pins 18.3) compiling sources written against `@types/react` 19
failed on files it never rendered —

```
providers.tsx: 'RuntimeErrorBoundary' cannot be used as a JSX component
resource.tsx:  Type '0n' is not assignable to type 'ReactNode'
```

— and no amount of version pinning fixes the shape of that: a package that
ships sources makes its own dependency versions its consumers' problem,
forever, for every consumer. Publishing declarations ends it. A consumer
typechecks against the `.d.ts`, which `skipLibCheck` does not re-check, and
whose `react` references resolve in the consumer's own program.

`src/published-surface.test.ts` is the gate: it fails if any entry point
points back into `src/`, or if `src` reappears in `files`.

### One `.d.ts` for React 18 and 19

`peerDependencies` declares `react: "^18.3.0 || ^19.0.0"`, and both halves are
real: forge's Next.js and Vite scaffolds are on 19, its Expo scaffold on 18.3.

The two `@types/react` majors disagree about where `JSX.Element` lives — 19
declares it inside the `react` module, 18.3 declares it globally and
re-exports it from `react/jsx-runtime`. An **inferred** component return type
is therefore emitted as `import("react").JSX.Element` when compiled against 19
and `import("react/jsx-runtime").JSX.Element` when compiled against 18, so the
published `.d.ts` would silently depend on whichever major happened to be
installed at build time.

So every exported component **declares `: ReactElement`**, a name both majors
export from `react`. With that, the emitted `.d.ts` is byte-for-byte identical
under either — the peer range is a fact about the artifact rather than a hope.
`src/published-surface.test.ts` fails on any `JSX.Element` reaching `dist/`,
and `npm run typecheck` compiles the package against **both** majors
(`tsconfig.react18.json` redirects `react` at an aliased `@types/react-18`).

### Peer dependencies

`react`, `@connectrpc/connect`, `@bufbuild/protobuf` and `@opentelemetry/api`
are peers, resolved from the consuming app. `next` deliberately is **not** — a
Vite SPA consumes this package identically.

Only `@opentelemetry/api` is a **required** peer: the barrel's trace
propagation and the `/node` entry both build on it. Every other peer is
**optional** (`peerDependenciesMeta`), because some entry point never imports
it and npm would otherwise install it into every consumer. `react`,
`@connectrpc/connect` and `@bufbuild/protobuf` are what the barrel and the
browser subpaths import, so a browser app declares them as its own
dependencies — every forge frontend scaffold (Next.js, Vite, Expo) already
does. A consumer of `/node` alone (an Electron main process, a Node CLI)
installs `@opentelemetry/api` and nothing else. `src/published-surface.test.ts`
holds the rule: a peer the `/node` import closure does not reach must be
optional.

Next.js consumers keep `transpilePackages: ["@reliantlabs/forge-web-runtime"]`
(forge's generated `next.config.ts` sets it). The reason changed with the
build — it is no longer "these are `.tsx` files" but "these modules carry
`"use client"` and Next has to process them rather than treat the package as
an opaque external."

## Browser telemetry (`/telemetry`)

Raw DOM events (including `mousedown`, `mouseup` and `click`) stay in masked
session replay rather than creating automatic interaction spans. Document loads,
fetch/XHR requests and explicitly instrumented meaningful actions still create
spans. The interceptor stack preserves the caller's OTel context across its own
authentication and telemetry-startup waits; the SDK propagates the actual network
span's context to configured API origins.

The real Chromium SDK contract test is a separate slow tier:
`npm run build && npx playwright install chromium && npm run test:browser`.
It verifies exact action → fetch parent IDs and wire `traceparent`, that the
fetch spans of unary and stream calls made while an `interaction()` is open
are its children, retains mouse events in replay, checks private
text/input sentinels are masked, and that no exported span carries the page
URL's query string or fragment.

`initBrowserTelemetry` starts the HyperDX browser SDK once per page load
(errors, console, fetch/XHR, page loads, web vitals, optional replay) and ships
to this origin's `/_otel`. It is a no-op without a DOM or an endpoint and never
throws.

```ts
import { initBrowserTelemetry } from "@reliantlabs/forge-web-runtime/telemetry";

await initBrowserTelemetry({
  serviceName: "web",
  serviceVersion: import.meta.env.VITE_APP_VERSION, // the forge build id
  frontend: "web", // resource attribute forge.frontend
  endpoint: "/_otel",
  consoleCapture: false, // log/info/warn/debug only; console.error(err) is still an error
  replay: true,
  replaySampleRate: 0.1, // 0..1 of SESSIONS; decided once, held in sessionStorage
});
```

One visit is one session (`rum.sessionId`), ending after 15 minutes without
activity or 4 hours. The SDK keeps the session only in a cookie, so on an origin
that drops cookies (an Electron renderer served from `app://`) it used to start a
new session every minute. Init now probes whether cookies persist and, when they
do not, keeps that one cookie in localStorage under the same limits
(`installSessionCookieFallback`). Origins whose cookies work are untouched.

`track`, `setTelemetryUser` and `disableTelemetry` come from the barrel, so a
component can call them without importing the SDK. They are safe anywhere: no-ops
on the server, and calls made while the SDK chunk is still loading are held
(at most 100) and replayed in order.

- `track(name, props?)` — one `custom-action` span. At most 20 props; keys
  `[a-z0-9_.]{1,64}`; values string, number or boolean; strings are redacted for
  credential shapes and cut to 256 chars. Not for user content.
- `setTelemetryUser(id | null)` — the `userId` global attribute. Opaque id only,
  never an email. `null` clears it.
- `disableTelemetry()` — stops the replay recorder and silences `track`,
  `setTelemetryUser` and `reportException` until the next page load. It cannot
  recall spans already batched, and the SDK's auto-instrumentation has no off
  switch once running: an app that must send nothing without consent should not
  call `initBrowserTelemetry` before it has it.
- `identifyUser(id | null, { provider? })` — `setTelemetryUser(id)` plus one
  `auth_signed_in {kind: "resumed"}` per user per UTC day per browser (deduped
  in memory, and in localStorage under `forge.auth.seen` only in the `full`
  tier). Call it wherever the app learns who
  is signed in; it is what counts a user with a long-lived session as active.
- `trackAuthEvent("signed_up" | "signed_in" | "signed_out", { userId, method?,
provider?, socialProvider?, verified? })` — the standard auth events, for an
  app whose auth never touches a forge server (forge's managed auth reports
  them from the server). Both refuse an id that is empty, longer than 128
  characters or contains `@` (the provider's opaque subject, never an email),
  and under Global Privacy Control or Do Not Track send no auth event and
  attach no id.

- Analytics tiers (`analytics: "off" | "aggregate" | "identified" | "full"`,
  ordered, default `identified`): the browser runs at the lowest of the mode,
  the consent the app signalled and GPC / DNT (which cap it at `aggregate`).
  Below `full` **nothing is stored on the device**: no cookie (the SDK's
  `__rum_sid` session is answered from memory, `installRumSessionStore`), no
  localStorage, no sessionStorage, no visitor id, no replay. `identified`
  carries a signed-in user's opaque id; `aggregate` carries none.
  `grantAnalyticsConsent()` (call it each page load once your consent manager
  says yes) raises mode `full` to the `full` tier: the session moves to the
  cookie, `visitor.id`, stored attribution, `user_identified` and (from the
  next load) replay. `revokeAnalyticsConsent()` erases what `full` stored;
  `optOutOfAnalytics()` turns product analytics off and remembers it in
  `forge.analytics_opt_out`. `analyticsTier()` reports the tier. `track()`
  warns once on the reserved prefixes `auth_`, `page_`, `flag_`, `web_`.
- `trackExposure(flag, variant, { provider })` records one `flag_evaluated`
  per person, flag and variant per UTC day; `forgeExposureHook` does it for
  every OpenFeature evaluation (`OpenFeature.addHooks(forgeExposureHook)`).

- Page views and the signed-in user, both off unless passed (forge's generated
  `otel_gen.ts` passes them): `pageViews: true | { basePath, routeName }`
  records one `page_viewed {route}` per route, the pathname with the query,
  fragment and basePath cut and id-looking segments as `:id`
  (`normalizeRoute` in the barrel); `identify: { sessionUrl }` asks that
  endpoint (an `/auth/session` answering `{ authenticated, subject }`, with the
  cookie) at start, on a move into or out of `/auth/*` and when the tab is
  shown again, and passes `subject` (never an `@`-shaped one) to
  `setTelemetryUser`. The first view of a page load carries `entry: "true"`
  and its external `referrer_host` / `referrer_path` (`siteHosts` names hosts
  that are this site); a URL's `utm_*`, `ref` and `click_id` (the ad
  network's name, never the click id) ride along when present. Each view
  also ends in one `page_engaged {route, view_id, engaged_ms, scroll_pct}`: the time
  the page was visible, focused and in use (a 30 s idle cutoff, capped at 30
  minutes) and the deepest document scroll, reported once at the next route
  change, hide or `pagehide` and exported at once with `fetch` keepalive; a
  view the reader returns to reports again, under the same random
  `view_id`, with `resumed: "true"` and the cumulative figures (`pageViews: { engagement: false }` turns it off).
  `authenticated:
false`, a 401 or 403, and any answer naming nobody (another non-2xx, a
  foreign body, a network error) clear the user it set, once; before it has
  set one they change nothing. Both stop on `disableTelemetry()`.

- Attribution (`appClient`, `visitorIdStorageKey`, `withAttribution(url)`): init
  stamps `app.client` (`electron` inside Electron, else `web`; pass `"landing"`
  from a marketing site) on every event and, in the `full` tier only,
  `visitor.id`, and reads `rid` and
  `utm_source|medium|campaign|term|content` from the URL into localStorage. The
  first `setTelemetryUser(id)` per user also emits one `user_identified` event
  with `visitor_id` and the stored utm values. `withAttribution(url)` appends
  `rid` and the utm values to an outbound absolute URL, and is the identity
  before init or after `disableTelemetry()`. A visit carrying new values
  replaces the stored ones (last touch); `attributionTouch: "first"` keeps the
  first for good, for a marketing site attributing acquisition.

  `attribution` and `enableAttribution({ search })` (from `/telemetry`) are
  deprecated spellings that keep working: passing `attribution` at all means
  mode `full`, `true` also grants consent at start, and `enableAttribution()`
  raises the default mode to `full` and grants. `search` is the landing URL's
  query string kept from page load, for when a client-side navigation has
  dropped it since. `revokeAnalyticsConsent()` erases the stored ids on
  withdrawal; `getAttribution()` is the captured record, for a site that
  stamps campaign values on its own events.

- Client context: init puts the low-entropy client hints of
  `navigator.userAgentData` on the resource (`browser.brands` as
  `"Google Chrome 131, Chromium 131"` with GREASE brands dropped,
  `browser.platform`, `browser.mobile`), and the viewport and screen in CSS
  pixels on every span (`app.viewport.width|height`, `app.screen.width|height`,
  restamped once a resize settles). Every value is validated and a malformed
  one is dropped. Browser, OS and device type are deliberately not reported:
  the ingest side derives them from the request's `User-Agent` and `Sec-CH-UA*`
  headers, which page script cannot set. A desktop app's preload exposes its
  versions as `window.forgeDesktop` (`contextBridge.exposeInMainWorld("forgeDesktop",
{ appVersion, electronVersion: process.versions.electron, chromiumVersion:
process.versions.chrome })`), which init stamps as `app.desktop.version`,
  `app.desktop.electron.version` and `app.desktop.chromium.version`; `/node`
  stamps the same three on an Electron main process (`app.desktop.version` is
  its `serviceVersion`).

- Benign browser errors (`classifyBenignError`, `forge.errors_filtered`): an
  `AbortError` (a cancelled request, an unmounted component, a navigation), a
  `TimeoutError` (`AbortSignal.timeout()` elapsing) and
  the browser's generic fetch failure (a `TypeError` reading exactly `Failed to
fetch`, `Load failed` or `NetworkError when attempting to fetch resource.`)
  are kept out of the error pipeline, whether they arrive as an unhandled
  rejection, an uncaught error, `console.error` or `reportException`. A
  `ConnectError` is never filtered. Each filtered error is counted, and the
  counts go out as `forge.errors_filtered` actions (`kind`, `source`, `count`)
  at most once a minute and when the page is hidden, so a real wave of network
  failures still shows. The filter acts on what the SDK RECORDS, never on what
  the app sees: `window.onerror`, `onunhandledrejection`, every `error` /
  `unhandledrejection` listener and the console receive every event exactly as
  without telemetry. `classifyBenignError: () => null` records everything; any
  `(error) => kind | null` replaces the default (for an unhandled rejection or
  uncaught error it is handed `{ name, message }` as the SDK recorded them).

Privacy is forced, not configurable: when replay records, all text and inputs
are masked, canvas is never recorded, `img, picture, video, audio, iframe,
object, embed` are blocked, images are not inlined and fonts are not collected.
Request and response bodies and headers are not captured.

No URL leaves the browser with its query string or fragment, which is where
emails, tokens, OAuth codes and search terms live. The SDK stamps the page URL
on every span (`location.href`) and on documentLoad spans (`http.url`,
`document.referrer`), the request URL on fetch/XHR and resource spans
(`http.url`, `url.full`), and, when its interaction instrumentation is on, the
page URL on click spans (`http.url`) and the previous one on routeChange spans
(`prev.href`). Right after init the runtime wraps the SDK's span
exporter, so every URL-valued attribute of a span, a span event, a link or the
resource is exported without them, whichever instrumentation recorded it:
attributes named by the conventions and the SDK (`url.full`, `http.url`,
`http.target`, `location.href`, `prev.href`, `page.url`, `document.referrer`,
`target_src`, …) or ending in `href`, `url`, `uri`, `src` or `referrer`.
`url.query` and `url.fragment` are dropped. Free text that merely contains a URL
(a console message, a stack) is not rewritten, and the recorded spans are not
modified; only what is serialized changes. Nothing is kept by default;
`keepUrlQueryParams: ["utm_*", "ref"]` keeps the named parameters (exact names,
or prefixes ending in `*`; case-sensitive) and never the fragment. An app that
runs its own OTel SDK gets the same with
`new BatchSpanProcessor(scrubbingSpanExporter(exporter))`, and `scrubUrl` /
`scrubUrlAttributes` for its own attributes. The platform's ingest scrubs the
same attributes again, as defence in depth.

Session replay gets the same treatment. Its opening record (rrweb's Meta event,
sent again with every full snapshot) holds the page URL as `data.href` in a log
body rather than in a span attribute, so the recorder is given an rrweb plugin
that strips the query and fragment from it, with the same `keepUrlQueryParams`,
before the event is serialized. URLs inside the recorded DOM (links,
stylesheets) are page content the replay renders, and are left as they are.
An app that runs rrweb itself can pass `replayUrlScrubPlugin()` in its
`plugins`.

## Auth-provider adapters (`/auth/<provider>`)

An app on Supabase, Clerk, Auth0 or Firebase reports who is signed in with one
line. Each subpath maps the provider's callbacks onto `identifyUser` (the id on
every span and replay, and the daily `resumed` sign-in) and
`trackAuthEvent("signed_out", …)`, naming the provider on each event. They are
structurally typed and import nothing from the provider's SDK, so they add no
dependency, and they inherit the auth events' GPC/DNT and tier rules.

| Subpath          | Call                                                                                                                                                         | Notes                                                                                                                                                                                                                              |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `/auth/supabase` | `createSupabaseAuthTelemetry(supabase, { anonymous? })`, or `supabaseAuthTelemetryListener()` inside your own `onAuthStateChange`                            | `INITIAL_SESSION` / `SIGNED_IN` / `TOKEN_REFRESHED` / `USER_UPDATED` identify; `SIGNED_OUT` signs out. Anonymous sessions are skipped by default (`anonymous: "identify"` attaches them) and identified once `is_anonymous` flips. |
| `/auth/clerk`    | `createClerkAuthTelemetry(clerk)`                                                                                                                            | Over `clerk.addListener`.                                                                                                                                                                                                          |
| `/auth/auth0`    | `await syncAuth0(client)` on load and after `handleRedirectCallback`; `await reportAuth0SignOut(client)` before `logout()`                                   | auth0-spa-js has no listener.                                                                                                                                                                                                      |
| `/auth/firebase` | `createFirebaseAuthTelemetry((cb) => onIdTokenChanged(auth, cb))`; `reportFirebaseSignIn(cred.user, getAdditionalUserInfo(cred))`; `reportFirebaseSignOut()` | `isNewUser` gives an exact `signed_up`. Anonymous users are never counted.                                                                                                                                                         |

A browser `SIGNED_IN` is not a credential sign-in (supabase-js emits it on tab
refocus), so the adapters never send one: exact sign-ups and credential
sign-ins come from the provider itself, through `forge/pkg/authhooks`.

## Interactions: click to usable

The SDK times each RPC. What the user waits for is usually several RPCs, a
stream's first frame and a render: "open a chat" is a click, a stream, a
snapshot, a React commit and a paint. `interaction()` (barrel) is one span for
that whole operation:

```ts
import { interaction, useInteractionEnd } from "@reliantlabs/forge-web-runtime";

const op = interaction("chat.open", { "chat.id": chatId }); // on intent
op.mark("snapshot_received"); // optional phase markers (span events)
op.endAfterPaint({ outcome: "ok" }); // when the screen is usable
// or, in the component that knows: useInteractionEnd(op, transcriptLoaded);
```

The result is one root span `chat.open` (kind INTERNAL, attributes
`interaction.name`, `interaction.scope`, `interaction.outcome`) whose children
are the browser fetch spans of every RPC made while it was open, which in turn
parent the server spans.

```ts
interaction(name: string, attributes?: InteractionAttributes, options?: { scope?: string; timeoutMs?: number }): Interaction
currentInteraction(scope?: string): Interaction | undefined
interactionContextInterceptor(): Interceptor // from the barrel or /interceptors
useInteractionEnd(op: Interaction | null | undefined, ready: boolean, attributes?: InteractionEndAttributes): void

interface Interaction {
  readonly name: string; readonly scope: string;
  readonly traceId: string; readonly ended: boolean;
  setAttributes(attributes: InteractionAttributes): void;
  mark(name: string, attributes?: InteractionAttributes): void;
  end(attributes?: InteractionEndAttributes): void;          // { outcome?: string, ...attrs }
  endAfterPaint(attributes?: InteractionEndAttributes): void;
  run<T>(fn: () => T): T;
}
```

- **Foreground, not async context.** A browser has no async context:
  `context.with` holds a span only until the first `await`. The interceptor
  stack already carries the CALLER's context across its own awaits, so a span
  the caller had active when it made the call (an explicitly traced action,
  or `op.run()`) stays the parent. Most RPCs of an operation, though, start in
  effects, query functions and stream hooks with no span active at all. So the
  runtime holds the open interactions, at most one per `scope` (default
  `foreground`), and a call made with no active span joins the most recently
  started one. Starting an interaction in a busy scope ends the old one with
  outcome `superseded`.
- **Chosen at call entry.** The choice is made when the call enters the
  interceptor chain — connect-es runs the outermost interceptor synchronously
  inside the client call — never after an await, so a call held by auth's
  `getToken()` (or any awaiting stage) while the user opens something else
  stays in the interaction it started under. Every runtime interceptor stamps
  the call on entry if no earlier one has, so `buildRuntimeInterceptors()` and
  any chain whose outermost stage is a runtime interceptor (e.g.
  `errorNormalizeInterceptor`) are correct as they are. A chain whose
  outermost stages are the app's own and await before any runtime stage runs
  puts `interactionContextInterceptor()` first.
- **The fetch span is the child, not just the traceparent.** The SDK opens
  its fetch span from `context.active()` at the instant `fetch()` is called.
  The interceptor calls `next()` inside the interaction's context, which
  covers connect-web's unary path (fetch is synchronous with `next()`); a
  server stream awaits its request body first, so `initBrowserTelemetry`
  installs a thin wrapper over the SDK's patched fetch that re-enters the
  context the interceptor recorded for that request.
- **Ending.** `end()` ends now; `endAfterPaint()` ends after the next painted
  frame (two `requestAnimationFrame`s), or at once with
  `interaction.paint: "skipped"` when the page is hidden or there is no rAF.
  The first end wins. `outcome` defaults to `ok`; `error` also sets the span
  status. An interaction nobody ends ends after `timeoutMs` (default 60s) as
  `timeout`. A page hidden while one is open stamps `page.hidden: true`.
- **Telemetry off.** With no SDK it records nothing, but carries a real
  synthesized trace id, so its RPCs still share one trace on the server. After
  `disableTelemetry()` it records nothing either. Nothing here throws.

## Node and Electron-main telemetry (`/node`)

`initNodeTelemetry` is the browser entry's twin for a Node process — an
Electron app's main process, a CLI. It ships logs, errors and spans as
OTLP/JSON to the same ingest, normally the keyed one
(`https://<ingest host>/k/<key>`), using Node built-ins only: no
OpenTelemetry SDK packages to install beside `@opentelemetry/api`.

```js
import { app } from "electron";
import {
  flushOnQuit,
  frontendServiceName,
  initNodeTelemetry,
} from "@reliantlabs/forge-web-runtime/node";

const telemetry = initNodeTelemetry({
  serviceName: frontendServiceName("shop", "desktop"), // "<project>-<frontend>", as forge derives it
  serviceVersion: app.getVersion(),
  environment: "prod",
  appClient: "electron",
  endpoint: "https://ingest.example.dev/k/pk_live_…", // unset, "" or "off": telemetry is off
  origin: "app://bundle", // the keyed ingest admits only an allowlisted Origin
  appRoot: app.getAppPath(), // stack paths are reported relative to it
});
flushOnQuit(app); // hold before-quit until the queue is out (2s at most)

telemetry?.logger.warn("renderer gone", { reason: "oom" });
telemetry?.reportError(err, { "settings.section": "privacy" });
telemetry?.tracer.startActiveSpan("backend.start", (span) => {
  /* … */ span.end();
});
```

- **Uncaught exceptions and unhandled rejections** become ERROR records with
  `exception.type`, `exception.message`, `exception.stacktrace` and
  `code.function.name` — the keys forge/pkg/observe writes for a server error —
  and no `rum.sessionId`, so `forge obs issues` groups them with sfp1.
  `code.function.name` is the innermost application frame (not Node, Electron or
  `node_modules`), written `fn (src/file.js)`.
- **The process's fate is unchanged.** Exceptions are watched with
  `uncaughtExceptionMonitor`. When the runtime's `unhandledRejection` listener is
  the only one, it flushes, then raises the rejection as Node would have
  (`--unhandled-rejections` is honoured). In plain Node with no
  `uncaughtException` handler, it flushes and then exits as Node does; under
  Electron nothing is installed there. `captureProcessErrors: false` turns all
  of this off.
- **Batching:** records wait up to `batchDelayMs` (5s) or `maxBatchSize` (256);
  at most `maxQueueSize` (2048) per signal are held. Timers are unref'd. A fatal
  record is sent at once, and an idle process flushes on `beforeExit`.
  `flush(ms)` / `shutdown(ms)` resolve by their timeout whatever the network
  does. An app that already defers `before-quit` for its own shutdown awaits
  `telemetry.flush()` as the last step of it instead of calling `flushOnQuit`.
- **Privacy:** an exception keeps its message's first line, with credential
  shapes redacted (the patterns `track()` uses) and at most 1024 chars; the
  stack is rebuilt from that line and its frames, with the home directory, the
  install path and `app.asar` removed from every path. String attributes and
  bodies are redacted and bounded, and object-valued attributes are dropped.
  `setUser(id)` stamps the opaque `userId` the browser entry stamps. Nothing
  else about the machine is sent: no host name, IP, argv or environment.
- **Auth events:** `trackAuthEvent(kind, { userId, method?, provider? … })`
  from `/node` writes the standard `auth_signed_up` / `auth_signed_in` /
  `auth_signed_out` product event as the INFO log record forge/pkg/observe's
  `TrackAuth` writes (`app.client` is `backend` unless `appClient` is set), for
  a Next.js route handler or an Express backend completing an Auth0 or Clerk
  callback. A no-op before init.
- The global `@opentelemetry/api` tracer, AsyncLocalStorage context manager and
  W3C `traceparent` propagator are this runtime's unless `registerGlobal: false`.
  `reportException` from the barrel reports through it once it is running.

## The dev servers' `/_otel` proxy (`/vite`, `/otel-proxy`)

In the dev loop the page sends OTLP/HTTP to `/_otel/v1/{traces,logs}` on its
own origin, and the dev server forwards it to `OTEL_EXPORTER_OTLP_ENDPOINT`.
On a hosted dev stack it also attaches the stack's token from
`FORGE_OTEL_HEADERS_FILE`, read on every request. Anything that reaches the
dev server can reach that proxy, any website open in the developer's browser
included, so it forwards only what the page's own exporter sends:

- a same-origin caller: `Origin` must name the host the browser addressed
  (`Host`; for the Next.js relay, the `X-Forwarded-Host` Next's rewrite sets
  from it), and `Sec-Fetch-Site`, when sent, must be `same-origin`. A foreign
  or missing `Origin`, any of those headers sent twice, or a host value that
  is not exactly one `host[:port]` is 403, before the method is checked or
  the body read, and the connection is closed. Loopback is not
  required: a dev server opened through a workspace preview URL, a tunnel or
  a LAN address works, because same-origin holds there too.
- `POST` to `/v1/traces`, `/v1/logs` and `/v1/metrics` under the prefix. Any
  other method is 405 and any other path 404, compared byte for byte, so
  traversal and percent-encoded spellings never match.
- bodies up to 8 MiB, counted as they stream in and buffered before the
  upstream is contacted; above that, 413.
- only the page's `content-type` and `content-encoding`, plus the token:
  never its cookies, `authorization` or anything else, and never a
  `set-cookie` back.

None of the refusals reaches the upstream. With no upstream, a valid POST is
answered 204 and every other request is refused the same way.

```ts
// vite.config.ts: a plugin; requests outside /_otel pass through.
import { forgeOtelProxy } from "@reliantlabs/forge-web-runtime/vite";
plugins: [react(), forgeOtelProxy({ upstream: "http://127.0.0.1:4318" })];

// next.config.ts: a rewrite cannot filter or add a header, so rewrite
// /_otel/:path* to a loopback relay.
import { startOtelRelay } from "@reliantlabs/forge-web-runtime/otel-proxy";
const relay = await startOtelRelay({ upstream: "http://127.0.0.1:4318" });
```

`createOtelProxyHandler` is the shared handler, for any other Node server.

## The DOM-free interceptors entry point

```ts
import {
  buildRuntimeInterceptors, // the Connect transport stack
  ConnectClientError, // what the stack throws
  setTraceSampled, // traceInterceptor's knob
} from "@reliantlabs/forge-web-runtime/interceptors";
```

The `./interceptors` subpath is the transport layer on its own: `src/errors.ts`
and `src/trace.ts` are the only modules it reaches. It imports **no React and
no `@types/react`**, so a consumer never typechecks components it does not
render.

That is what keeps the package honest on React Native. A mobile app renders
none of the components here — they emit `<table>`, `<input>`, `<div>` — so
reaching them would drag a bundle's worth of dead code and a React binding
onto a platform whose renderer is not react-dom. The subpath skips them
entirely. `src/interceptors.ts` is guarded by
`src/interceptors-entry.test.ts`, which walks the entry point's real import
graph and fails if React ever appears in it.

(This subpath was _also_ the workaround for React 18 vs 19 type skew, back
when the package published its sources. That is no longer why it exists — see
"One `.d.ts` for React 18 and 19" above; the barrel typechecks fine from Expo
now. It stays because dead components in a mobile bundle is a separate and
still-real cost.)

React Native resolvers predate `exports`: Expo SDK 52 / React Native 0.76 runs
Metro with `unstable_enablePackageExports` off, and `expo/tsconfig.base` sets
`moduleResolution: node10`. Neither reads the `exports` map. The
`interceptors/package.json` directory shim is what they resolve instead — same
build output, reached the old way. Delete it and mobile breaks while web keeps
working.

## Money and basis points

The web counterpart of `forge/pkg/money`. An amount is an integer count of
its currency's minor unit (protobuf-es hands an `int64` over as a `bigint`)
plus an ISO 4217 code, and no float sits anywhere between the wire and the
screen.

```ts
formatMinorUnits(123456n); // "$1,234.56"
formatMinorUnits(1234n, { currency: "JPY" }); // "¥1,234"
formatMinorUnits(123456n, { currency: "EUR", locale: "de-DE" }); // "1.234,56 €"
parseMinorUnits("$1,234.5"); // 123450n
parseMinorUnits("1.234"); // null: USD has two minor digits, so no rounding
minorUnitsToInput(123450n); // "1234.50", parses back exactly
formatBasisPoints(825); // "8.25%"
parseBasisPoints("8.25%"); // 825
```

Minor digits come from Intl (JPY 0, KWD 3). Formatting stays exact past 2^53.
The parsers return `null` rather than guessing, so a form validates on that.
`src/money.ts`'s header lists the full rules.

## The `--color-*` contract

Several components render semantic Tailwind utilities. They are part of this
package's public contract: the consuming app's stylesheet MUST declare these
custom properties in its `@theme` block, and Tailwind v4 must be told to scan
this package — **it does not scan `node_modules`**:

```css
/* frontends/<name>/src/app/globals.css (Next.js) */
@source "../../node_modules/@reliantlabs/forge-web-runtime";

/* frontends/<name>/src/index.css (Vite SPA) */
@source "../node_modules/@reliantlabs/forge-web-runtime";
```

Without that directive every utility only this package uses is dropped from
the built CSS and the components render unstyled.

Required tokens:

| Token                    | Used for                         |
| ------------------------ | -------------------------------- |
| `--color-surface`        | table / panel background         |
| `--color-surface-muted`  | table head, row hover            |
| `--color-border`         | table and panel rules            |
| `--color-ink`            | primary text                     |
| `--color-ink-muted`      | secondary text, empty states     |
| `--color-ink-subtle`     | placeholders, disabled controls  |
| `--color-accent`         | focus ring, active filter border |
| `--color-danger`         | error text, destructive fill     |
| `--color-danger-hover`   | destructive fill hover           |
| `--color-danger-surface` | error panel background           |
| `--color-danger-border`  | error panel border               |
| `--color-danger-ink`     | error text on a danger surface   |
| `--color-on-danger`      | text on a danger fill            |

## Local development

A dev forge build writes an npm `file:` dependency on this directory into a
generated frontend's `package.json`, so npm creates and maintains the link
itself and edits here are picked up with nothing published. The path is
written relative or home-anchored, never absolute. A release build writes an
ordinary version range instead.

`forge project libraries` prints where the package resolved to and what every
module exports — read it instead of searching the disk.

### The live-edit loop

The bridge resolves `dist/`, so **run `npm run dev` here while you edit** —
`tsc --watch`, a few hundred milliseconds per incremental build. The linked
app's bundler is already watching this directory (Metro via `watchFolders`,
Next via `transpilePackages`, Vite because a linked dep is treated as source),
so a save still lands in the running app; it just arrives one compile later.

You do not have to remember to build before the _first_ install. `prepare`
(`scripts/build.mjs`) runs when npm materialises the `file:` link and again
before `npm publish` — the two moments a consumable `dist/` has to exist. On a
checkout that has never been developed it installs this package's own
devDependencies once so `tsc` is there at all, because npm does not install a
link target's devDependencies and forge's CI never runs `npm install` here.
The script never runs for a registry install: the tarball already has `dist/`.

`npm test` builds first (`pretest`), so `src/published-surface.test.ts` always
asserts against a current artifact rather than whatever was last on disk.
