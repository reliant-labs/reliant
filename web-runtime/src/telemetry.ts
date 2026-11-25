// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Browser telemetry: the HyperDX browser SDK, initialised the forge way.
//
// Captures errors (uncaught, unhandled rejections, boundary-caught), console
// output, fetch/XHR spans, document load, long tasks and web vitals, and
// ships them to ClickStack through a SAME-ORIGIN `/_otel` route. Product
// analytics ride on it in a tier the app and the person choose, cookieless
// by default (see "Analytics tiers" below).
//
// ── Why HyperDX REPLACES the old OTel web wiring, not joins it ───────────
//
// The SDK is @hyperdx/otel-web, driven through the runtime's port of
// @hyperdx/browser's wrapper (sdk-client.ts). It builds its own
// WebTracerProvider and calls provider.register() — the global tracer
// provider, context manager and propagator. A second registration (the old
// `./otel` subpath did exactly that) is rejected by @opentelemetry/api with a
// "duplicate registration" diagnostic and silently loses: whichever ran
// second would simply never see a span. Two tracer providers cannot coexist
// in one page, so there is one, and it is HyperDX's. `./otel` and its eight
// @opentelemetry/* SDK peer dependencies are gone.
//
// traceparent on RPCs is unaffected. traceInterceptor (trace.ts) injects the
// active span's context into every Connect call, or synthesises a valid
// traceparent when none is active — with no SDK at all. With the SDK running,
// the fetch span HyperDX opens for the call carries the same trace id, and
// `apiOrigins` below makes that hold for a cross-origin API too (which is
// every dev loop: the API binds its own port).
//
// ── The browser holds no secret ──────────────────────────────────────────
//
// `url` is `${location.origin}/_otel`. The browser talks only to its own
// origin; a dev-server proxy (next.config.ts rewrites / Vite server.proxy) or
// the deployment's ingress forwards `/_otel/v1/{traces,logs}` to the OTLP/HTTP
// port of a collector, and that hop — never the bundle — owns credentials.
//
// The SDK nevertheless wants an `apiKey` and sends it as an `authorization`
// header. We pass PLACEHOLDER_API_KEY, a constant that grants nothing and is
// public by construction. A collector that enforces ingestion auth (ClickStack
// `collectorAuthenticationEnforced`) rejects it with 401, so the proxy in
// front of such a collector must strip the header or replace it with the real
// key. The local ClickStack collector does not enforce, and ignores it.
//
// ── Replay: shipped, off, masked, media blocked ─────────────────────────
//
// Replay is rrweb, recorded by the runtime (replay-recorder.ts). It is
// DISABLED unless `replay: true`.
// Privacy is forced, not offered: every input and all text is masked, canvas is
// never recorded, and images, video, audio, iframes and embeds are BLOCKED
// (replaced by a same-size placeholder; their pixels never leave the browser).
// Images are not inlined and web fonts are not collected. There is deliberately
// no option to turn any of that off while replay is on; an app that needs
// unmasked replay passes its own SDK options.
//
// `replaySampleRate` (0..1, default 1) records only that share of SESSIONS. The
// SDK has no session sampler, so the decision is made here, once, and held in
// sessionStorage for the tab's session. Replay runs only in the `full`
// analytics tier (below): it is per-person recording, which the cookieless
// tiers never do.
//
// ── Replay costs the page little ─────────────────────────────────────────
//
// The recorder HyperDX's SDK bundled did its export on the main thread: every
// 5 s it protobuf-encoded and gzipped the batch, which on a phone cost every
// tap 15–45% and stalled scrolling for 300–400 ms every few seconds. The
// runtime's recorder (replay-recorder.ts) loads rrweb in its own chunk only
// when replay runs, encodes batches as OTLP/JSON and gzips them in a Worker,
// sends every REPLAY_FLUSH_INTERVAL_MS (`replayFlushIntervalMs`) and whenever
// the page is hidden or left (keepalive), and with `replayStart:
// "interaction"` records nothing until the person first interacts. rrweb
// itself still serializes mutations and input on the main thread, so
// continuous input is sampled (REPLAY_SAMPLING: the pointer at half rate,
// scroll positions each 150 ms, a field's final value rather than every
// keystroke) and snapshots leave out scripts, comments and head metadata
// (rrweb slimDOM). `replaySampling` and `replaySlimDOM: false` take either
// back.
//
// ── Withdrawal stops replay now ──────────────────────────────────────────
//
// When the tier leaves `full` (revokeAnalyticsConsent, optOutOfAnalytics) or
// telemetry is disabled, no replay leaves the page from that instant: not a
// record made afterwards, not one made before it and still buffered, and not
// a span from stopping the recorder. The recorder's stop() is final: rrweb
// stops, the records it held are dropped, a batch still being encoded is
// never sent, and it emits nothing on the way out (replay-recorder.ts,
// "Withdrawal"). It is stopped once however many paths ask. Replay that
// already left before the withdrawal stays where it is.
//
// ── One session per visit, even where cookies are dropped ────────────────
//
// The SDK keeps its session id only in the `__rum_sid` cookie and mints a new
// one whenever it cannot read that cookie back, which it checks every minute.
// Below the `full` analytics tier the cookie is answered from memory instead
// (see "Analytics tiers"); in `full`, an origin that drops cookies (an Electron renderer on a custom scheme such
// as `app://`) therefore got a new session every minute. Before init, the
// runtime probes whether cookies persist and, when they do not, keeps that one
// cookie in localStorage with the same 15-minute inactivity and 4-hour limits
// (session-cookie-fallback.ts). Origins whose cookies work are untouched.
//
// ── No session until the page has been seen ──────────────────────────────
//
// A page loaded in the background (a desktop window opened hidden, a tab
// restored behind another) starts no session at all: the SDK is not loaded
// until the document is first visible. Started hidden, it would mint a session
// made only of background fetch spans, with nothing on screen to record, and
// the recorder would hold no replay for it. Until then reportException returns
// false (the caller logs to the console) and track() calls are held, bounded,
// as they are while the SDK loads. A page that never becomes visible sends
// nothing. An RPC does not start it either: until the page has been seen, an
// RPC is told not to wait for telemetry and carries a synthesized traceparent
// (telemetry-gate.ts).
//
// ── Paint vitals are dropped for a page first shown late ─────────────────
//
// web-vitals reports no FCP or LCP painted after the page was first hidden,
// but it learns of hiding only from the moment it registers, and it registers
// inside the SDK, which starts only once the page is visible. So a page loaded
// in the background would report, as its FCP and LCP, the time from navigation
// until it was first SHOWN: a tab opened behind another for a minute reported
// a 60 s LCP. The runtime records when the page was first hidden (the browser's
// `visibility-state` entries where it has them, else from startup on) and
// drops an FCP or LCP painted at or after that moment. The paint time is the
// metric's value (web-vitals times it from navigation, less a prerendered
// page's activationStart); a back/forward-cache restore, timed from the
// restore, is kept. TTFB, CLS and INP are kept: being hidden does not change
// them.
//
// ── No URL leaves the browser with its query string or fragment ──────────
//
// The query and the fragment are where emails, tokens, OAuth codes and search
// terms live, and the SDK stamps the page URL on every span: `location.href`
// on each one, `http.url` and `document.referrer` on documentLoad, the
// request URL on fetch/XHR and resource spans, and, when its interaction
// instrumentation is on, `http.url` on click spans and `prev.href` on
// routeChange spans. Right after init the runtime wraps the SDK's span
// exporter (url-scrub.ts, installSpanExportScrub), so every URL-valued span,
// span-event, link and resource attribute is exported without its query and
// fragment, whatever instrumentation recorded it; `url.query` and
// `url.fragment` are dropped. `keepUrlQueryParams` (off by default) names
// query parameters to keep (`utm_*`). fetch/XHR spans are also stripped as
// they end (finishNetworkSpan), before the span is named from the URL. The
// ingest side scrubs the same attributes again, as defence in depth, for an
// older runtime and for anything that reaches it some other way.
//
// Session replay carries the page URL too, outside any span: rrweb's Meta
// event, the opening record of a recording, holds `location.href` as
// `data.href` in a log body. The recorder gets an rrweb plugin
// (url-scrub.ts, replayUrlScrubPlugin) that strips it the same way, with the
// same `keepUrlQueryParams`, before the event is serialized.
//
// ── Network spans are named by what they called ──────────────────────────
//
// HyperDX names every fetch/XHR span `HTTP <METHOD>`, so a trace a browser
// starts is titled "HTTP POST" whatever it did. The same hook renames it to
// the procedure when it is a Connect/gRPC call (`POST /pkg.v1.Service/Method`,
// the server span's name with the method in front; spanRouteName). Any other
// URL keeps `HTTP <METHOD>`: a free-form path can carry ids and tokens, and
// the span name is exported unscrubbed.
//
// ── Analytics tiers: cookieless by default ───────────────────────────────
//
// `analytics` is the app's ceiling: "off", "aggregate", "identified" (the
// default) or "full". The tier the browser actually runs in is decided in track.ts
// ("Analytics tiers"): below `full` NOTHING is stored on the device. The SDK's
// `__rum_sid` session cookie is answered from memory
// (session-cookie-fallback.ts, installRumSessionStore), there is no visitor
// id, no campaign is stored, no replay runs and no sampling decision is kept.
// Visitors are counted by the platform's ingest edge (a daily-salted hash it
// computes and forgets), never by anything the page computes. `full` needs
// mode "full" AND grantAnalyticsConsent(): then the session cookie, the
// visitor id, attribution and (from the next page load) replay come back,
// and revokeAnalyticsConsent() / optOutOfAnalytics() take them away again
// and erase what was stored. GPC / DNT cap the browser at `aggregate`.
//
// ── Page views and the signed-in user, when asked ────────────────────────
//
// `pageViews` records a `page_viewed {route}` product event per route
// (page-views.ts) and, unless `pageViews.engagement` is false, one
// `page_engaged {route, view_id, engaged_ms, scroll_pct}` per page view when it ends
// (page-engagement.ts); `identify` finds the signed-in user from the API's
// `/auth/session` and stamps their opaque id on every event
// (session-identity.ts). Both are OFF in this API, so a hand-written caller
// sees no change; forge's generated otel_gen.ts turns them on, and an env
// turns them off with PAGE_VIEWS / IDENTIFY_USER = "false" in its runtime
// config. Both start only once the SDK runs and stop on disableTelemetry().
//
// ── One click, one span ──────────────────────────────────────────────────
//
// The SDK's interaction instrumentation stays off: it opened a span per
// listener an event reached, mousedown and mouseup included, and none for the
// listeners React added before the SDK loaded. Instead the runtime records one
// `click` span per real click from the moment initBrowserTelemetry is called,
// held until the SDK runs, with the attributes the SDK's click spans carried
// (click-capture.ts). `clickCapture: false` turns it off; the `off` analytics
// tier records none.
//
// ── Client context ───────────────────────────────────────────────────────
//
// The page reports what only it can know about its client — client hints
// (browser.brands/platform/mobile), its viewport and screen (app.viewport.*,
// app.screen.*) and, inside a desktop app, the app's versions from the
// `window.forgeDesktop` preload bridge (app.desktop.*) — sanitized
// (client-context.ts). Browser, OS and device type are NOT page claims: the
// ingest side derives them from the request's User-Agent and Sec-CH-UA
// headers and overwrites any the page sent.
//
// ── Loading ──────────────────────────────────────────────────────────────
//
// The SDK (@hyperdx/otel-web, ~430 KB minified) is imported lazily, so
// bundlers split it into its own chunk; rrweb (~180 KB) is a second chunk,
// loaded only when replay runs.
// initBrowserTelemetry is therefore async and idempotent — every caller gets
// the same promise, which is what survives React StrictMode's double mount and
// Fast Refresh. It is a no-op without a DOM (SSR, prerender) and without an
// endpoint, and it never throws.
//
// Evaluating that chunk is one long main-thread task (~540 ms, plus ~170 ms
// of init, at 4x CPU on a phone-class emulation of reliantlabs.io, when it was
// @hyperdx/browser's single UMD file; about half that as otel-web's ESM). Started as
// soon as the page was visible it ran right after first paint, inside
// hydration, and a tap in that window waited ~1.1 s instead of ~0.4 s. So by
// default (`start: "idle"`) the SDK starts once the page has loaded, in the
// browser's first idle period or right after the first interaction has
// painted, or at once when an RPC waits for it (telemetry-start.ts,
// telemetry-gate.ts). Its startup yields between stages, so the chunk's
// evaluation, the SDK's init and the recording of what was held are separate
// tasks. `start: "eager"` starts as soon as the page is visible.
//
// Nothing is lost by starting later. From the call on, the runtime holds,
// bounded, what the SDK would have recorded and records it once it runs:
// uncaught errors, unhandled rejections and failed resource loads
// (early-errors.ts), errors handed to reportException, clicks
// (click-capture.ts), and track() calls, page views included (the route watch
// starts when the page is first visible). Clicks, track() calls and failed
// resource loads are recorded at the time they happened; a held exception is
// recorded when the SDK runs, with `forge.error.delay_ms` saying how much
// earlier it happened. Web vitals, long tasks and the document load need no
// holding: the SDK reads them from the browser's buffered performance
// timeline when it starts. Console output from before the SDK runs is not
// captured.
import { trace } from "@opentelemetry/api";

import {
  captureAttribution,
  type AttributionTouch,
  detectAppClient,
  stopAttribution,
  takeIdentifyProps,
} from "./attribution.js";
import {
  type BenignErrorClassifier,
  type BenignErrorSource,
  classifyBenignError,
  FilteredErrorCounter,
} from "./benign-errors.js";
import {
  pageClientResourceAttributes,
  watchViewport,
} from "./client-context.js";
import {
  CLICK_SCOPE,
  CLICK_SPAN_NAME,
  type ClickCapture,
  type ClickRecord,
  captureClicks,
  elementXPath,
} from "./click-capture.js";
import {
  type EarlyErrors,
  type HeldError,
  holdEarlyErrors,
} from "./early-errors.js";
import {
  setExceptionReporter,
  setPendingExceptionHolder,
} from "./error-reporter.js";
import {
  FRAME_STALL_SCOPE,
  FRAME_STALL_SPAN_NAME,
  type FrameStall,
  type FrameStallWatch,
  watchFrameStalls,
} from "./frame-stalls.js";
import { installFetchContext } from "./interaction-context.js";
import {
  REPLAY_FLUSH_INTERVAL_MS,
  type ReplayStart,
} from "./replay-recorder.js";
import { type HyperDXClient, loadSdkClient } from "./sdk-client.js";
import { installPageViews, type PageViewOptions } from "./page-views.js";
import {
  type IdentifyOptions,
  type SessionIdentity,
  watchSessionIdentity,
} from "./session-identity.js";
import {
  installRumSessionStore,
  type RumSessionStore,
} from "./session-cookie-fallback.js";
import {
  registerRumSession,
  registerTelemetryStartup,
  rumSessionId,
  setRpcTelemetry,
} from "./telemetry-gate.js";
import {
  type StartGate,
  startGate,
  type TelemetryStart,
  yieldToMain,
} from "./telemetry-start.js";
import {
  installSpanExportScrub,
  type QueryParamFilter,
  queryParamFilter,
  replayUrlScrubPlugin,
  scrubUrl,
} from "./url-scrub.js";
import {
  type AnalyticsMode,
  analyticsTier,
  browserPrivacyOptOut,
  currentAnalyticsMode,
  eraseAnalyticsDeviceState,
  grantAnalyticsConsent,
  isTelemetryDisabled,
  onAnalyticsTierChange,
  onTelemetryDisabled,
  setAnalyticsMode,
  setTelemetrySink,
  stampedTelemetryUser,
  type TrackValue,
} from "./track.js";

export {
  clientResourceAttributes,
  type ForgeDesktopInfo,
  viewportAttributes,
} from "./client-context.js";

export {
  cookiesPersist,
  installRumSessionStore,
  installSessionCookieFallback,
  RUM_SESSION_COOKIE,
  RUM_SESSION_STORAGE_KEY,
  type RumSessionBackend,
  type RumSessionStore,
} from "./session-cookie-fallback.js";

export { type PageViewOptions } from "./page-views.js";
export {
  REPLAY_FLUSH_INTERVAL_MS,
  type ReplayStart,
} from "./replay-recorder.js";
export {
  START_IDLE_TIMEOUT_MS,
  START_LOAD_TIMEOUT_MS,
  type TelemetryStart,
} from "./telemetry-start.js";
export { type IdentifyOptions } from "./session-identity.js";

export {
  isUrlAttribute,
  scrubbingSpanExporter,
  scrubUrl,
  scrubUrlAttributes,
  queryParamFilter,
  replayUrlScrubPlugin,
  type QueryParamFilter,
  type ReplayRecordPlugin,
  type SpanExporterLike,
  type TelemetryUrlScrubOptions,
} from "./url-scrub.js";

export {
  type BenignErrorClassifier,
  type BenignErrorKind,
  classifyBenignError,
  FILTERED_ERRORS_ACTION,
} from "./benign-errors.js";

/** Where the browser ships telemetry: this origin's `/_otel`. */
export const DEFAULT_INGEST_PATH = "/_otel";

/**
 * The apiKey handed to the SDK. Public by construction: it identifies
 * nothing and authorises nothing. See the header — the proxy or collector
 * owns the real credential.
 */
export const PLACEHOLDER_API_KEY = "forge-browser-public";

export interface BrowserTelemetryConfig {
  /** `service.name` on everything this page emits. */
  serviceName: string;
  /** `service.version`. Omit for none. */
  serviceVersion?: string;
  /**
   * The forge environment name. Emitted as BOTH `deployment.environment.name`
   * (current semconv) and `deployment.environment` (the legacy key HyperDX's
   * default sources still key on).
   */
  environment?: string;
  /** `service.namespace`, conventionally the project name. */
  serviceNamespace?: string;
  /**
   * The forge frontend's name, emitted as the `forge.frontend` resource
   * attribute. Unlike `serviceName` (which an app may override to anything) it
   * is the stable key that joins this page's telemetry to the frontend and its
   * source maps. Omit for none.
   */
  frontend?: string;
  /**
   * Ingest base: a path on this origin (`/_otel`) or, as an escape hatch, an
   * absolute URL. Unset, empty, or `off` disables telemetry — the app runs
   * untraced rather than throwing, which is what makes it safe to call
   * unconditionally from the app shell.
   */
  endpoint?: string;
  /**
   * Origins (or URLs — only the origin is used) of the app's own backend.
   * Requests to them always get a `traceparent` from the SDK's fetch/XHR
   * spans. This page's own origin is always included.
   */
  apiOrigins?: Array<string | undefined>;
  /**
   * Mirror console.log/info/warn/debug into telemetry spans. Default true;
   * pass false when console output can hold user content. It does NOT stop
   * `console.error(err)` from being recorded as an error: the SDK's error
   * instrumentation is separate and always on.
   */
  consoleCapture?: boolean;
  /**
   * Capture full request/response headers AND BODIES on fetch/XHR spans.
   * Default false, and it should stay false: bodies are user data.
   */
  advancedNetworkCapture?: boolean;
  /**
   * Query parameters kept in the URLs telemetry exports: exact names (`ref`)
   * or prefixes ending in `*` (`utm_*`), matched case-sensitively on the
   * decoded name. Default none: every URL-valued attribute (location.href,
   * http.url, url.full, prev.href, document.referrer, …), and the page URL in
   * a session replay's Meta records, is exported without its query string,
   * and always without its fragment. See "No URL leaves the
   * browser with its query string or fragment" in the header.
   */
  keepUrlQueryParams?: string[];
  /**
   * Session replay (rrweb). Default false. When true, all text and inputs are
   * masked, media is blocked and canvas is not recorded (see the header).
   * It runs only in the `full` analytics tier (`analytics: "full"` and
   * grantAnalyticsConsent() before this call): replay records a person, which
   * the cookieless tiers never do. Consent granted later in the page load
   * starts it on the next one. Consent withdrawn stops it at once, and what
   * the recorder had buffered is dropped, not sent.
   */
  replay?: boolean;
  /**
   * The share of sessions to record when `replay` is true, 0..1. Default 1
   * (every session). Decided once per session and held in sessionStorage. A
   * value that is not a number records nothing. An app with its own policy
   * (every session of a new user, a fraction of the rest) computes the rate
   * it wants for this session and passes it here.
   */
  replaySampleRate?: number;
  /**
   * Record one `click` span per real click (click-capture.ts): the clicked
   * control's tag and XPath and the page URL, never its text. Default true.
   * Clicks made while the SDK loads are held and recorded once it runs. The
   * `off` analytics tier records none.
   */
  clickCapture?: boolean;
  /**
   * When the SDK starts (see "Loading" in the header). "idle" (the default):
   * once the page has loaded, in the browser's first idle period or right
   * after the first interaction has painted, whichever is first, or at once
   * when an RPC waits for it. "eager": as soon as the page is visible, as
   * every release before this option did. Either way nothing is lost: errors,
   * clicks, page views and track() calls made before it starts are held and
   * recorded once it runs.
   */
  start?: TelemetryStart;
  /**
   * Record a `frame.stall` span for each frame the page took 50 ms or more
   * to paint, while loading and for a few seconds after each input
   * (frame-stalls.ts). It is how an iPhone, whose WebKit has no Long Tasks
   * API, reports jank at all. Default true.
   */
  frameStalls?: boolean;
  /**
   * Attach a redacted `forge.forensics` event to the span of a failed RPC:
   * the request's shape, never its content (rpc-forensics.ts: numbers,
   * bools, enums and id-named strings as values, unless the name also names
   * a secret, like `session_id`; every other string, bytes, list and map as
   * a length; `debug_redact` fields never), once per
   * procedure and error code per page load. Default true; forge's generated
   * src/lib/otel_gen.ts passes false when the runtime config says
   * `RPC_FORENSICS = "false"`. The RPC spans themselves (rpc-span.ts) are
   * recorded either way.
   */
  rpcForensics?: boolean;
  /**
   * How finely session replay records continuous input, as rrweb's
   * `sampling` (mousemove and scroll throttles in ms, `input: "last"` to keep
   * only a field's final value, …). Each key given replaces the runtime's
   * default for it (REPLAY_SAMPLING). Ignored without replay.
   */
  replaySampling?: ReplaySampling;
  /**
   * Leave out of replay snapshots what a replay never shows: scripts,
   * comments, head metadata and whitespace (rrweb `slimDOMOptions: "all"`).
   * Default true; false records the DOM whole. Ignored without replay.
   */
  replaySlimDOM?: boolean;
  /**
   * When session replay starts recording, with `replay` on. "load" (the
   * default): as soon as the SDK runs. "interaction": not until the person
   * first taps, clicks or types; rrweb is not even loaded before then, and
   * the recording starts from the page as it is at that moment.
   */
  replayStart?: ReplayStart;
  /**
   * How often (ms) recorded replay is sent. Default REPLAY_FLUSH_INTERVAL_MS
   * (15 s); a batch also goes within a second once 48 KB are held, and
   * whenever the page is hidden or left.
   */
  replayFlushIntervalMs?: number;
  /** Verbose SDK diagnostics to the console. Default false. */
  debug?: boolean;
  /**
   * The `app.client` stamped on every event: `web`, `electron` or `landing`
   * (any short string is accepted). Unset, it is `electron` inside Electron
   * and `web` elsewhere.
   */
  appClient?: string;
  /**
   * The app's analytics ceiling (track.ts, "Analytics tiers"), ordered
   * off < aggregate < identified < full:
   *
   *   "identified" (default) cookieless: product events with no persistent
   *                          id and nothing stored on the device; the
   *                          signed-in user's opaque id rides along once one
   *                          is named.
   *   "aggregate"            as identified, but strictly anonymous: no user
   *                          id on browser events, even when signed in.
   *   "full"                 as identified until the app calls
   *                          grantAnalyticsConsent(); then a persistent
   *                          visitor id, the RUM session cookie, campaign
   *                          attribution across days and replay.
   *   "off"                  no product events and no user id; errors and
   *                          traces still flow.
   *
   * GPC / DNT cap any mode at aggregate, with no user id. forge's generated
   * otel_gen.ts passes the runtime config's ANALYTICS_MODE (absent means
   * "identified").
   */
  analytics?: AnalyticsMode;
  /**
   * localStorage key holding this browser's anonymous visitor id (stamped as
   * `visitor.id`) in the `full` tier. Set it to adopt an id a site already
   * keeps, e.g. a landing page's `reliant.visitor_id`. Default
   * `forge.visitor_id`. A `rid` in the landing URL wins over it.
   */
  visitorIdStorageKey?: string;
  /**
   * `last` (default): a visit carrying `rid`/`utm_*` replaces the stored
   * record. `first`: the first stored record is kept for good — a marketing
   * site attributing acquisition to the campaign that first brought a visitor.
   */
  attributionTouch?: AttributionTouch;
  /**
   * Decides which errors are benign browser noise: kept out of the error
   * pipeline and counted as `forge.errors_filtered` actions instead
   * (benign-errors.ts). Default classifyBenignError: an AbortError or TimeoutError, and the
   * browser's generic "Failed to fetch" TypeError; never a ConnectError. It
   * applies to what the SDK records from unhandled rejections, uncaught
   * errors, console.error and reportException; the app's own handlers see
   * every event unchanged. For an unhandled rejection or uncaught error it is
   * handed `{ name, message }` as the SDK recorded them, not the original
   * value. Pass `() => null` to record everything.
   */
  classifyBenignError?: BenignErrorClassifier;
  /**
   * Record a `page_viewed {route}` product event for each route the user
   * lands on (page-views.ts): the pathname only, basePath stripped, ids
   * replaced by `:id`, consecutive repeats dropped. Default false; forge's
   * generated src/lib/otel_gen.ts turns it on unless the env's runtime
   * config says `PAGE_VIEWS = "false"`. Pass options to strip a basePath or
   * name routes yourself.
   */
  pageViews?: boolean | PageViewOptions;
  /**
   * Find the signed-in user by asking `sessionUrl` (the API's
   * `/auth/session`, with the cookie) and pass its `subject` to
   * setTelemetryUser, again on a move into or out of `/auth/*` and when the
   * tab becomes visible (session-identity.ts). Unset (the default), nothing
   * is asked; forge's generated src/lib/otel_gen.ts sets it for a frontend
   * with native sign-in unless the runtime config says
   * `IDENTIFY_USER = "false"`. An app that calls setTelemetryUser from its
   * own IdP leaves it unset.
   */
  identify?: IdentifyOptions;
}

let started: Promise<boolean> | null = null;

// Attribution state shared by start() and enableAttribution(): whichever runs
// second does the stamping, so the call order does not matter.
let attributionWanted = false;
let attributionOptions: { search?: string } = {};
let runningConfig: BrowserTelemetryConfig | null = null;
let runningClient: HyperDXClient | null = null;
// setTelemetryAttributes calls made before the SDK runs, applied when it does.
let heldAttributes: Record<string, string> = {};

/**
 * Stamp `attributes` on every span from here on: the app's own dimensions
 * (a build id, a deployment cohort) beside the runtime's `app.client`. Values
 * are strings; an empty string clears a key. Safe to call before
 * initBrowserTelemetry resolves (held, then applied when the SDK starts), more
 * than once (keys merge, the latest value wins), and on the server (a no-op).
 * Use it instead of reaching for the SDK: the runtime owns the SDK's client.
 */
export function setTelemetryAttributes(
  attributes: Record<string, string>,
): void {
  if (typeof window === "undefined" || isTelemetryDisabled()) {
    return;
  }
  const clean: Record<string, string> = {};
  for (const [key, value] of Object.entries(attributes ?? {})) {
    if (typeof key === "string" && key !== "" && typeof value === "string") {
      clean[key] = value;
    }
  }
  if (Object.keys(clean).length === 0) {
    return;
  }
  if (runningClient) {
    try {
      runningClient.setGlobalAttributes?.(clean);
    } catch {
      // Telemetry must never break the app.
    }
    return;
  }
  heldAttributes = { ...heldAttributes, ...clean };
}

function captureVisitor(config: BrowserTelemetryConfig): string {
  return captureAttribution({
    search: attributionOptions.search,
    visitorIdStorageKey: config.visitorIdStorageKey,
    touch: config.attributionTouch,
  });
}

/**
 * @deprecated Use grantAnalyticsConsent(). Kept working: it raises the
 * default "identified" mode to "full" (a caller asking for attribution has said it
 * wants the persistent visitor id) and grants consent, so from here on every
 * event carries `visitor.id`, withAttribution stamps links and the first
 * sign-in emits `user_identified`. A mode of "off" or "aggregate" (an app's
 * explicit choice) stays as it is, and GPC / DNT
 * still cap the browser at aggregate. Pass `search` (the landing URL's query
 * string, kept from page load) when a client-side navigation may have
 * dropped it since. Safe to call before init resolves, more than once, and
 * on the server; a no-op after disableTelemetry().
 */
export function enableAttribution(options?: { search?: string }): void {
  if (typeof window === "undefined" || isTelemetryDisabled()) {
    return;
  }
  if (options?.search !== undefined) {
    attributionOptions = { search: options.search };
  }
  if (currentAnalyticsMode() === "identified") {
    setAnalyticsMode("full");
  }
  grantAnalyticsConsent();
}

// The mode a config asks for: `analytics`, else the cookieless default,
// "identified".
function configuredMode(config: BrowserTelemetryConfig): AnalyticsMode {
  const mode = config.analytics;
  if (
    mode === "off" ||
    mode === "aggregate" ||
    mode === "identified" ||
    mode === "full"
  ) {
    return mode;
  }
  return "identified";
}

// Start or stop the full tier's device state on a running SDK as the tier
// moves: the session moves to the device and back, the visitor id is
// stamped or blanked, and the first sign-in is identified.
function applyTier(
  tier: string,
  previous: string,
  hdx: HyperDXClient,
  config: BrowserTelemetryConfig,
  rum: RumSessionStore | null,
  stopReplay: () => void,
): void {
  if (tier !== "full") {
    // First, before anything else moves: replay records a person, and it
    // never outlives the full tier (see "Withdrawal stops replay now").
    stopReplay();
  }
  if (tier === "full" && previous !== "full") {
    rum?.moveToDevice(localStorageOrNull());
    attributionWanted = true;
    hdx.setGlobalAttributes?.({ "visitor.id": captureVisitor(config) });
    const user = stampedTelemetryUser();
    const identify = user ? takeIdentifyProps(user) : null;
    if (identify) {
      hdx.addAction?.("user_identified", identify);
    }
    return;
  }
  if (tier !== "full") {
    rum?.moveToMemory();
    if (attributionWanted || previous === "full") {
      attributionWanted = false;
      stopAttribution();
      hdx.setGlobalAttributes?.({ "visitor.id": "" });
    }
  }
}

/**
 * initBrowserTelemetry starts the HyperDX browser SDK once per page load and
 * resolves to whether it is running. See the module header for the contract.
 */
export function initBrowserTelemetry(
  config: BrowserTelemetryConfig,
): Promise<boolean> {
  if (typeof window === "undefined") {
    return Promise.resolve(false);
  }
  if (started) {
    return started;
  }
  if (isTelemetryDisabled()) {
    return Promise.resolve(false);
  }
  // Before anything is held or replayed: the tier decides what track() and
  // setTelemetryUser may send. A mode raised before init (enableAttribution,
  // documented safe to call before init, lifts the default "identified" to
  // "full") is never lowered back by the default: an app that hands the
  // landing attribution over and then starts telemetry keeps its visitor id.
  // An explicit configured mode still wins ("off" and "aggregate" are the
  // app's choice, as in enableAttribution).
  const configured = configuredMode(config);
  setAnalyticsMode(
    configured === "identified" && currentAnalyticsMode() === "full"
      ? "full"
      : configured,
  );
  if (currentAnalyticsMode() !== "full") {
    // Ids an earlier page load stored (before analytics were cookieless by
    // default, or under a mode since lowered) are not kept by a mode that
    // can never reach `full`.
    eraseAnalyticsDeviceState();
  }
  const ingest = resolveIngestUrl(config.endpoint, window.location.origin);
  if (!ingest) {
    // Nothing will ever drain track() calls held for the SDK; let them go.
    setTelemetrySink(null);
    return Promise.resolve(false);
  }
  // Before the SDK loads, so a click or an error while it does is not lost.
  const clicks =
    config.clickCapture === false
      ? null
      : captureClicks(
          () => !isTelemetryDisabled() && analyticsTier() !== "off",
          queryParamFilter(config.keepUrlQueryParams),
        );
  const errors = holdEarlyErrors();
  if (errors) {
    setPendingExceptionHolder((error, attributes) =>
      errors.hold(error, attributes),
    );
  }
  const stalls =
    config.frameStalls === false
      ? null
      : watchFrameStalls(() => !isTelemetryDisabled());
  const gate = startGate(config.start);
  const visible = firstVisible();
  // What the RPC span stage of the interceptor chain records (rpc-span.ts).
  setRpcTelemetry({ spans: true, forensics: config.rpcForensics !== false });
  started = start(config, ingest, { clicks, errors, stalls, gate, visible });
  // An RPC that waits for telemetry starts it now (telemetry-gate.ts), unless
  // the page has never been visible: no session starts before it has been
  // seen, so the RPC is told not to wait.
  registerTelemetryStartup(started, () => {
    if (!visible.seen()) {
      return false;
    }
    gate.startNow();
    return true;
  });
  return started;
}

/** What initBrowserTelemetry set going before the SDK loads. */
interface EarlyCapture {
  clicks: ClickCapture | null;
  errors: EarlyErrors | null;
  stalls: FrameStallWatch | null;
  gate: StartGate;
  visible: FirstVisible;
}

async function start(
  config: BrowserTelemetryConfig,
  ingest: string,
  { clicks, errors, stalls, gate, visible }: EarlyCapture,
): Promise<boolean> {
  // Before the first await: until web-vitals registers in hdx.init, only this
  // watch sees the page hidden (see "Paint vitals" in the header).
  const hidden = watchFirstHidden();
  let identity: SessionIdentity | null = null;
  let stopRoutes = () => {};
  let stopWaiting = () => {};
  // Telemetry is not starting after all: let go of everything held for it.
  const abandon = () => {
    stopWaiting();
    visible.stop();
    hidden.stop();
    gate.cancel();
    clicks?.stop();
    stalls?.stop();
    errors?.stop();
    setPendingExceptionHolder(null);
    stopRoutes();
    setTelemetrySink(null);
  };
  // Until the SDK's sink is installed, disableTelemetry() reaches this
  // startup only here: the waits below end on it rather than on a visibility
  // change, a load or an idle period that may never come (a hidden page).
  const disabledWhileWaiting = new Promise<void>((resolve) => {
    stopWaiting = onTelemetryDisabled(resolve);
  });
  try {
    await Promise.race([visible.ready, disabledWhileWaiting]);
    if (isTelemetryDisabled()) {
      abandon();
      return false;
    }
    // Page views from the moment the page is seen, not from when the SDK
    // runs: the views and engaged time of a visitor who moves on, or leaves,
    // before then are held like any track() call.
    stopRoutes = startRouteWatch(config, (route, previous) =>
      identity?.onRoute(route, previous),
    );
    await Promise.race([gate.ready, disabledWhileWaiting]);
    stopWaiting();
    if (isTelemetryDisabled()) {
      abandon();
      return false;
    }
    const hdx = await loadSdkClient();
    // Evaluating the chunk was one task; the SDK's init gets its own.
    await yieldToMain();
    if (!hdx || isTelemetryDisabled()) {
      abandon();
      return false;
    }
    // Replay records a person: only in the full tier, which is also the only
    // one allowed to keep the sampling decision in sessionStorage.
    const replay =
      analyticsTier() === "full" &&
      decideReplay(config, sessionStorageOrNull(), Math.random);
    // Before init: the SDK reads its session cookie as it starts. In memory
    // below the full tier; on the device in it.
    const rum = keepRumSession();
    // Before init: the fetch/XHR hook reads it from the first request on.
    keepQuery = queryParamFilter(config.keepUrlQueryParams);
    hdx.init(
      buildSdkOptions(
        { ...config, replay },
        ingest,
        window.location.origin,
        pageClientResourceAttributes(),
      ),
    );
    // Straight after init, in the same task: the SDK's batch is exported on a
    // timer, so every span it has recorded so far is still held and goes out
    // scrubbed too (see "No URL leaves the browser" in the header).
    scrubSdkUrls(config);
    // Over the SDK's patched fetch, so an RPC whose fetch() runs after an
    // await (a server stream) still opens its span under the context the
    // trace interceptor chose. See interaction-context.ts.
    installFetchContext();
    // From init on, so the gates hold whatever the recorder does after a
    // withdrawal (see "Withdrawal stops replay now").
    let replayStopped = false;
    const stopReplay = () => {
      if (replayStopped) {
        return;
      }
      replayStopped = true;
      try {
        hdx.stopSessionRecorder?.();
      } catch {
        // Telemetry must never break the app.
      }
    };
    // From here web-vitals watches visibility itself.
    hidden.stop();
    dropBackgroundPaintVitals(hidden.at());
    // After init: both hook what the SDK RECORDS, never what the app sees.
    const benign = benignFilter(config, hdx);
    filterSdkErrorSpans(benign);
    tapSdkInputs(benign);
    // Every span carries where it came from and, in the full tier, which
    // visitor made it; the product-event view reads these as app.client and
    // visitor.id. Below the full tier there is no visitor id: visitor.id
    // joins when grantAnalyticsConsent() raises the tier.
    attributionWanted = analyticsTier() === "full";
    const globals: Record<string, string> = {
      "app.client": detectAppClient(config.appClient),
    };
    if (attributionWanted) {
      globals["visitor.id"] = captureVisitor(config);
    }
    hdx.setGlobalAttributes?.({ ...heldAttributes, ...globals });
    heldAttributes = {};
    // The viewport and screen ride on every span, refreshed on resize
    // (client-context.ts): the SDK's global attributes are a span processor
    // that stamps the current set on each span as it starts, so a span carries
    // the size of the moment it began (proved against the real SDK in
    // scripts/browser-telemetry-smoke.mjs). Device, OS and browser are
    // derived at ingest.
    const stopViewport = watchViewport((attrs) =>
      hdx.setGlobalAttributes?.(attrs),
    );
    runningClient = hdx;
    runningConfig = config;
    // The session id rides on every Connect call as baggage (trace.ts) and on
    // every fetch/XHR span (sessionAttribute), joining backend traces to the
    // session that caused them.
    registerRumSession(() => hdx.getSessionId?.());
    const stopTier = onAnalyticsTierChange((tier, previous) =>
      applyTier(tier, previous, hdx, config, rum, stopReplay),
    );
    // The SDK's own listeners record from here on; the early ones stop, in
    // the same task, so nothing falls between them or is recorded twice.
    const held = errors?.take() ?? [];
    setPendingExceptionHolder(null);
    setExceptionReporter((error, attributes) => {
      if (alreadyRecorded(error) || benign.filtered(error, "reportException")) {
        return;
      }
      hdx.recordException(error, attributes);
    });
    setTelemetrySink({
      // A held event is recorded at the time it happened.
      track: (name, attributes, at) =>
        at === undefined
          ? hdx.addAction?.(name, attributes)
          : recordHeldAction(name, attributes, at),
      setUser: (id) => {
        hdx.setGlobalAttributes?.({ userId: id });
        // The first sign-in of a user in this browser links the visitor to
        // them: one event, never repeated for the same user.
        const identify = takeIdentifyProps(id);
        if (identify) {
          hdx.addAction?.("user_identified", identify);
        }
      },
      // The org later events are about (setTelemetryOrg), beside userId.
      setOrg: (id) => hdx.setGlobalAttributes?.({ orgId: id }),
      // The page is being hidden or unloaded: export the batch now. The
      // SDK's exporter posts with fetch keepalive, which outlives the page.
      flush: () => flushSdk(),
      disable: () => {
        runningClient = null;
        stopReplay();
        // The SDK's tracer provider stays registered: the RPC span stage
        // stops recording on its own.
        setRpcTelemetry({ spans: false, forensics: false });
        // Back to "no SDK": reportException returns false, so a caller falls
        // back to console.error and the error does not vanish.
        setExceptionReporter(null);
        registerRumSession(null);
        stopViewport();
        stopTier();
        stopRoutes();
        identity?.stop();
        clicks?.stop();
        stalls?.stop();
      },
    });
    // What was held is recorded in tasks of its own.
    await yieldToMain();
    if (isTelemetryDisabled()) {
      return false;
    }
    await recordHeldErrors(held, hdx, benign);
    // The clicks held while the SDK loaded, then every later one.
    clicks?.attach(recordClick);
    // Likewise the frame stalls.
    stalls?.attach(recordFrameStall);
    // After the sink, so the user goes straight to the SDK. Off unless the
    // caller asked (otel_gen.ts does).
    identity = startIdentity(config);
    return true;
  } catch (err) {
    // Blocked chunk, ad-blocker, SDK throw: telemetry must never break the app.
    // A startup that failed after the hooks went in takes them out again.
    abandon();
    removeBenignFilter();
    // A warning, not a debug line. The page goes on without a single span,
    // and a debug line hid that: a test runner that could not load the SDK
    // looked exactly like a working one that had nothing to send.
    console.warn(
      "[telemetry] failed to start; this page records no spans:",
      err,
    );
    return false;
  }
}

/** The tracer the SDK records track() (addAction) on. */
const SDK_ACTION_SCOPE = "custom-action";

/**
 * A track() call held while the SDK loaded, recorded as the SDK's addAction
 * records one, but at the time it was made.
 */
function recordHeldAction(
  name: string,
  attributes: Record<string, TrackValue>,
  at: number,
): void {
  const span = trace
    .getTracer(SDK_ACTION_SCOPE)
    .startSpan(name, { startTime: at, attributes });
  span.end(at);
}

/** Held errors recorded per task before the next yield. */
const HELD_ERRORS_PER_TASK = 5;

/** Each held error's source, as the SDK names the span that records it. */
const HELD_ERROR_SOURCE: Record<string, string> = {
  error: "onerror",
  unhandledrejection: "unhandledrejection",
  reportException: "reportException",
};

/**
 * Records the errors held before the SDK ran, through the same filters as
 * one it records itself: benign ones are counted, not recorded, and one value
 * is recorded once however many ways it was reported. Each carries
 * `forge.error.source` (onerror, unhandledrejection, reportException) and
 * `forge.error.delay_ms`, how long before the SDK ran it happened.
 */
async function recordHeldErrors(
  held: HeldError[],
  hdx: HyperDXClient,
  benign: BenignFilter,
): Promise<void> {
  for (let i = 0; i < held.length; i++) {
    if (i > 0 && i % HELD_ERRORS_PER_TASK === 0) {
      await yieldToMain();
      if (isTelemetryDisabled()) {
        return;
      }
    }
    const error = held[i]!;
    try {
      if (error.kind === "resource") {
        recordHeldResourceError(error.target, error.at);
        continue;
      }
      if (
        alreadyRecorded(error.value) ||
        benign.filtered(error.value, error.kind)
      ) {
        continue;
      }
      noteSeen(error.value);
      hdx.recordException(error.value, {
        ...error.attributes,
        "forge.error.source": HELD_ERROR_SOURCE[error.kind] ?? error.kind,
        "forge.error.delay_ms": Math.max(0, Date.now() - error.at),
      });
    } catch {
      // Telemetry must never break the app.
    }
  }
}

/**
 * A resource that failed to load before the SDK ran, recorded as the SDK
 * records one (its `eventListener.error` span on the `errors` tracer), at the
 * time it failed. target_src is scrubbed at export like any URL.
 */
function recordHeldResourceError(target: Element, at: number): void {
  const attributes: Record<string, string> = {
    component: "error",
    "error.type": "error",
    target_element: target.tagName,
    target_xpath: elementXPath(target),
  };
  const src = (target as { src?: unknown }).src;
  if (typeof src === "string" && src !== "") {
    attributes.target_src = src;
  }
  const span = trace
    .getTracer(SDK_ERRORS_SCOPE.name, SDK_ERRORS_SCOPE.version)
    .startSpan("eventListener.error", { startTime: at, attributes });
  span.end(at);
}

/**
 * One click as a span, at the moment it happened: started and ended at the
 * event's timestamp, and never made active, so it parents nothing.
 */
function recordClick(click: ClickRecord): void {
  const span = trace.getTracer(CLICK_SCOPE).startSpan(CLICK_SPAN_NAME, {
    startTime: click.at,
    attributes: click.attributes,
  });
  span.end(click.at);
}

/** One frame stall as a span, from the last frame to the late one. */
function recordFrameStall(stall: FrameStall): void {
  const span = trace
    .getTracer(FRAME_STALL_SCOPE)
    .startSpan(FRAME_STALL_SPAN_NAME, {
      startTime: stall.start,
      attributes: {
        component: "frame-stall",
        "frame.gap_ms": Math.round(stall.gap),
        "frame.trigger": stall.trigger,
      },
    });
  span.end(stall.start + stall.gap);
}

/** The slice of the SDK's tracer provider flushSdk calls. */
interface FlushableProvider {
  getDelegate?(): unknown;
  forceFlush?(): Promise<void> | void;
}

// Export every finished span the SDK holds, now. The SDK registers its
// provider globally, behind the API's proxy; the delegate is the provider
// whose span processors hold the batch.
function flushSdk(): void {
  try {
    const proxy = trace.getTracerProvider() as FlushableProvider;
    const provider = (proxy.getDelegate?.() ?? proxy) as FlushableProvider;
    const done = provider.forceFlush?.();
    if (done && typeof done.catch === "function") {
      done.catch(() => {});
    }
  } catch {
    // Telemetry must never break the app.
  }
}

// The session watch, when `identify` names a session endpoint.
function startIdentity(config: BrowserTelemetryConfig): SessionIdentity | null {
  const url = config.identify?.sessionUrl;
  if (typeof url !== "string" || url.trim() === "") {
    return null;
  }
  try {
    return watchSessionIdentity({ sessionUrl: url });
  } catch {
    return null;
  }
}

// The route watch, when there are page views to record or a session watch
// (identify) that re-asks on leaving the sign-in screens. Returns its
// teardown. `onRoute` reaches the session watch once it runs.
function startRouteWatch(
  config: BrowserTelemetryConfig,
  onRoute: (route: string, previous: string | null) => void,
): () => void {
  const pageViews = config.pageViews ?? false;
  const identify = config.identify?.sessionUrl;
  const identity = typeof identify === "string" && identify.trim() !== "";
  if (pageViews === false && !identity) {
    return () => {};
  }
  const options = typeof pageViews === "object" ? pageViews : {};
  try {
    return installPageViews({
      basePath: options.basePath,
      routeName: options.routeName,
      siteHosts: options.siteHosts,
      pageViews: pageViews !== false,
      engagement: pageViews !== false && options.engagement !== false,
      onRoute: identity ? onRoute : undefined,
    });
  } catch {
    return () => {};
  }
}

/** Whether the document has been visible yet, as firstVisible watches it. */
interface FirstVisible {
  /** Resolves once the document has been visible. */
  readonly ready: Promise<void>;
  /** True from the moment the document has been visible. */
  seen(): boolean;
  /** Stops watching (ready then never resolves); its listener goes with it. */
  stop(): void;
}

/**
 * Watches for the document to be visible: seen at once for a visible page or
 * one with no Page Visibility API, otherwise at its first visibilitychange to
 * visible. See "No session until the page has been seen" in the header.
 */
function firstVisible(): FirstVisible {
  if (
    typeof document === "undefined" ||
    document.visibilityState !== "hidden" ||
    typeof document.addEventListener !== "function"
  ) {
    return { ready: Promise.resolve(), seen: () => true, stop: () => {} };
  }
  let seen = false;
  const onChange = () => {
    if (document.visibilityState !== "hidden") {
      stop();
      seen = true;
      resolveReady();
    }
  };
  const stop = () => document.removeEventListener("visibilitychange", onChange);
  let resolveReady = () => {};
  const ready = new Promise<void>((resolve) => {
    resolveReady = resolve;
  });
  document.addEventListener("visibilitychange", onChange);
  return { ready, seen: () => seen, stop };
}

/** When the page was first hidden, as watchFirstHidden saw it. */
interface HiddenWatch {
  /** ms since the time origin, or Infinity while the page has not been hidden. */
  at(): number;
  stop(): void;
}

/**
 * Watches for the page's first hidden moment from now until stop(). The
 * browser's `visibility-state` entries (Chromium) go back to navigation; where
 * there are none, a page hidden now counts as hidden from now on, and a later
 * visibilitychange to hidden from then. See "Paint vitals" in the header.
 */
function watchFirstHidden(): HiddenWatch {
  let at = Number.POSITIVE_INFINITY;
  const now = () =>
    typeof performance !== "undefined" && typeof performance.now === "function"
      ? performance.now()
      : 0;
  try {
    const entries =
      typeof performance !== "undefined" &&
      typeof performance.getEntriesByType === "function"
        ? performance.getEntriesByType("visibility-state")
        : [];
    // A prerendered page is hidden until it is activated, as web-vitals
    // also ignores: only hiding after activation counts.
    const activated = activationStart();
    for (const e of entries) {
      if (e.name === "hidden" && e.startTime >= activated) {
        at = Math.min(at, e.startTime);
      }
    }
  } catch {
    // No such entry type here: the document's state below decides.
  }
  if (typeof document === "undefined") {
    return { at: () => at, stop: () => {} };
  }
  const hiddenNow = () =>
    document.visibilityState === "hidden" &&
    !(document as { prerendering?: boolean }).prerendering;
  if (hiddenNow()) {
    at = Math.min(at, now());
  }
  if (typeof document.addEventListener !== "function") {
    return { at: () => at, stop: () => {} };
  }
  const onChange = () => {
    if (hiddenNow()) {
      at = Math.min(at, now());
    }
  };
  document.addEventListener("visibilitychange", onChange);
  return {
    at: () => at,
    stop: () => document.removeEventListener?.("visibilitychange", onChange),
  };
}

/** The tracer (name) the SDK's web-vitals instrumentation records under. */
const SDK_WEBVITALS_SCOPE = "webvitals";

/** The web vitals timed from navigation to a paint: wrong for a hidden page. */
const PAINT_VITALS = ["fcp", "lcp"] as const;

/** The span attribute the SDK copies web-vitals' metric.navigationType into. */
const SDK_WEBVITALS_NAV_TYPE = "webvitals.navigation_type";

/**
 * When an FCP or LCP span's paint happened, in ms since the time origin (the
 * coordinates of hiddenAt), or undefined when its value is not timed from
 * navigation. The SDK (@hyperdx/otel-web webvitals.js) sets the attribute to
 * web-vitals' metric.value, which web-vitals 3 computes as the paint entry's
 * startTime minus activationStart (0 except for a prerendered page); so the
 * paint time is the value plus activationStart. A page restored from the
 * back/forward cache instead reports the time since the restore, which says
 * nothing about when the first load was hidden.
 */
function paintTime(
  value: number,
  attributes: Record<string, unknown>,
): number | undefined {
  const navigationType = attributes[SDK_WEBVITALS_NAV_TYPE];
  if (navigationType === "back-forward-cache") {
    return undefined;
  }
  return navigationType === "prerender" ? value + activationStart() : value;
}

/** The navigation's activationStart (a prerendered page), else 0. */
function activationStart(): number {
  try {
    const [nav] =
      typeof performance !== "undefined" &&
      typeof performance.getEntriesByType === "function"
        ? (performance.getEntriesByType("navigation") as Array<{
            activationStart?: unknown;
          }>)
        : [];
    const at = nav?.activationStart;
    return typeof at === "number" && Number.isFinite(at) ? at : 0;
  } catch {
    return 0;
  }
}

/**
 * Drops an FCP or LCP span whose paint came at or after hiddenAt (ms since the
 * time origin): it measured how long the page waited to be shown. A no-op for
 * a page that was never hidden.
 */
function dropBackgroundPaintVitals(hiddenAt: number): void {
  if (!Number.isFinite(hiddenAt)) {
    return;
  }
  const proxy = trace.getTracerProvider() as { getDelegate?: () => unknown };
  const provider = (proxy.getDelegate?.() ?? proxy) as {
    getTracer?: (name: string, version?: string) => unknown;
  };
  const tracer = provider.getTracer?.(SDK_WEBVITALS_SCOPE) as
    | StartSpanTracer
    | undefined;
  if (!tracer || typeof tracer.startSpan !== "function") {
    return;
  }
  const original = tracer.startSpan;
  const startSpan = function (
    this: unknown,
    name: string,
    ...rest: unknown[]
  ): unknown {
    const span = original.call(this, name, ...rest) as RecordingSpan;
    if (typeof span?.end !== "function") {
      return span;
    }
    const end = span.end;
    span.end = function (this: unknown, ...args: unknown[]) {
      const attributes = span.attributes ?? {};
      for (const vital of PAINT_VITALS) {
        const value = attributes[vital];
        if (typeof value !== "number") {
          continue;
        }
        const paintedAt = paintTime(value, attributes);
        if (paintedAt !== undefined && paintedAt >= hiddenAt) {
          // Never ended, so never exported.
          return undefined;
        }
      }
      return end.apply(this, args);
    };
    return span;
  };
  tracer.startSpan = startSpan;
  benignTeardown.push(() => {
    if (tracer.startSpan === startSpan) {
      tracer.startSpan = original;
    }
  });
}

// ── One failure, one record ──────────────────────────────────────────────
//
// The SDK records an Error that reaches window "error", window
// "unhandledrejection", or console.error. React (18 and 19) console.errors an
// error a boundary catches BEFORE it calls componentDidCatch, and Next
// dispatches an uncaught render error to window "error" — so by the time
// reportException runs, the SDK has usually recorded that very error already.
// Recording it again would count one failure twice, and the issue layer
// (which groups by fingerprint) would report two.
//
// So the taps below remember which Error OBJECTS the SDK was shown, and the
// installed recorder skips those. Whatever the SDK did not see — a custom
// onCaughtError that silences React's logging, an error rethrown as a new
// object — is still recorded. The console tap is installed after the SDK's own
// wrapper, so it sits outermost and only observes; every call is forwarded
// untouched.
//
// The cost is the React component stack: the SDK's record of a boundary-caught
// error comes from console.error(error), which carries the error and its
// stack but not React's componentStack, and attributes passed to a skipped
// reportException are dropped with it.
const seenBySdk = new WeakSet<object>();
let tapped = false;

function noteSeen(value: unknown): void {
  if (typeof value === "object" && value !== null) {
    seenBySdk.add(value);
  }
}

function tapSdkInputs(benign: BenignFilter): void {
  if (tapped) {
    return;
  }
  tapped = true;
  const wrapped = console.error;
  const tap = function (this: unknown, ...args: unknown[]) {
    for (const arg of args) {
      try {
        if (arg instanceof Error) {
          noteSeen(arg);
        }
      } catch {
        // A revoked Proxy: not ours to inspect; forward it untouched.
      }
    }
    // The call always goes on to the SDK and the console, exactly as without
    // this tap. Only the verdict on the SDK's RECORD of it is decided here,
    // where the real arguments are: the SDK opens its console.error span
    // synchronously inside wrapped(), and filterSdkErrorSpans reads it.
    const previous = consoleVerdict;
    consoleVerdict = benignKindsOf(benign, args);
    try {
      return wrapped.apply(this, args);
    } finally {
      consoleVerdict = previous;
    }
  };
  console.error = tap;
  const onError = (e: ErrorEvent) => noteSeen(e.error);
  const onRejection = (e: PromiseRejectionEvent) => noteSeen(e.reason);
  window.addEventListener("error", onError, true);
  window.addEventListener("unhandledrejection", onRejection, true);
  benignTeardown.push(() => {
    if (console.error === tap) {
      console.error = wrapped;
    }
    window.removeEventListener?.("error", onError, true);
    window.removeEventListener?.("unhandledrejection", onRejection, true);
    tapped = false;
  });
}

/**
 * The kind of every exception in a console.error call when ALL of them are
 * benign, or null to record the call: no exception, one real one, or an
 * argument that cannot even be inspected (a throwing Symbol.toStringTag
 * getter, a revoked Proxy).
 */
function benignKindsOf(benign: BenignFilter, args: unknown[]): string[] | null {
  const kinds: string[] = [];
  for (const arg of args) {
    let exception: boolean;
    try {
      exception = isExceptionLike(arg);
    } catch {
      return null;
    }
    if (!exception) {
      continue;
    }
    const kind = benign.classify(arg);
    if (kind === null) {
      return null;
    }
    kinds.push(kind);
  }
  return kinds.length > 0 ? kinds : null;
}

// ── Benign browser errors (benign-errors.ts) ────────────────────────────
//
// Filtered at the SDK's RECORDING boundary, never at the app's: every window
// "error" / "unhandledrejection" event and every console.error call reaches
// every app handler (window.onerror, onunhandledrejection, listeners of any
// phase, registered before telemetry started or after) and the console
// exactly as it would with no telemetry at all.
//
// The SDK's error capture opens one span per failure on its `errors` tracer —
// named onerror, unhandledrejection or console.error — and ends it once the
// exception is attached; only an ENDED span is ever exported. The SDK has no
// drop hook (its exporter options are not reachable through @hyperdx/browser,
// and an OTel 2.x provider takes no span processor after construction), so the
// runtime wraps that tracer's startSpan: a benign failure's span is counted
// and simply never ended, so it is never exported. Everything else ends as
// the SDK meant.
//
//   - window events: the span describes one value, so it is classified from
//     what the SDK recorded on it (error.object = the error's name,
//     error.message). A String (a non-Error throw) is never benign.
//   - console.error: one call can carry several values, and the span names
//     only the first exception, so the verdict comes from the console tap,
//     which classifies every argument (consoleVerdict).
interface BenignFilter {
  /** The kind of a benign error, or null. Counts nothing. */
  classify(error: unknown): string | null;
  /** Count one filtered error of `kind`. */
  note(kind: string, source: BenignErrorSource): void;
  /** Count and report true when `error` is benign; false to record it. */
  filtered(error: unknown, source: BenignErrorSource): boolean;
}

function benignFilter(
  config: BrowserTelemetryConfig,
  hdx: HyperDXClient,
): BenignFilter {
  const classify = config.classifyBenignError ?? classifyBenignError;
  const counter = new FilteredErrorCounter((name, attributes) => {
    if (!isTelemetryDisabled()) {
      hdx.addAction?.(name, attributes);
    }
  });
  const onPageHide = () => counter.flush();
  const onVisibility = () => {
    if (document.visibilityState === "hidden") {
      counter.flush();
    }
  };
  if (typeof window.addEventListener === "function") {
    window.addEventListener("pagehide", onPageHide);
  }
  if (typeof document !== "undefined") {
    document.addEventListener("visibilitychange", onVisibility);
  }
  benignTeardown.push(() => {
    window.removeEventListener?.("pagehide", onPageHide);
    if (typeof document !== "undefined") {
      document.removeEventListener("visibilitychange", onVisibility);
    }
  });
  const kindOf = (error: unknown): string | null => {
    try {
      return classify(error) || null;
    } catch {
      // A classifier that throws keeps the error.
      return null;
    }
  };
  return {
    classify: kindOf,
    note: (kind, source) => counter.note(kind, source),
    filtered(error, source) {
      const kind = kindOf(error);
      if (!kind) {
        return false;
      }
      counter.note(kind, source);
      return true;
    },
  };
}

/** The scope (name, version) the SDK's error capture records under. */
const SDK_ERRORS_SCOPE = { name: "errors", version: "1" } as const;

/** The spans that scope opens, one per captured failure, and their source. */
const SDK_ERROR_SPANS: Record<string, BenignErrorSource> = {
  onerror: "error",
  unhandledrejection: "unhandledrejection",
  "console.error": "console.error",
};

// The console tap's verdict on the call now inside the SDK's wrapper: the
// benign kinds, null to record it, undefined when no tapped call is running.
let consoleVerdict: string[] | null | undefined;

interface RecordingSpan {
  end(...args: unknown[]): unknown;
  attributes?: Record<string, unknown>;
}

interface StartSpanTracer {
  startSpan(name: string, ...rest: unknown[]): unknown;
}

function filterSdkErrorSpans(benign: BenignFilter): void {
  const proxy = trace.getTracerProvider() as { getDelegate?: () => unknown };
  const provider = (proxy.getDelegate?.() ?? proxy) as {
    getTracer?: (name: string, version?: string) => unknown;
  };
  const tracer = provider.getTracer?.(
    SDK_ERRORS_SCOPE.name,
    SDK_ERRORS_SCOPE.version,
  ) as StartSpanTracer | undefined;
  if (!tracer || typeof tracer.startSpan !== "function") {
    // No seam: record everything, as without the filter.
    return;
  }
  const original = tracer.startSpan;
  const startSpan = function (
    this: unknown,
    name: string,
    ...rest: unknown[]
  ): unknown {
    const span = original.call(this, name, ...rest) as RecordingSpan;
    const source = SDK_ERROR_SPANS[name];
    if (!source || typeof span?.end !== "function") {
      return span;
    }
    const verdict = name === "console.error" ? consoleVerdict : undefined;
    const end = span.end;
    span.end = function (this: unknown, ...args: unknown[]) {
      const kinds =
        name === "console.error"
          ? (verdict ?? null)
          : recordedKind(benign, span);
      if (kinds) {
        for (const kind of kinds) {
          benign.note(kind, source);
        }
        // Never ended, so never exported.
        return undefined;
      }
      return end.apply(this, args);
    };
    return span;
  };
  tracer.startSpan = startSpan;
  benignTeardown.push(() => {
    if (tracer.startSpan === startSpan) {
      tracer.startSpan = original;
    }
  });
}

/** The benign kind of the one failure a window-event span recorded, or null. */
function recordedKind(
  benign: BenignFilter,
  span: RecordingSpan,
): string[] | null {
  try {
    const name = span.attributes?.["error.object"];
    const message = span.attributes?.["error.message"];
    if (typeof name !== "string" || name === "String") {
      return null;
    }
    const kind = benign.classify({
      name,
      message: typeof message === "string" ? message : "",
    });
    return kind ? [kind] : null;
  } catch {
    return null;
  }
}

// What undoes the benign filter's hooks (the tracer wrap, the console tap, the
// flush listeners), run when startup fails at any point after they went in.
let benignTeardown: Array<() => void> = [];

function removeBenignFilter(): void {
  const undo = benignTeardown;
  benignTeardown = [];
  for (const fn of undo.reverse()) {
    try {
      fn();
    } catch {
      // Telemetry must never break the app.
    }
  }
}

// An exception value as console.error receives it: an Error, a DOMException
// (not an Error in every browser), or either from another realm (an iframe),
// which fails instanceof but keeps its [object Error] / [object DOMException]
// tag.
function isExceptionLike(value: unknown): boolean {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  if (value instanceof Error) {
    return true;
  }
  if (typeof DOMException !== "undefined" && value instanceof DOMException) {
    return true;
  }
  const tag = Object.prototype.toString.call(value);
  return tag === "[object Error]" || tag === "[object DOMException]";
}

function alreadyRecorded(error: unknown): boolean {
  return typeof error === "object" && error !== null && seenBySdk.has(error);
}

/**
 * The options passed to HyperDX.init. Exported for tests and for apps that
 * want to see exactly what is configured — it is pure. `clientAttributes`
 * are the page's own client resource attributes (client-context.ts:
 * browser.* client hints, app.desktop.*); the config's keys win over them.
 */
export function buildSdkOptions(
  config: BrowserTelemetryConfig,
  ingestUrl: string,
  pageOrigin: string,
  clientAttributes: Record<string, string> = {},
): Record<string, unknown> {
  // `config.replay` is the FINAL decision here. initBrowserTelemetry resolves
  // replaySampleRate into it (decideReplay) before calling this.
  const replay = config.replay === true && !replayPrivacyOptOut();
  const resourceAttributes: Record<string, string> = { ...clientAttributes };
  if (config.serviceVersion) {
    resourceAttributes["service.version"] = config.serviceVersion;
  }
  if (config.serviceNamespace) {
    resourceAttributes["service.namespace"] = config.serviceNamespace;
  }
  if (config.frontend) {
    resourceAttributes["forge.frontend"] = config.frontend;
  }
  if (config.environment) {
    resourceAttributes["deployment.environment.name"] = config.environment;
    resourceAttributes["deployment.environment"] = config.environment;
  }

  const targets = propagationTargets(pageOrigin, config.apiOrigins);
  const advancedNetworkCapture = config.advancedNetworkCapture ?? false;
  const options: Record<string, unknown> = {
    apiKey: PLACEHOLDER_API_KEY,
    service: config.serviceName,
    url: ingestUrl,
    tracePropagationTargets: targets,
    // The SDK would otherwise trace its own exports, and it must never
    // observe the route it ships through.
    ignoreUrls: [new RegExp(`^${escapeRegExp(ingestUrl)}`)],
    consoleCapture: config.consoleCapture ?? true,
    advancedNetworkCapture,
    // The SDK spreads this over ITS fetch/xhr config as whole keys, so
    // everything it would have set (propagation targets, the capture switch)
    // must be restated here or it is lost.
    instrumentations: {
      // Raw DOM events belong to replay, not distributed traces. HyperDX's
      // default traces every mousedown/up listener (including replay's own),
      // creating empty roots, and misses the listeners added before it
      // loaded. The runtime records one click span per click itself (see
      // "One click, one span"). Meaningful actions can still use manual
      // spans; fetch/xhr and document-load instrumentation keep their real
      // parents.
      interactions: false,
      fetch: {
        propagateTraceHeaderCorsUrls: targets,
        advancedNetworkCapture: () => advancedNetworkCapture,
        applyCustomAttributesOnSpan: finishNetworkSpan,
      },
      xhr: {
        propagateTraceHeaderCorsUrls: targets,
        advancedNetworkCapture: () => advancedNetworkCapture,
        applyCustomAttributesOnSpan: finishNetworkSpan,
      },
    },
    // Polls for window.Intercom on a timer; nothing here uses Intercom.
    disableIntercom: true,
    disableReplay: !replay,
    otelResourceAttributes: resourceAttributes,
    debug: config.debug ?? false,
    // Privacy is forced and unconditional: no path through this function
    // yields an unmasked or media-recording replay. The SDK's own default
    // masks inputs but NOT text, and text is where chat content and records
    // live.
    maskAllInputs: true,
    maskAllText: true,
    recordCanvas: false,
    blockSelector: REPLAY_BLOCK_SELECTOR,
    // When the runtime's recorder starts and how often it sends
    // (replay-recorder.ts). Not HyperDX options: the runtime's client reads
    // them (sdk-client.ts).
    replayStart: config.replayStart === "interaction" ? "interaction" : "load",
    replayFlushIntervalMs:
      typeof config.replayFlushIntervalMs === "number" &&
      config.replayFlushIntervalMs > 0
        ? config.replayFlushIntervalMs
        : REPLAY_FLUSH_INTERVAL_MS,
    // Handed to rrweb's record() as they are. The plugin strips
    // the query and fragment from the replay's page URL (see "No URL leaves
    // the browser" in the header).
    replay: {
      inlineImages: false,
      collectFonts: false,
      // What the recorder costs the page (see "Replay costs the page little"
      // in the header): continuous input sampled, not every event; the DOM
      // without what no replay shows.
      sampling: replaySampling(config.replaySampling),
      ...(config.replaySlimDOM === false ? {} : { slimDOMOptions: "all" }),
      plugins: [
        replayUrlScrubPlugin({ keepQueryParams: config.keepUrlQueryParams }),
      ],
    },
  };
  return options;
}

/**
 * rrweb's `sampling` for continuous input, as the runtime records it. rrweb
 * records every scroll position at most each 100 ms, the pointer each 50 ms
 * and every keystroke of every field; each one is serialized, queued and, on
 * sent in a batch.
 * Half the pointer rate, a 150 ms scroll throttle, media each 800 ms (rrweb:
 * 500) and a field's final value replay the same session at a fraction of the
 * cost; inputs are masked, so a keystroke-by-keystroke record only ever
 * replayed asterisks appearing.
 */
export const REPLAY_SAMPLING = {
  mousemove: 100,
  scroll: 150,
  media: 800,
  input: "last",
} as const satisfies ReplaySampling;

const REPLAY_SAMPLING_KEYS = [
  "mousemove",
  "mouseInteraction",
  "scroll",
  "media",
  "input",
] as const;

// REPLAY_SAMPLING with the keys the config gives, and only the keys
// ReplaySampling names: nothing else reaches rrweb's record() through it.
function replaySampling(
  overrides: ReplaySampling | undefined,
): Record<string, unknown> {
  const out: Record<string, unknown> = { ...REPLAY_SAMPLING };
  for (const key of REPLAY_SAMPLING_KEYS) {
    const value = overrides?.[key];
    if (value !== undefined) {
      out[key] = value;
    }
  }
  return out;
}

/** The rrweb `sampling` keys a replay's recording takes (canvas is never recorded). */
export interface ReplaySampling {
  /** false: no pointer movement; a number: at most one position each N ms. */
  mousemove?: boolean | number;
  /** false: no clicks, focus and the like; or per kind (`{ MouseUp: false }`). */
  mouseInteraction?: boolean | Record<string, boolean | undefined>;
  /** At most one scroll position each N ms. */
  scroll?: number;
  /** At most one media (play, pause, seek) event each N ms. */
  media?: number;
  /** "all": every change to a field; "last": its value once typing stops. */
  input?: "all" | "last";
}

/** Elements a replay never records: their pixels stay in the browser. */
export const REPLAY_BLOCK_SELECTOR =
  "img, picture, video, audio, iframe, object, embed";

/** Where a replay sampling decision is held for the tab's session. */
export const REPLAY_SAMPLE_KEY = "forge.replay.sampled";

/** The slice of Storage the sampling decision needs. */
interface DecisionStore {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

/**
 * Whether this session records a replay: config.replay, then the sample rate,
 * drawn once and remembered in `store` so a reload (or a route change that
 * re-runs init) never re-rolls into a session that only half-recorded. With no
 * usable store the draw is made each load. Browser DNT/GPC preferences always
 * win over an opt-in or a stored draw; exported for tests.
 */
export function decideReplay(
  config: Pick<BrowserTelemetryConfig, "replay" | "replaySampleRate">,
  store: DecisionStore | null,
  random: () => number,
): boolean {
  if (config.replay !== true || replayPrivacyOptOut()) {
    return false;
  }
  const rate = clampRate(config.replaySampleRate);
  if (rate >= 1) {
    return true;
  }
  if (rate <= 0) {
    return false;
  }
  try {
    const held = store?.getItem(REPLAY_SAMPLE_KEY);
    if (held === "1" || held === "0") {
      return held === "1";
    }
  } catch {
    // Storage blocked: fall through to a fresh draw.
  }
  const sampled = random() < rate;
  try {
    store?.setItem(REPLAY_SAMPLE_KEY, sampled ? "1" : "0");
  } catch {
    // Not persisted: the next load draws again.
  }
  return sampled;
}

// Replay is sensitive even when masked. The browser's DNT/GPC preferences
// (browserPrivacyOptOut, shared with the auth events) outrank the author's
// opt-in AND a previously stored sampling decision. They do not change
// ordinary error/trace telemetry. An unreadable preference fails closed.
function replayPrivacyOptOut(): boolean {
  return browserPrivacyOptOut();
}

// Unset is "every session"; anything that is not a number is a bug, and a bug
// must not start recording people.
function clampRate(rate: number | undefined): number {
  if (rate === undefined) {
    return 1;
  }
  if (typeof rate !== "number" || Number.isNaN(rate)) {
    return 0;
  }
  return Math.min(1, Math.max(0, rate));
}

/**
 * See "One session per visit" and "Analytics tiers" in the header: the SDK's
 * session cookie, answered from memory below the full tier and kept on the
 * device (cookie, else localStorage) in it. Never throws.
 */
function keepRumSession(): RumSessionStore | null {
  try {
    if (typeof document === "undefined") {
      return null;
    }
    // In the full tier the session an earlier load kept on the device
    // continues; below it, any such session is erased at install.
    const full = analyticsTier() === "full";
    const rum = installRumSessionStore(document, Date.now, {
      keepDeviceSession: full,
    });
    if (full) {
      rum?.moveToDevice(localStorageOrNull());
    }
    return rum;
  } catch {
    return null;
  }
}

function localStorageOrNull(): Storage | null {
  try {
    return window.localStorage ?? null;
  } catch {
    // Blocked storage throws on access.
    return null;
  }
}

function sessionStorageOrNull(): DecisionStore | null {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

/** The span attributes that hold a request URL. */
const URL_ATTRIBUTES = ["http.url", "url.full"] as const;

/** `url` without its query string and fragment. */
export function stripUrlQuery(url: string): string {
  const cut = url.search(/[?#]/);
  return cut === -1 ? url : url.slice(0, cut);
}

interface UrlSpan {
  attributes?: Record<string, unknown>;
  setAttribute(key: string, value: string): unknown;
  updateName?(name: string): unknown;
}

// applyCustomAttributesOnSpan for both instrumentations: it runs before the
// span ends, and after HyperDX's own `HTTP <METHOD>` rename, so the values
// set here are the ones exported. The span carries the RUM session as
// `session.id` (the resource's rum.sessionId says the same for the browser;
// the attribute is what the backend's own spans carry too, so a trace is found
// by session from either side).
function finishNetworkSpan(span: UrlSpan): void {
  stripNetworkQuery(span);
  const url = span.attributes?.["url.full"] ?? span.attributes?.["http.url"];
  const method =
    span.attributes?.["http.request.method"] ??
    span.attributes?.["http.method"];
  if (typeof url === "string" && typeof span.updateName === "function") {
    const name = spanRouteName(typeof method === "string" ? method : "", url);
    if (name) {
      span.updateName(name);
    }
  }
  const session = rumSessionId();
  if (session) {
    span.setAttribute("session.id", session);
  }
}

// The query parameters the running SDK keeps (keepUrlQueryParams), or null.
let keepQuery: QueryParamFilter | null = null;

/**
 * Wraps the running SDK's span exporter so nothing it exports carries a URL
 * query or fragment. A provider without the pinned SDK's shape is reported
 * once: its spans still go out, and the ingest side's scrub still strips them.
 */
function scrubSdkUrls(config: BrowserTelemetryConfig): void {
  const provider = trace.getTracerProvider() as FlushableProvider;
  const scrubbing = installSpanExportScrub(provider, {
    keepQueryParams: config.keepUrlQueryParams,
  });
  const delegate = (provider.getDelegate?.() ?? provider) as FlushableProvider;
  // Only an SDK provider can flush; the API's no-op provider (no SDK
  // running) has nothing to scrub.
  if (scrubbing === 0 && typeof delegate.forceFlush === "function") {
    console.warn(
      "[telemetry] the SDK's span exporter was not found; URLs are exported with their query and fragment",
    );
  }
}

function stripNetworkQuery(span: UrlSpan): void {
  for (const key of URL_ATTRIBUTES) {
    const value = span.attributes?.[key];
    if (typeof value === "string") {
      const stripped = scrubUrl(value, keepQuery);
      if (stripped !== value) {
        span.setAttribute(key, stripped);
      }
    }
  }
}

// A Connect/gRPC procedure: `/<package>.<Service>/<Method>`, the service and
// method PascalCase as protobuf declares them. The whole path is the route,
// fixed by the proto, so it carries no request values.
const RPC_PATH = /^\/[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*\.[A-Z]\w*\/[A-Z]\w*$/;

/**
 * The name a fetch/XHR span gets: `<METHOD> /<package>.<Service>/<Method>`,
 * the server span's name with the method in front, when the URL is a
 * Connect/gRPC procedure. Empty for any other URL, which keeps the SDK's
 * `HTTP <METHOD>`: an arbitrary path can hold values (ids, tokens, emails) no
 * pattern recognises reliably, and a span name must be neither sensitive nor
 * unbounded. Empty too when `url` cannot be parsed. Exported for tests.
 */
export function spanRouteName(method: string, url: string): string {
  let path: string;
  try {
    path = new URL(url, "http://base.invalid").pathname;
  } catch {
    return "";
  }
  if (!RPC_PATH.test(path)) {
    return "";
  }
  return `${method.trim().toUpperCase() || "POST"} ${path}`;
}

/**
 * resolveIngestUrl turns the configured endpoint into the absolute URL the SDK
 * is given, or null when telemetry is off. A path resolves against the page
 * origin, so the default is same-origin by construction.
 */
export function resolveIngestUrl(
  endpoint: string | undefined,
  pageOrigin: string,
): string | null {
  const value = (endpoint ?? "").trim();
  if (value === "" || value.toLowerCase() === "off") {
    return null;
  }
  try {
    return new URL(value, pageOrigin).toString().replace(/\/+$/, "");
  } catch {
    return null;
  }
}

function propagationTargets(
  pageOrigin: string,
  apiOrigins: Array<string | undefined> = [],
): RegExp[] {
  const origins = new Set<string>([pageOrigin]);
  for (const raw of apiOrigins) {
    if (!raw) {
      continue;
    }
    try {
      origins.add(new URL(raw, pageOrigin).origin);
    } catch {
      // An unparsable origin is not a target.
    }
  }
  return [...origins].map((o) => new RegExp(`^${escapeRegExp(o)}(?:[/?#]|$)`));
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** The tracer to open manual spans on. Resolves HyperDX's provider once it is running. */
export function getTracer(name = "default") {
  return trace.getTracer(name);
}

export { context, propagation, trace } from "@opentelemetry/api";
