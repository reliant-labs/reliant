// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// This module is also the package's SECOND ENTRY POINT, published as
// "@reliantlabs/forge-web-runtime/interceptors". It is the DOM-free, React-free
// half of the runtime: the transport layer and nothing else. Importing it
// pulls in exactly ./errors and ./trace — no React, no @types/react, no
// window/document. That matters for React Native, where Expo pins React 18
// while the package's components are typed against React 19: a mobile app
// importing the full barrel would typecheck the components it never renders
// and fail. Everything re-exported at the bottom of this file is part of
// this entry point's surface; keep it React-free (guarded by
// interceptors-entry.test.ts).
//
// The Connect transport interceptor stack. This is the one place every
// frontend→backend call flows through, composed once and handed to
// createConnectTransport by the app's own connect module. Order (outermost
// first):
//
//   error-normalize → auth → brand headers → traceparent
//
// error-normalize is outermost so every caller sees a typed
// ConnectClientError instead of a raw ConnectError.
//
// RETRY OWNERSHIP — read before adding a retry layer.
// The single retry owner is the app's React Query client: it
// structurally knows reads from writes, so it retries transient READ
// failures and NEVER retries a mutation — no duplicate writes. Each React
// Query retry re-runs the queryFn, which re-runs this whole interceptor
// chain (fresh token + traceparent), so nothing is lost by not retrying
// here. `retryInterceptor` below is therefore NOT in the default chain; it
// stays exported (idempotency-gated) only for callers who use the raw
// transport OUTSIDE React Query. Putting it back in the default chain would
// stack two retry layers AND retry non-idempotent mutations — the bug this
// split fixed.
import { MethodOptions_IdempotencyLevel } from "@bufbuild/protobuf/wkt";
import { context } from "@opentelemetry/api";

import { ConnectClientError, normalizeError } from "./errors.js";
import { telemetryReady } from "./telemetry-gate.js";
import { injectTraceContext, traceIdFrom } from "./trace.js";

import type {
  Interceptor,
  UnaryRequest,
  StreamRequest,
} from "@connectrpc/connect";

/**
 * RuntimeInterceptorConfig is the seam the app wires its runtime dependencies
 * through. Everything is optional: an unconfigured chain still propagates
 * traceparent and normalizes errors.
 */
export interface RuntimeInterceptorConfig {
  /** Returns the current bearer token, or null when unauthenticated. */
  getToken?: () => Promise<string | null> | string | null;
  /** Returns the active brand, attached as the X-Brand header. */
  getBrand?: () => string | null | undefined;
  /** Invoked with the typed error on every failed RPC (telemetry sink). */
  onError?: (err: ConnectClientError) => void;
  /** Max transparent retries for retryable unary failures. Default 2. */
  maxRetries?: number;
  /** Base backoff in ms; grows exponentially with jitter. Default 200. */
  retryBaseMs?: number;
}

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

/** Attaches the bearer token from the app's auth provider. */
export function authInterceptor(cfg: RuntimeInterceptorConfig): Interceptor {
  return (next) => async (req) => {
    const parent = context.active();
    const token = cfg.getToken ? await cfg.getToken() : null;
    if (token) {
      req.header.set("Authorization", `Bearer ${token}`);
    }
    // Browser stack context managers cannot carry context through native
    // await. Restore the caller's context before entering the next boundary.
    return context.with(parent, () => next(req));
  };
}

/** Attaches brand context headers when the app supplies them. */
export function headerInterceptor(cfg: RuntimeInterceptorConfig): Interceptor {
  return (next) => async (req) => {
    const brand = cfg.getBrand?.();
    if (brand) {
      req.header.set("X-Brand", brand);
    }
    return next(req);
  };
}

/**
 * Propagates a W3C traceparent so the backend (otelconnect + otelhttp) joins
 * the same distributed trace. Always emits a valid header — see trace.ts.
 *
 * While browser telemetry is still starting it waits (bounded; see
 * telemetry-gate.ts) so the call is made through the SDK's patched fetch and
 * its traceparent names a span the browser really exports.
 */
export function traceInterceptor(): Interceptor {
  return (next) => async (req) => {
    const parent = context.active();
    await telemetryReady();
    return context.with(parent, () => {
      injectTraceContext(req.header);
      return next(req);
    });
  };
}

/** Converts any thrown failure into a typed ConnectClientError. */
export function errorNormalizeInterceptor(
  cfg: RuntimeInterceptorConfig,
): Interceptor {
  return (next) => async (req) => {
    try {
      return await next(req);
    } catch (err) {
      // traceInterceptor (innermost) has written the traceparent onto this
      // same request by now: the fallback when the server sent no
      // traceresponse.
      const normalized = normalizeError(
        err,
        traceIdFrom(req.header.get("traceparent")),
      );
      cfg.onError?.(normalized);
      throw normalized;
    }
  };
}

/**
 * retryEligible reports whether a request is SAFE to retry transparently:
 * a no-side-effects / idempotent method (declared via proto
 * `idempotency_level`), or one carrying an explicit Idempotency-Key header.
 * A plain mutation (the default IDEMPOTENCY_UNKNOWN) is never retried, so a
 * transparent retry can't produce a duplicate write.
 */
function retryEligible(req: UnaryRequest | StreamRequest): boolean {
  const level = req.method.idempotency;
  if (
    level === MethodOptions_IdempotencyLevel.NO_SIDE_EFFECTS ||
    level === MethodOptions_IdempotencyLevel.IDEMPOTENT
  ) {
    return true;
  }
  return req.header.has("Idempotency-Key");
}

/**
 * Transparently retries retryable failures with exponential backoff +
 * jitter — for IDEMPOTENT requests ONLY (see retryEligible). NOT part of the
 * default chain: React Query owns retries (see the retry-ownership note at
 * the top of this file). Exported for callers using the raw transport
 * outside React Query. Streaming RPCs are never retried (a partially-
 * consumed stream can't be safely replayed).
 */
export function retryInterceptor(cfg: RuntimeInterceptorConfig): Interceptor {
  const max = cfg.maxRetries ?? 2;
  const base = cfg.retryBaseMs ?? 200;
  return (next) => async (req) => {
    if (req.stream || !retryEligible(req)) {
      return next(req);
    }
    let attempt = 0;
    for (;;) {
      try {
        return await next(req);
      } catch (err) {
        const normalized =
          err instanceof ConnectClientError ? err : normalizeError(err);
        if (attempt >= max || !normalized.retryable) {
          throw normalized;
        }
        const backoff = base * 2 ** attempt + Math.random() * base;
        await sleep(backoff);
        attempt++;
      }
    }
  };
}

/**
 * buildRuntimeInterceptors composes the full stack in the correct order.
 * Pass the result to createConnectTransport's `interceptors` option.
 *
 * retryInterceptor is deliberately NOT included — React Query is the single
 * retry owner (see the retry-ownership note at the top of this file).
 */
export function buildRuntimeInterceptors(
  cfg: RuntimeInterceptorConfig = {},
): Interceptor[] {
  return [
    errorNormalizeInterceptor(cfg),
    authInterceptor(cfg),
    headerInterceptor(cfg),
    traceInterceptor(),
  ];
}

// ── The rest of the transport-layer surface ──────────────────────────────
//
// These belong to this entry point because the interceptor stack's contract
// is expressed in them: errorNormalizeInterceptor THROWS ConnectClientError
// and RuntimeInterceptorConfig.onError RECEIVES one, so a consumer cannot
// write a handler without the type; setTraceSampled is traceInterceptor's
// only knob. A consumer of "@reliantlabs/forge-web-runtime/interceptors" must
// never have to fall back to the React-typed barrel to complete the job.

export {
  codeName,
  ConnectClientError,
  FORGE_ERROR_REASON_HEADER,
  isRetryableCode,
  normalizeError,
} from "./errors.js";

export {
  freshTraceparent,
  injectTraceContext,
  setTraceSampled,
  traceIdFrom,
  TRACEPARENT_RE,
  TRACERESPONSE_HEADER,
} from "./trace.js";
