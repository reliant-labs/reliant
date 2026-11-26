// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Typed client errors. Connect surfaces failures as ConnectError with a
// numeric Code; the rest of the app should reason about a stable, typed
// shape instead of sniffing status codes at every call site. normalizeError
// maps any thrown value into a ConnectClientError — a ConnectError subclass,
// so connect-es consumers keep working — carrying the canonical code name,
// an HTTP-ish status, a retryability verdict, the backend's reason, and a
// human-presentable message.
import { Code, ConnectError } from "@connectrpc/connect";

import { TRACERESPONSE_HEADER, traceIdFrom } from "./trace.js";

/**
 * FORGE_ERROR_REASON_HEADER is the Connect trailer/metadata key carrying a
 * stable, machine-readable reason code (snake_case) the backend sets for
 * every error. UI/routing keys off `error.reason` instead of matching
 * human-readable message strings — the message text is display-only and
 * may change. The backend owns the setter; the frontend only reads it.
 */
export const FORGE_ERROR_REASON_HEADER = "x-forge-error-reason";

/**
 * stripServerFraming removes the transport framing that must never surface
 * to a user: the leading "[code] " gRPC-code prefix connect-es puts on
 * `ConnectError.message`. What survives is the message the server wrote,
 * which is exactly what a user should read.
 *
 * This is the SINGLE implementation — a project never wants a different
 * answer here, and a copy frozen into a scaffold could never be corrected
 * once shipped. Idempotent: framing that has already been stripped strips
 * to itself.
 */
export function stripServerFraming(message: string): string {
  return message.replace(/^\[[a-z_]+\]\s*/i, "").trim();
}

/** The copy shown when an error carries no presentable message of its own. */
const DEFAULT_USER_MESSAGE = "Something went wrong. Please try again.";

/**
 * userMessage — turn any thrown value into copy fit for end users.
 *
 * `ConnectError.message` prefixes the gRPC code ("[not_found] no such task");
 * `rawMessage` is the server's human-readable text without that framing, and
 * stripServerFraming drops the prefix from whichever of the two is available.
 * Use this everywhere an error is SHOWN (banners, toasts) instead of
 * `err.message`.
 *
 * Pass `fallback` to override the default copy for a surface that wants its
 * own wording.
 */
export function userMessage(
  err: unknown,
  fallback: string = DEFAULT_USER_MESSAGE,
): string {
  if (err instanceof ConnectError) {
    return stripServerFraming(err.rawMessage || err.message) || fallback;
  }
  if (err instanceof Error) {
    return stripServerFraming(err.message) || fallback;
  }
  return err == null ? fallback : stripServerFraming(String(err)) || fallback;
}

/** Connect codes worth a transparent transport-level retry. */
const RETRYABLE = new Set<Code>([
  Code.Unavailable,
  Code.DeadlineExceeded,
  Code.Aborted,
  Code.ResourceExhausted,
]);

/** Rough Connect-code → HTTP-status mapping for display/telemetry only. */
const HTTP_STATUS: Partial<Record<Code, number>> = {
  [Code.InvalidArgument]: 400,
  [Code.Unauthenticated]: 401,
  [Code.PermissionDenied]: 403,
  [Code.NotFound]: 404,
  [Code.AlreadyExists]: 409,
  [Code.ResourceExhausted]: 429,
  [Code.Unimplemented]: 501,
  [Code.Unavailable]: 503,
  [Code.DeadlineExceeded]: 504,
  [Code.Internal]: 500,
};

/**
 * codeName — the canonical wire name of a Connect code: `Code.NotFound` →
 * "not_found", `Code.DeadlineExceeded` → "deadline_exceeded". The spelling
 * the Connect protocol puts on the wire and in the "[code] " message prefix.
 *
 * (An earlier version lower-cased the enum key, which produced "notfound" —
 * a name that matches nothing the server or the docs spell.)
 */
export function codeName(code: Code): string {
  const key = Code[code];
  if (typeof key !== "string") return "unknown";
  return key.replace(
    /[A-Z]/g,
    (c, i: number) => (i === 0 ? "" : "_") + c.toLowerCase(),
  );
}

/**
 * isConnectClientErrorShape — the STRUCTURAL test behind
 * {@link ConnectClientError}'s `instanceof`.
 *
 * A class object is per module instance, not per package. npm nests a
 * transitive dependency on any version conflict, and a bundler can pull one
 * package in as both ESM and CJS, so a tree holding two copies of this
 * package holds two distinct ConnectClientError classes. Matching on shape
 * instead of identity is what makes the two agree: the shape is the
 * contract, the constructor is not.
 *
 * The shape is a ConnectError's (numeric `code`, `metadata`, `rawMessage`)
 * plus the fields this class adds; `name` is "ConnectError" for the reason
 * given on the class.
 */
function isConnectClientErrorShape(v: unknown): v is ConnectClientError {
  if (!(v instanceof Error)) return false;
  if (Object.getPrototypeOf(v) === ConnectClientError.prototype) return true;
  const e = v as Partial<ConnectClientError>;
  return (
    v.name === "ConnectError" &&
    typeof e.code === "number" &&
    typeof e.codeName === "string" &&
    typeof e.rawMessage === "string" &&
    typeof e.status === "number" &&
    typeof e.retryable === "boolean" &&
    (typeof e.reason === "string" || e.reason === null)
  );
}

/** Detail types a ConnectError carries; spelled off the base class's field. */
type ConnectErrorDetails = ConnectError["details"];

/**
 * ConnectClientError is the single typed error shape the app catches.
 *
 * It IS a ConnectError — a subclass — so the error the runtime's
 * errorNormalizeInterceptor throws still satisfies every consumer written
 * against connect-es: `err instanceof ConnectError` holds, `err.code` is the
 * numeric `Code`, and `metadata`, `details`, `findDetails()` and `rawMessage`
 * are the original error's. What it adds is the normalized view: `codeName`,
 * `status`, `retryable`, `reason` and `traceId`. An app that already branches
 * on ConnectError can therefore adopt the runtime chain one call site at a
 * time; nothing downgrades to `Code.Unknown` because a catch block met a type
 * it did not expect.
 *
 * `name` stays "ConnectError". That is load-bearing: connect-es's
 * `ConnectError[Symbol.hasInstance]` accepts an object whose prototype is
 * exactly `ConnectError.prototype`, or one NAMED "ConnectError" with the
 * right fields — a subclass instance is the second case, so any other name
 * fails `instanceof ConnectError`.
 *
 * `message` is the server's text WITHOUT the "[code] " framing (the same as
 * `rawMessage`), so it is presentable at every layer; connect-es's own
 * `message` carries the prefix. Render with {@link userMessage} regardless.
 */
export class ConnectClientError extends ConnectError {
  /** Canonical Connect code name, e.g. "unavailable", "not_found". */
  readonly codeName: string;
  /** Approximate HTTP status, for telemetry and generic UI. */
  readonly status: number;
  /** True when a transparent retry is reasonable (transient failures). */
  readonly retryable: boolean;
  /**
   * Stable machine-readable reason code from the backend
   * (x-forge-error-reason metadata), or null. Key UI/routing off THIS,
   * never off `message` — message text is display-only.
   */
  readonly reason: string | null;
  /**
   * The trace the failed request was recorded in: the server's
   * `traceresponse` when it sent one, else the trace id of the traceparent
   * this runtime sent, else null (the request never left the browser). Put it
   * in a bug report, a toast's "copy details", or a support ticket — it is the
   * key to every server span and log line of that request.
   */
  readonly traceId: string | null;

  /**
   * `status` and `retryable` default from `code`; `reason` defaults to the
   * x-forge-error-reason entry in `metadata`.
   */
  constructor(args: {
    /** The server's message, without "[code] " framing. */
    message: string;
    code: Code;
    metadata?: HeadersInit;
    details?: ConnectErrorDetails;
    status?: number;
    retryable?: boolean;
    reason?: string | null;
    traceId?: string | null;
    cause?: unknown;
  }) {
    super(args.message, args.code, args.metadata, undefined, args.cause);
    // ConnectError's constructor takes only OUTGOING details (a server's);
    // a client error carries the INCOMING ones it received, so they are
    // assigned rather than passed. Its name, "ConnectError", is kept (above).
    if (args.details) this.details = args.details;
    this.message = args.message;
    this.codeName = codeName(args.code);
    this.status = args.status ?? HTTP_STATUS[args.code] ?? 500;
    this.retryable = args.retryable ?? isRetryableCode(args.code);
    this.reason =
      args.reason !== undefined
        ? args.reason
        : this.metadata.get(FORGE_ERROR_REASON_HEADER) || null;
    this.traceId = args.traceId ?? null;
  }

  /**
   * `instanceof` matches on SHAPE, not on class identity — so every check
   * spelled `err instanceof ConnectClientError`, here and in app code, keeps
   * working when two copies of this package end up in one bundle (see
   * {@link isConnectClientErrorShape}). Without it, normalizeError's
   * pass-through guard misses an already-normalized error from the other
   * copy and re-wraps it as a fresh error. @connectrpc/connect does the same
   * on ConnectError, for the same reason.
   */
  static [Symbol.hasInstance](v: unknown): boolean {
    return isConnectClientErrorShape(v);
  }
}

/** isRetryable reports whether a Connect code merits a transport retry. */
export function isRetryableCode(code: Code): boolean {
  return RETRYABLE.has(code);
}

/**
 * normalizeError coerces any thrown value into a ConnectClientError. Already-
 * normalized errors pass through unchanged so the interceptor chain never
 * double-wraps. `requestTraceId` is the trace id of the traceparent the
 * request carried, used when the server sent no `traceresponse`.
 *
 * Both `instanceof` checks below match on SHAPE rather than class identity —
 * ours via ConnectClientError's Symbol.hasInstance, ConnectError's via the one
 * @connectrpc/connect ships — so neither branch is missed when a second copy
 * of either package lands in the bundle. The ConnectClientError check comes
 * first because every ConnectClientError is also a ConnectError.
 */
export function normalizeError(
  err: unknown,
  requestTraceId: string | null = null,
): ConnectClientError {
  if (err instanceof ConnectClientError) {
    return err;
  }

  if (err instanceof ConnectError) {
    return new ConnectClientError({
      // Strip the "[code] " prefix so the message is user-presentable at
      // every layer.
      message: stripServerFraming(err.rawMessage || err.message),
      code: err.code,
      // The original metadata and details ride along, so `findDetails()` and
      // `metadata.get(...)` answer exactly as they did on the raw error. The
      // reason (backend-owned) is read from that metadata by the constructor.
      metadata: err.metadata,
      details: err.details,
      // The server's own span when it answered; the trace this runtime asked
      // for when it did not (a proxy error, an older server).
      traceId:
        traceIdFrom(err.metadata.get(TRACERESPONSE_HEADER)) ?? requestTraceId,
      cause: err,
    });
  }

  if (err instanceof Error) {
    // An aborted or timed-out fetch is a cancellation, as ConnectError.from
    // reads it.
    if (err.name === "AbortError" || err.name === "TimeoutError") {
      return new ConnectClientError({
        message: stripServerFraming(err.message),
        code: Code.Canceled,
        traceId: requestTraceId,
        cause: err,
      });
    }
    // A network fault (fetch rejected: offline, DNS, CORS preflight, TLS)
    // surfaces as a TypeError — that, and only that, is a safe transparent
    // retry. A generic Error is NOT blanket-retried (blanket-retrying every
    // bare Error widened the blast radius to non-idempotent failures).
    const networkFault = err instanceof TypeError;
    return new ConnectClientError({
      message: stripServerFraming(err.message),
      code: networkFault ? Code.Unavailable : Code.Unknown,
      status: networkFault ? 503 : 500,
      retryable: networkFault,
      reason: null,
      traceId: requestTraceId,
      cause: err,
    });
  }

  return new ConnectClientError({
    message:
      typeof err === "string" ? stripServerFraming(err) : "Unknown error",
    code: Code.Unknown,
    status: 500,
    retryable: false,
    reason: null,
    cause: err,
  });
}
