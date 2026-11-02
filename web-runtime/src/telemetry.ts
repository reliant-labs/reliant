// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Browser telemetry: the HyperDX browser SDK, initialised the forge way.
//
// Captures errors (uncaught, unhandled rejections, boundary-caught), console
// output, fetch/XHR spans, document load, long tasks and web vitals, and
// ships them to ClickStack through a SAME-ORIGIN `/_otel` route.
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
// sessionStorage for the tab's session.
//
// ── One session per visit, even where cookies are dropped ────────────────
//
// The SDK keeps its session id only in the `__rum_sid` cookie and mints a new
// one whenever it cannot read that cookie back, which it checks every minute.
// An origin that drops cookies (an Electron renderer on a custom scheme such
// as `app://`) therefore got a new session every minute. Before init, the
// runtime probes whether cookies persist and, when they do not, keeps that one
// cookie in localStorage with the same 15-minute inactivity and 4-hour limits
// (session-cookie-fallback.ts). Origins whose cookies work are untouched.
//
// ── Network URLs carry no query string ───────────────────────────────────
//
// fetch/XHR spans record the request URL, and a query string is where tokens,
// OAuth codes and search terms live. The runtime rewrites `http.url` and
// `url.full` on every fetch/XHR span to the URL without query or fragment.
// The SDK's own `location.href` attribute (the page URL, stamped on every span)
// has no client-side hook; the ingest side scrubs it.
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
  takeIdentifyProps,
} from "./attribution.js";
import {
  type BenignErrorClassifier,
  type BenignErrorSource,
  classifyBenignError,
  FilteredErrorCounter,
} from "./benign-errors.js";
import { setExceptionReporter } from "./error-reporter.js";
import { installSessionCookieFallback } from "./session-cookie-fallback.js";
import { registerTelemetryStartup } from "./telemetry-gate.js";
import {
  isTelemetryDisabled,
  setTelemetrySink,
  type TrackValue,
} from "./track.js";

export {
  cookiesPersist,
  installSessionCookieFallback,
  RUM_SESSION_COOKIE,
  RUM_SESSION_STORAGE_KEY,
} from "./session-cookie-fallback.js";

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
   * Session replay (rrweb). Default false. When true, all text and inputs are
   * masked, media is blocked and canvas is not recorded (see the header).
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
   * localStorage key holding this browser's anonymous visitor id (stamped as
   * `visitor.id`). Set it to adopt an id a site already keeps, e.g. a landing
   * page's `reliant.visitor_id`. Default `forge.visitor_id`. A `rid` in the
   * landing URL wins over it.
   */
  visitorIdStorageKey?: string;
  /**
   * Start visitor attribution with the SDK. Default true. Pass false when the
   * visitor id needs a consent the page does not have yet (error telemetry for
   * everyone, a visitor id only with analytics consent): no visitor id is
   * created, read or stamped, nothing is captured from the URL, withAttribution
   * returns URLs untouched and `user_identified` never fires, until the app
   * calls enableAttribution().
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
   * (benign-errors.ts). Default classifyBenignError: an AbortError, and the
   * browser's generic "Failed to fetch" TypeError; never a ConnectError. It
   * applies to what the SDK records from unhandled rejections, uncaught
   * errors, console.error and reportException; the app's own handlers see
   * every event unchanged. For an unhandled rejection or uncaught error it is
   * handed `{ name, message }` as the SDK recorded them, not the original
   * value. Pass `() => null` to record everything.
   */
  classifyBenignError?: BenignErrorClassifier;
}

/** The slice of the SDK this module calls. Typed here, not imported, so the module loads without it. */
interface HyperDXClient {
  init(config: Record<string, unknown>): void;
  recordException(error: unknown, attributes?: Record<string, unknown>): void;
  addAction?(name: string, attributes?: Record<string, TrackValue>): void;
  setGlobalAttributes?(attributes: Record<string, string>): void;
  stopSessionRecorder?(): void;
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
 * Turn on visitor attribution for a page that started telemetry with
 * `attribution: false`, once the consent it needs is granted: from here on
 * every event carries `visitor.id`, withAttribution stamps links and the first
 * sign-in emits `user_identified`. Pass `search` (the landing URL's query
 * string, kept from page load) when a client-side navigation may have dropped
 * it since. Safe to call before init resolves, more than once, and on the
 * server; a no-op after disableTelemetry().
 */
export function enableAttribution(options?: { search?: string }): void {
  if (typeof window === "undefined" || isTelemetryDisabled()) {
    return;
  }
  if (options?.search !== undefined) {
    attributionOptions = { search: options.search };
  }
  if (attributionWanted) {
    return;
  }
  attributionWanted = true;
  if (runningClient && runningConfig) {
    try {
      runningClient.setGlobalAttributes?.({
        "visitor.id": captureVisitor(runningConfig),
      });
    } catch {
      // Telemetry must never break the app.
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
  try {
    const hdx = resolveClient(await import("@hyperdx/browser"));
    if (!hdx || isTelemetryDisabled()) {
      setTelemetrySink(null);
      return false;
    }
    const replay = decideReplay(config, sessionStorageOrNull(), Math.random);
    // Before init: the SDK reads its session cookie as it starts.
    keepSessionWithoutCookies();
    hdx.init(
      buildSdkOptions({ ...config, replay }, ingest, window.location.origin),
    );
    // After init: both hook what the SDK RECORDS, never what the app sees.
    const benign = benignFilter(config, hdx);
    filterSdkErrorSpans(benign);
    tapSdkInputs(benign);
    // Every span carries where it came from and which visitor made it; the
    // product-event view reads these as app.client and visitor.id.
    // With attribution deferred (attribution: false), visitor.id joins them
    // when enableAttribution() runs.
    if (config.attribution !== false) {
      attributionWanted = true;
    }
    const globals: Record<string, string> = {
      "app.client": detectAppClient(config.appClient),
    };
    if (attributionWanted) {
      globals["visitor.id"] = captureVisitor(config);
    }
    hdx.setGlobalAttributes?.(globals);
    runningClient = hdx;
    runningConfig = config;
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
      disable: () => {
        // Back to "no SDK": reportException returns false, so a caller falls
        // back to console.error and the error does not vanish.
        setExceptionReporter(null);
        hdx.stopSessionRecorder?.();
      },
    });
    return true;
  } catch (err) {
    // Blocked chunk, ad-blocker, SDK throw: telemetry must never break the app.
    // A startup that failed after the hooks went in takes them out again.
    removeBenignFilter();
    console.debug("[telemetry] disabled:", err);
    setTelemetrySink(null);
    return false;
  }
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
 * want to see exactly what is configured — it is pure.
 */
export function buildSdkOptions(
  config: BrowserTelemetryConfig,
  ingestUrl: string,
  pageOrigin: string,
): Record<string, unknown> {
  // `config.replay` is the FINAL decision here. initBrowserTelemetry resolves
  // replaySampleRate into it (decideReplay) before calling this.
  const replay = config.replay === true;
  const resourceAttributes: Record<string, string> = {};
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
        applyCustomAttributesOnSpan: stripNetworkQuery,
      },
      xhr: {
        propagateTraceHeaderCorsUrls: targets,
        advancedNetworkCapture: () => advancedNetworkCapture,
        applyCustomAttributesOnSpan: stripNetworkQuery,
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
    replay: { inlineImages: false, collectFonts: false },
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
 * usable store the draw is made each load. Pure given its inputs; exported for
 * tests.
 */
export function decideReplay(
  config: Pick<BrowserTelemetryConfig, "replay" | "replaySampleRate">,
  store: DecisionStore | null,
  random: () => number,
): boolean {
  if (config.replay !== true) {
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

/** See "One session per visit" in the header. Never throws. */
function keepSessionWithoutCookies(): void {
  try {
    if (typeof document === "undefined") {
      return;
    }
    installSessionCookieFallback(document, window.localStorage ?? null);
  } catch {
    // localStorage access can throw (blocked storage): keep the SDK default.
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
}

// applyCustomAttributesOnSpan for both instrumentations: it runs before the
// span ends, so the rewritten value is the one exported.
function stripNetworkQuery(span: UrlSpan): void {
  for (const key of URL_ATTRIBUTES) {
    const value = span.attributes?.[key];
    if (typeof value === "string") {
      const stripped = stripUrlQuery(value);
      if (stripped !== value) {
        span.setAttribute(key, stripped);
      }
    }
  }
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
