// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Reading the server trace id off a thrown value, with NO dependencies.
//
// devlog.ts and error-reporter.ts are imported by every frontend, including the
// ones that never install OpenTelemetry, so they cannot import trace.ts (which
// imports @opentelemetry/api) or errors.ts (which imports @connectrpc/connect).
// They need one fact from a ConnectClientError — its traceId — and read it
// structurally here: the same shape-over-identity rule errors.ts applies to
// `instanceof`.

/** Attribute key reportException stamps the server's trace id under. */
export const SERVER_TRACE_ID_ATTRIBUTE = "server.trace_id";

const TRACE_ID_RE = /^[0-9a-f]{32}$/;

/**
 * serverTraceIdOf returns the trace id a failed request was recorded in — the
 * `traceId` a ConnectClientError carries (errors.ts) — for `value` or anything
 * on its `cause` chain, or null.
 */
export function serverTraceIdOf(value: unknown): string | null {
  let v: unknown = value;
  for (
    let depth = 0;
    depth < 5 && v !== null && typeof v === "object";
    depth++
  ) {
    const id = (v as { traceId?: unknown }).traceId;
    if (typeof id === "string" && TRACE_ID_RE.test(id)) {
      return id;
    }
    v = (v as { cause?: unknown }).cause;
  }
  return null;
}
