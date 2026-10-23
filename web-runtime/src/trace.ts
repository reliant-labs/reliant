// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// W3C trace-context propagation for the browser.
//
// The headline capability: EVERY Connect call carries a valid `traceparent`
// header, so the backend (which runs otelconnect + otelhttp with a
// TraceContext propagator) joins the SAME distributed trace. This works
// WITHOUT any OTLP collector configured — traceparent generation is
// decoupled from span export.
//
//   - If the app opted into full OpenTelemetry tracing (src/lib/otel.ts
//     ran initTelemetry with an endpoint, registering a WebTracerProvider),
//     there is an active recording span; we inject ITS context so the
//     browser span and the server span share one trace id.
//   - Otherwise we synthesize a fresh, valid W3C traceparent from a CSPRNG.
//     The backend still roots a trace we can correlate this request to, and
//     no browser-side collector is required.
import { context, propagation } from "@opentelemetry/api";

/** Matches a well-formed W3C traceparent: version-traceid-spanid-flags. */
export const TRACEPARENT_RE = /^00-[0-9a-f]{32}-[0-9a-f]{16}-0[0-9a-f]$/;

const ZERO_TRACE_ID = "0".repeat(32);
const ZERO_SPAN_ID = "0".repeat(16);

/**
 * Whether a SYNTHESIZED traceparent (the no-active-span fallback below)
 * marks the trace as sampled/recorded.
 *
 * Default true — "correlation-safe": a browser-initiated request is always
 * recorded, so it can be joined to its server span even when the backend
 * uses head-based sampling (this module's whole promise is "every call is
 * correlatable"). Flip it to false via setTraceSampled() to instead DEFER to
 * the server's sampler — cheaper at high volume, but a synthesized trace the
 * server samples out leaves that browser request uncorrelated. Only affects
 * the synthesized path; when a real OpenTelemetry span is active, ITS
 * sampling flag is propagated verbatim (see injectTraceContext).
 */
let traceSampled = true;

/** Override the sampled flag on synthesized traceparents. Default true. */
export function setTraceSampled(sampled: boolean): void {
  traceSampled = sampled;
}

/**
 * randomHex returns `bytes` random bytes as a lowercase hex string using a
 * CSPRNG. Falls back to Math.random only if Web Crypto is unavailable (old
 * embedded webviews); the fallback is fine for a correlation id that carries
 * no security weight.
 */
function randomHex(bytes: number): string {
  const buf = new Uint8Array(bytes);
  const c = globalThis.crypto;
  if (c && typeof c.getRandomValues === "function") {
    c.getRandomValues(buf);
  } else {
    for (let i = 0; i < bytes; i++) {
      buf[i] = Math.floor(Math.random() * 256);
    }
  }
  let out = "";
  for (const b of buf) {
    out += b.toString(16).padStart(2, "0");
  }
  return out;
}

/**
 * freshTraceparent synthesizes a valid W3C traceparent. The trace-flags byte
 * reflects the configurable sampled flag (setTraceSampled; default 01 =
 * sampled). Trace/span ids are re-rolled on the astronomically-unlikely
 * all-zero draw (the spec forbids all-zero ids).
 */
export function freshTraceparent(): string {
  let traceId = randomHex(16);
  while (traceId === ZERO_TRACE_ID) {
    traceId = randomHex(16);
  }
  let spanId = randomHex(8);
  while (spanId === ZERO_SPAN_ID) {
    spanId = randomHex(8);
  }
  const flags = traceSampled ? "01" : "00";
  return `00-${traceId}-${spanId}-${flags}`;
}

/**
 * TRACERESPONSE_HEADER is the W3C Trace Context Level 2 response header a
 * forge server sets on every Connect response, failed ones included: the
 * context of ITS server span (forge/pkg/observe TraceResponseInterceptor).
 * It is the authoritative answer to "which trace did this request land in" —
 * the traceparent the browser sent is only a request, which a server that does
 * not trust remote parents, or a proxy that restarts the trace, overrides.
 */
export const TRACERESPONSE_HEADER = "traceresponse";

/**
 * traceIdFrom extracts the trace id from a traceparent or traceresponse value
 * (`00-<trace-id>-<span-id>-<flags>`), or returns null for anything malformed
 * or an all-zero id.
 */
export function traceIdFrom(value: string | null | undefined): string | null {
  if (!value || !TRACEPARENT_RE.test(value.trim())) {
    return null;
  }
  const traceId = value.trim().split("-")[1] ?? null;
  return traceId === ZERO_TRACE_ID ? null : traceId;
}

/**
 * injectTraceContext writes a `traceparent` (and any accompanying W3C
 * context headers, e.g. `tracestate`) onto the outgoing request headers.
 *
 * Prefers an active OpenTelemetry span so a fully-instrumented app stitches
 * the browser and backend spans into one trace; otherwise falls back to a
 * synthesized traceparent so propagation ALWAYS happens. Idempotent per
 * request — safe to call once per attempt.
 */
export function injectTraceContext(header: Headers): void {
  const carrier: Record<string, string> = {};
  propagation.inject(context.active(), carrier, {
    set(c, k, v) {
      c[k] = String(v);
    },
  });

  if (carrier.traceparent && TRACEPARENT_RE.test(carrier.traceparent)) {
    for (const [k, v] of Object.entries(carrier)) {
      header.set(k, v);
    }
    return;
  }

  header.set("traceparent", freshTraceparent());
}
