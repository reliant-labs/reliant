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
  detectAppClient,
  takeIdentifyProps,
} from "./attribution.js";
import { setExceptionReporter } from "./error-reporter.js";
import { registerTelemetryStartup } from "./telemetry-gate.js";
import {
  isTelemetryDisabled,
  setTelemetrySink,
  type TrackValue,
} from "./track.js";

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
    hdx.init(
      buildSdkOptions({ ...config, replay }, ingest, window.location.origin),
    );
    tapSdkInputs();
    // Every span carries where it came from and which visitor made it; the
    // product-event view reads these as app.client and visitor.id.
    hdx.setGlobalAttributes?.({
      "app.client": detectAppClient(config.appClient),
      "visitor.id": captureAttribution({
        visitorIdStorageKey: config.visitorIdStorageKey,
      }),
    });
    setExceptionReporter((error, attributes) => {
      if (alreadyRecorded(error)) {
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

function tapSdkInputs(): void {
  if (tapped) {
    return;
  }
  tapped = true;
  const wrapped = console.error;
  console.error = function (this: unknown, ...args: unknown[]) {
    for (const arg of args) {
      if (arg instanceof Error) {
        noteSeen(arg);
      }
    }
    return wrapped.apply(this, args);
  };
  window.addEventListener("error", (e) => noteSeen(e.error), true);
  window.addEventListener(
    "unhandledrejection",
    (e) => noteSeen(e.reason),
    true,
  );
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
