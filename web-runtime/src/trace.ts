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
import {
  propagation,
  trace,
  TraceFlags,
  type Context,
} from "@opentelemetry/api";

import { outgoingContext } from "./interaction-context.js";
import { rumSessionId } from "./telemetry-gate.js";

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

/** A random, non-zero W3C trace id. Internal: not part of the barrel. */
export function randomTraceId(): string {
  let traceId = randomHex(16);
  while (traceId === ZERO_TRACE_ID) {
    traceId = randomHex(16);
  }
  return traceId;
}

/** A random, non-zero W3C span id. Internal: not part of the barrel. */
export function randomSpanId(): string {
  let spanId = randomHex(8);
  while (spanId === ZERO_SPAN_ID) {
    spanId = randomHex(8);
  }
  return spanId;
}

/** The trace flags a synthesized context carries (setTraceSampled). Internal. */
export function synthesizedTraceFlags(): number {
  return traceSampled ? TraceFlags.SAMPLED : TraceFlags.NONE;
}

/**
 * freshTraceparent synthesizes a valid W3C traceparent. The trace-flags byte
 * reflects the configurable sampled flag (setTraceSampled; default 01 =
 * sampled). Trace/span ids are re-rolled on the astronomically-unlikely
 * all-zero draw (the spec forbids all-zero ids).
 */
export function freshTraceparent(): string {
  const flags = traceSampled ? "01" : "00";
  return `00-${randomTraceId()}-${randomSpanId()}-${flags}`;
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
 *
 * `ctx` defaults to the context the request should join right now: the
 * active one, or the open interaction (see interaction.ts) when no span is
 * active. With no SDK registered there is no propagator, so a valid span
 * context in `ctx` — an interaction's — is written out directly, and every
 * RPC of the interaction shares its trace id.
 */
export function injectTraceContext(
  header: Headers,
  ctx: Context = outgoingContext(),
): void {
  const carrier: Record<string, string> = {};
  propagation.inject(ctx, carrier, {
    set(c, k, v) {
      c[k] = String(v);
    },
  });

  const sc = trace.getSpanContext(ctx);
  if (carrier.traceparent && TRACEPARENT_RE.test(carrier.traceparent)) {
    for (const [k, v] of Object.entries(carrier)) {
      header.set(k, v);
    }
  } else if (sc && trace.isSpanContextValid(sc)) {
    const flags = (sc.traceFlags & 0xff).toString(16).padStart(2, "0");
    header.set("traceparent", `00-${sc.traceId}-${sc.spanId}-${flags}`);
  } else {
    header.set("traceparent", freshTraceparent());
  }
  const session = rumSessionId();
  if (session) {
    header.set("baggage", withSessionBaggage(header.get("baggage"), session));
  }
}

/**
 * withSessionBaggage adds the `session.id` member to a W3C baggage value,
 * replacing one already there, and keeps every other member.
 */
export function withSessionBaggage(
  existing: string | null,
  session: string,
): string {
  const others = (existing ?? "")
    .split(",")
    .map((m) => m.trim())
    .filter((m) => m !== "" && m.split("=")[0]?.trim() !== SESSION_BAGGAGE_KEY);
  return [...others, `${SESSION_BAGGAGE_KEY}=${session}`].join(",");
}

/** The baggage member forge servers read the RUM session from. */
export const SESSION_BAGGAGE_KEY = "session.id";
