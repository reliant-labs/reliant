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
// @hyperdx/browser wraps @hyperdx/otel-web, which builds its own
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
// The SDK bundles an rrweb recorder. It is DISABLED unless `replay: true`.
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
// ── Withdrawal stops replay now ──────────────────────────────────────────
//
// When the tier leaves `full` (revokeAnalyticsConsent, optOutOfAnalytics) or
// telemetry is disabled, no replay leaves the page from that instant: not a
// record made afterwards, not one made before it and still buffered, and not
// a span from stopping the recorder. Stopping the SDK's recorder alone does not
// do that. It buffers rrweb records and posts them to `${ingest}/v1/logs` on
// its own 5 s timer (and on unload), so a stopped recorder still sends what it
// held; and a stop() on an already stopped recorder starts a `record stop`
// span on its `rum.rr-web` tracer, so stopping twice exported one replay span
// at the moment of withdrawal. So when replay runs, two gates go in right after
// init and close for good, BEFORE the one stop: the recorder's record posts
// (told from other OTLP logs to the same URL by their `rum.rr-web` scope) are
// answered locally without the network (gateReplayRecords), and its
// tracer's spans are never ended (gateReplaySpans). The recorder is stopped
// once however many paths ask. Replay that already left before the withdrawal
// stays where it is.
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
// nothing.
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
// The SDK is ~600 KB minified (rrweb and the OTel web stack are bundled in
// it), so it is imported lazily: bundlers split it into its own chunk that
// loads after the app is interactive and costs the first paint nothing.
// initBrowserTelemetry is therefore async and idempotent — every caller gets
// the same promise, which is what survives React StrictMode's double mount and
// Fast Refresh. It is a no-op without a DOM (SSR, prerender) and without an
// endpoint, and it never throws.
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
import { setExceptionReporter } from "./error-reporter.js";
import { installFetchContext } from "./interaction-context.js";
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
} from "./telemetry-gate.js";
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
   * @deprecated Use `analytics` and grantAnalyticsConsent(). Kept working:
   * with `analytics` unset, passing `attribution` at all sets the mode to
   * "full" (it was always a choice about a persistent visitor id); `true`
   * also grants consent at start, as `attribution: true` started attribution
   * before; `false` waits for enableAttribution() / grantAnalyticsConsent().
   * Unset, there is no visitor id below the `full` tier.
   */
  attribution?: boolean;
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

/** The slice of the SDK this module calls. Typed here, not imported, so the module loads without it. */
interface HyperDXClient {
  init(config: Record<string, unknown>): void;
  recordException(error: unknown, attributes?: Record<string, unknown>): void;
  addAction?(name: string, attributes?: Record<string, TrackValue>): void;
  setGlobalAttributes?(attributes: Record<string, string>): void;
  stopSessionRecorder?(): void;
  getSessionId?(): string | undefined;
}

let started: Promise<boolean> | null = null;

// Attribution state shared by start() and enableAttribution(): whichever runs
// second does the stamping, so the call order does not matter.
let attributionWanted = false;
let attributionOptions: { search?: string } = {};
let runningConfig: BrowserTelemetryConfig | null = null;
let runningClient: HyperDXClient | null = null;

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

// The mode a config asks for: `analytics`, else the legacy `attribution`
// (any explicit value meant a persistent visitor id: "full"), else the
// cookieless default, "identified".
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
  return config.attribution === undefined ? "identified" : "full";
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

/** The scope (name, version) the SDK's session recorder records spans under. */
const SDK_REPLAY_SCOPE = { name: "rum.rr-web", version: "4.0.0" } as const;

/** Where the SDK's recorder posts its records: `${ingest}${this}`. */
const SDK_REPLAY_LOGS_PATH = "/v1/logs";

/**
 * Puts both replay exports behind a gate that closes for good when the
 * returned stop runs, and stops the recorder then, once. See "Withdrawal stops
 * replay now" in the header. Call it right after hdx.init, with replay on.
 */
function gateReplay(hdx: HyperDXClient, ingest: string): () => void {
  let open = true;
  const closed = () => !open;
  gateReplayRecords(`${ingest}${SDK_REPLAY_LOGS_PATH}`, closed);
  gateReplaySpans(closed);
  return () => {
    if (!open) {
      return;
    }
    // Closed before the stop, so nothing the stop emits gets out.
    open = false;
    try {
      hdx.stopSessionRecorder?.();
    } catch {
      // Telemetry must never break the app; the gates hold either way.
    }
  };
}

type FetchFn = (...args: Parameters<typeof fetch>) => ReturnType<typeof fetch>;

/**
 * Answers the recorder's record posts itself, without the network, once
 * `closed()`: the batch it had buffered when replay stopped is dropped, not
 * sent. Every other request passes through untouched, including other OTLP
 * logs posted to the same `${ingest}/v1/logs`: a post is the recorder's only
 * when its OTLP body names the recorder's scope (isReplayBatch). The recorder
 * calls the global fetch when it exports, so this layer is reached whatever
 * wraps fetch after it.
 */
function gateReplayRecords(logsUrl: string, closed: () => boolean): void {
  const g = globalThis as { fetch?: FetchFn };
  const inner = g.fetch;
  if (typeof inner !== "function") {
    return;
  }
  const target = canonicalUrl(logsUrl, logsUrl);
  g.fetch = function (this: unknown, ...args: Parameters<typeof fetch>) {
    if (
      !closed() ||
      target === undefined ||
      canonicalUrl(requestUrl(args[0]), logsUrl) !== target
    ) {
      return inner.apply(this, args);
    }
    return isReplayBatch(args[0], args[1]).then((replay) =>
      replay
        ? typeof Response === "function"
          ? new Response(null, { status: 204 })
          : (undefined as unknown as Response)
        : inner.apply(this, args),
    );
  };
}

/**
 * Whether a request carries the recorder's records: an OTLP logs body
 * (gzip-compressed, as the recorder sends it, or not) one of whose ScopeLogs
 * has the recorder's scope name, read at its field path (hasReplayScope), so
 * the same bytes inside a log body or attribute do not count. A gzip body this
 * browser cannot decompress counts as the recorder's when it is protobuf, the
 * recorder's shape: after a withdrawal, dropping an unreadable log batch is
 * the side to err on.
 */
async function isReplayBatch(
  input: unknown,
  init: RequestInit | undefined,
): Promise<boolean> {
  try {
    const headers = requestHeaders(input, init);
    let bytes = await requestBody(input, init);
    if (!bytes) {
      return false;
    }
    if (/\bgzip\b/i.test(headers?.get("content-encoding") ?? "")) {
      const plain = await gunzip(bytes);
      if (!plain) {
        return /x-protobuf/i.test(headers?.get("content-type") ?? "");
      }
      bytes = plain;
    }
    return hasReplayScope(bytes);
  } catch {
    // Unreadable: not provably the recorder's, so it goes out as asked.
    return false;
  }
}

/** SDK_REPLAY_SCOPE.name as the UTF-8 bytes protobuf carries. */
const REPLAY_SCOPE_NAME: readonly number[] = Array.from(
  SDK_REPLAY_SCOPE.name,
  (c) => c.charCodeAt(0),
);

/**
 * Whether an OTLP ExportLogsServiceRequest, protobuf-encoded, has a ScopeLogs
 * whose scope is the recorder's: the path resource_logs (1) → scope_logs (2)
 * → scope (1) → name (1), as opentelemetry-proto's logs.proto defines it.
 * Every other field, a log record's body and attributes included, is skipped
 * unread. Throws on a body that is not well-formed protobuf.
 */
function hasReplayScope(request: Uint8Array): boolean {
  let found = false;
  forEachField(request, 1, (resourceLogs) => {
    forEachField(resourceLogs, 2, (scopeLogs) => {
      // A scalar field repeated on the wire: the last one is its value.
      let name: Uint8Array | undefined;
      forEachField(scopeLogs, 1, (scope) => {
        forEachField(scope, 1, (n) => {
          name = n;
        });
      });
      if (name && sameBytes(name, REPLAY_SCOPE_NAME)) {
        found = true;
      }
    });
  });
  return found;
}

/**
 * Calls `visit` with the payload of every length-delimited field numbered
 * `field` in `message`, in order, skipping all others by their wire type.
 * Throws on a truncated or malformed message.
 */
function forEachField(
  message: Uint8Array,
  field: number,
  visit: (payload: Uint8Array) => void,
): void {
  let at = 0;
  const varint = (): number => {
    let value = 0;
    for (let shift = 0; shift < 64; shift += 7) {
      if (at >= message.length) {
        throw new Error("protobuf: truncated varint");
      }
      const b = message[at++] as number;
      // Lengths and tags fit in 53 bits; higher bits only matter to skip.
      value += (b & 0x7f) * 2 ** shift;
      if ((b & 0x80) === 0) {
        return value;
      }
    }
    throw new Error("protobuf: varint too long");
  };
  const skip = (n: number) => {
    if (at + n > message.length) {
      throw new Error("protobuf: truncated field");
    }
    at += n;
  };
  while (at < message.length) {
    const tag = varint();
    const wire = tag % 8;
    const number = Math.floor(tag / 8);
    switch (wire) {
      case 0:
        varint();
        break;
      case 1:
        skip(8);
        break;
      case 2: {
        const length = varint();
        const start = at;
        skip(length);
        if (number === field) {
          visit(message.subarray(start, start + length));
        }
        break;
      }
      case 5:
        skip(4);
        break;
      default:
        // Groups (3, 4) are not in OTLP; anything else is not protobuf.
        throw new Error(`protobuf: wire type ${wire}`);
    }
  }
}

function sameBytes(a: Uint8Array, b: readonly number[]): boolean {
  if (a.length !== b.length) {
    return false;
  }
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) {
      return false;
    }
  }
  return true;
}

function requestHeaders(
  input: unknown,
  init: RequestInit | undefined,
): Headers | undefined {
  if (typeof Headers !== "function") {
    return undefined;
  }
  if (init?.headers) {
    return new Headers(init.headers);
  }
  if (typeof Request === "function" && input instanceof Request) {
    return input.headers;
  }
  return undefined;
}

async function requestBody(
  input: unknown,
  init: RequestInit | undefined,
): Promise<Uint8Array | undefined> {
  const body: unknown = init?.body;
  if (body == null) {
    if (typeof Request === "function" && input instanceof Request) {
      return new Uint8Array(await input.clone().arrayBuffer());
    }
    return undefined;
  }
  if (body instanceof Uint8Array) {
    return body;
  }
  if (body instanceof ArrayBuffer) {
    return new Uint8Array(body);
  }
  if (ArrayBuffer.isView(body)) {
    return new Uint8Array(body.buffer, body.byteOffset, body.byteLength);
  }
  if (typeof Blob === "function" && body instanceof Blob) {
    return new Uint8Array(await body.arrayBuffer());
  }
  if (typeof body === "string") {
    return new TextEncoder().encode(body);
  }
  return undefined;
}

async function gunzip(bytes: Uint8Array): Promise<Uint8Array | undefined> {
  if (
    typeof DecompressionStream !== "function" ||
    typeof Response !== "function"
  ) {
    return undefined;
  }
  try {
    const stream = new Response(bytes as BodyInit).body?.pipeThrough(
      new DecompressionStream("gzip"),
    );
    if (!stream) {
      return undefined;
    }
    return new Uint8Array(await new Response(stream).arrayBuffer());
  } catch {
    return undefined;
  }
}

/**
 * `url` resolved against `base`, without query, fragment or a trailing slash,
 * so equivalent spellings of the logs endpoint compare equal.
 */
function canonicalUrl(url: string | undefined, base: string) {
  if (url === undefined || typeof URL === "undefined") {
    return url;
  }
  try {
    const u = new URL(url, base);
    return `${u.origin}${u.pathname.replace(/\/+$/, "")}`;
  } catch {
    return url;
  }
}

function requestUrl(input: unknown): string | undefined {
  try {
    if (typeof input === "string") {
      return input;
    }
    if (typeof URL !== "undefined" && input instanceof URL) {
      return input.href;
    }
    const url = (input as { url?: unknown } | null)?.url;
    return typeof url === "string" ? url : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Leaves unended, so unexported, every span the recorder's tracer would end
 * once `closed()`. The SDK's recorder starts a `record stop` span when it is
 * stopped a second time; any later stop, from the app or the SDK, is caught
 * here.
 */
function gateReplaySpans(closed: () => boolean): void {
  const proxy = trace.getTracerProvider() as { getDelegate?: () => unknown };
  const provider = (proxy.getDelegate?.() ?? proxy) as {
    getTracer?: (name: string, version?: string) => unknown;
  };
  const tracer = provider.getTracer?.(
    SDK_REPLAY_SCOPE.name,
    SDK_REPLAY_SCOPE.version,
  ) as StartSpanTracer | undefined;
  if (!tracer || typeof tracer.startSpan !== "function") {
    return;
  }
  const original = tracer.startSpan;
  tracer.startSpan = function (
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
      // Never ended, so never exported.
      return closed() ? undefined : end.apply(this, args);
    };
    return span;
  };
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
  if (config.analytics === undefined && config.attribution === true) {
    grantAnalyticsConsent();
  }
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
  started = start(config, ingest);
  registerTelemetryStartup(started);
  return started;
}

async function start(
  config: BrowserTelemetryConfig,
  ingest: string,
): Promise<boolean> {
  // Before the first await: until web-vitals registers in hdx.init, only this
  // watch sees the page hidden (see "Paint vitals" in the header).
  const hidden = watchFirstHidden();
  try {
    await firstVisible();
    const hdx = resolveClient(await import("@hyperdx/browser"));
    if (!hdx || isTelemetryDisabled()) {
      hidden.stop();
      setTelemetrySink(null);
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
    const stopReplay = replay ? gateReplay(hdx, ingest) : () => {};
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
    hdx.setGlobalAttributes?.(globals);
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
    let identity: SessionIdentity | null = null;
    let stopRoutes = () => {};
    setExceptionReporter((error, attributes) => {
      if (alreadyRecorded(error) || benign.filtered(error, "reportException")) {
        return;
      }
      hdx.recordException(error, attributes);
    });
    setTelemetrySink({
      track: (name, attributes) => hdx.addAction?.(name, attributes),
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
        stopReplay();
        // Back to "no SDK": reportException returns false, so a caller falls
        // back to console.error and the error does not vanish.
        setExceptionReporter(null);
        registerRumSession(null);
        stopViewport();
        stopTier();
        stopRoutes();
        identity?.stop();
      },
    });
    // After the sink, so the first page view and the user go straight to the
    // SDK. Both are off unless the caller asked (otel_gen.ts does).
    identity = startIdentity(config);
    stopRoutes = startRouteWatch(config, identity);
    return true;
  } catch (err) {
    // Blocked chunk, ad-blocker, SDK throw: telemetry must never break the app.
    // A startup that failed after the hooks went in takes them out again.
    hidden.stop();
    removeBenignFilter();
    console.debug("[telemetry] disabled:", err);
    setTelemetrySink(null);
    return false;
  }
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
// that re-asks on leaving the sign-in screens. Returns its teardown.
function startRouteWatch(
  config: BrowserTelemetryConfig,
  identity: SessionIdentity | null,
): () => void {
  const pageViews = config.pageViews ?? false;
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
      onRoute: identity ? identity.onRoute : undefined,
    });
  } catch {
    return () => {};
  }
}

/**
 * Resolves once the document is visible: at once for a visible page or one
 * with no Page Visibility API, otherwise at its first visibilitychange to
 * visible. See "No session until the page has been seen" in the header.
 */
function firstVisible(): Promise<void> {
  if (
    typeof document === "undefined" ||
    document.visibilityState !== "hidden" ||
    typeof document.addEventListener !== "function"
  ) {
    return Promise.resolve();
  }
  return new Promise((resolve) => {
    const onChange = () => {
      if (document.visibilityState !== "hidden") {
        document.removeEventListener("visibilitychange", onChange);
        resolve();
      }
    };
    document.addEventListener("visibilitychange", onChange);
  });
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
      // creating empty roots. Meaningful actions can still use manual spans;
      // fetch/xhr and document-load instrumentation keep their real parents.
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
    // The SDK passes these to rrweb's record() as they are. The plugin strips
    // the query and fragment from the replay's page URL (see "No URL leaves
    // the browser" in the header).
    replay: {
      inlineImages: false,
      collectFonts: false,
      plugins: [
        replayUrlScrubPlugin({ keepQueryParams: config.keepUrlQueryParams }),
      ],
    },
  };
  return options;
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

// @hyperdx/browser is a UMD/CJS bundle whose module.exports IS the client, and
// its methods use `this`. A bundler hands back a module NAMESPACE that exposes
// both the client's members at the top level (as read-only getters) and the
// client itself as `default`. Calling init on the namespace binds `this` to that
// frozen object and the SDK throws on its first property write, which disabled
// telemetry silently. So descend to the deepest `default` that is still a
// client, the real instance, and never settle for the namespace in front of it.
function resolveClient(mod: unknown): HyperDXClient | null {
  let cur: unknown = mod;
  for (let depth = 0; depth < 4; depth++) {
    const next = peek(cur, "default");
    if (!isClient(next)) {
      break;
    }
    cur = next;
  }
  return isClient(cur) ? cur : null;
}

function isClient(v: unknown): v is HyperDXClient {
  return (
    typeof peek(v, "init") === "function" &&
    typeof peek(v, "recordException") === "function"
  );
}

// A module namespace object may throw on an absent key (strict ESM
// interop shims and test doubles do), and "absent" is an answer here.
function peek(obj: unknown, key: string): unknown {
  try {
    return (obj as Record<string, unknown>)[key];
  } catch {
    return undefined;
  }
}

/** The tracer to open manual spans on. Resolves HyperDX's provider once it is running. */
export function getTracer(name = "default") {
  return trace.getTracer(name);
}

export { context, propagation, trace } from "@opentelemetry/api";
