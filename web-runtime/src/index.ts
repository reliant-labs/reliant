// @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Public barrel. Import batteries from "@reliantlabs/forge-web-runtime".
//
// Everything here is MECHANISM: the transport interceptor stack, the session,
// the error boundary, the toast queue, the <Resource> tristate ladder, W3C
// trace propagation, client RUM. Nothing here reaches into an application —
// app-shaped values (auth state, event subscriptions, toast markup) arrive as
// props, so an app can restyle or replace its own files freely.
//
// Presentation that a project is meant to own — the component library under
// src/components/ui, globals.css, nav, layout — deliberately stays in the
// scaffold and is NOT part of this package.

// The per-group docs below are JSDoc, not `//`, on purpose: TypeScript's
// declaration emit preserves JSDoc and DROPS line comments, and dist/ is now
// the whole of what this package publishes. `forge project libraries` reads
// this inventory out of the published barrel, and a consumer's editor shows
// it on hover — both would get symbols with no explanation otherwise.

/** Transport interceptor stack. */
export {
  authInterceptor,
  buildRuntimeInterceptors,
  errorNormalizeInterceptor,
  headerInterceptor,
  retryInterceptor,
  traceInterceptor,
  type RuntimeInterceptorConfig,
} from "./interceptors.js";

/**
 * Typed client errors, and the backend-framing strippers every surface that
 * SHOWS an error goes through.
 */
export {
  codeName,
  ConnectClientError,
  FORGE_ERROR_REASON_HEADER,
  isRetryableCode,
  normalizeError,
  stripServerFraming,
  userMessage,
} from "./errors.js";

/**
 * Mount-prefix helpers, for the URLs Next.js's router never sees — absolute
 * URLs handed to external systems that round-trip back into the app.
 *
 * Barrel-exported rather than behind a subpath: three string functions with
 * no imports, so there is no dependency footprint for a subpath to fence off.
 */
export {
  createBasePath,
  joinBasePath,
  normalizeBasePath,
  type BasePathHelpers,
} from "./basepath.js";

/**
 * Money and basis points as integers: format minor units through Intl, and
 * parse form input back to minor units without rounding.
 *
 * The web counterpart of forge/pkg/money — an int64 count of the currency's
 * minor unit plus an ISO 4217 code, exact past 2^53. Barrel-exported for the
 * same reason as the base-path helpers: it imports nothing.
 */
export {
  basisPointsToInput,
  currencyMinorDigits,
  formatBasisPoints,
  formatMinorUnits,
  minorUnitsToInput,
  parseBasisPoints,
  parseMinorUnits,
  type BasisPointsOptions,
  type MoneyOptions,
  type ParseBasisPointsOptions,
  type ParseMoneyOptions,
} from "./money.js";

/** W3C trace-context propagation. */
export {
  freshTraceparent,
  injectTraceContext,
  setTraceSampled,
  traceIdFrom,
  TRACEPARENT_RE,
  TRACERESPONSE_HEADER,
} from "./trace.js";

/** Session: the signed-in user, derived from the app's auth state and JWT claims. */
export {
  decodeJwt,
  SessionProvider,
  useSession,
  type JwtClaims,
  type Session,
  type SessionAuth,
} from "./session.js";

/** The app-shell provider set: session + error boundary + toast host in one. */
export { RuntimeShell, type RuntimeShellProps } from "./providers.js";

/** Error boundary with a designed crash fallback, for a route or a subtree. */
export {
  DefaultErrorFallback,
  RuntimeErrorBoundary,
} from "./error-boundary.js";

/** The toast queue. The app supplies the event source and the markup. */
export {
  RuntimeToastHost,
  type RuntimeToast,
  type RuntimeToastHostProps,
  type RuntimeToastPayload,
  type RuntimeToastSink,
  type RuntimeToastVariant,
} from "./toast-host.js";

/** Route guarding: render a page only to a signed-in caller. */
export { RouteGuard, type RouteGuardProps } from "./route-guard.js";

/** Generic data-table container: the tristate ladder, filter and cursor pagination. */
export {
  Resource,
  type ResourceColumn,
  type ResourceProps,
  type ResourceStatus,
} from "./resource.js";

/**
 * Record a caught error with the browser telemetry SDK, when one is running.
 * A no-op otherwise, so it is safe in any catch block. The SDK itself arrives
 * through the "/telemetry" subpath, never the barrel.
 */
export {
  reportException,
  setExceptionReporter,
  type ExceptionAttributes,
} from "./error-reporter.js";

/** The server trace id a failed RPC's error carries (ConnectClientError.traceId). */
export { SERVER_TRACE_ID_ATTRIBUTE, serverTraceIdOf } from "./trace-id.js";

/**
 * Product events, the signed-in user and the consent kill-switch for browser
 * telemetry. Like reportException these are safe to call anywhere — no-ops on
 * the server and when telemetry is off — and calls made while the SDK is still
 * loading are held and replayed. The SDK itself arrives through the
 * "/telemetry" subpath, never the barrel.
 */
export {
  disableTelemetry,
  setTelemetryUser,
  track,
  type TrackProps,
  type TrackValue,
} from "./track.js";

/**
 * Visitor identity and campaign attribution: `withAttribution(url)` appends
 * this browser's `rid` and captured `utm_*` to an outbound link (a marketing
 * site pointing at the app); the runtime reads them on arrival and emits one
 * `user_identified` event at first sign-in. Consent-gated like track().
 * `getAttribution()` is the captured record (to stamp on a site's own events);
 * the storage keys are exported so a site that withdraws consent can erase them.
 */
export {
  ATTRIBUTION_STORAGE_KEY,
  IDENTIFIED_USER_STORAGE_KEY,
  UTM_PARAMS,
  VISITOR_ID_STORAGE_KEY,
  getAttribution,
  withAttribution,
  type AppClient,
  type Attribution,
  type AttributionTouch,
} from "./attribution.js";

/**
 * Dev-only browser log forwarding: mirrors console output and uncaught errors
 * to the dev server, which prints them into
 * `.forge/logs/<env>/frontend_<name>.log`. No-ops unless `dev: true`, and the
 * receiving endpoint exists only in a dev server — so a production bundle
 * neither posts nor has anywhere to post to.
 */
export {
  DEV_LOG_ENDPOINT,
  devLoggingInstalled,
  installDevLogging,
  uninstallDevLogging,
  type DevLoggingOptions,
  type DevLogPayload,
} from "./devlog.js";
