// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The seam between "something threw" and "somebody recorded it".
//
// The error boundary and the Next.js error files live in the barrel, which
// every frontend imports — including the Vite SPA and React Native ones that
// never install the HyperDX browser SDK. So the barrel cannot import the SDK.
// It imports this dependency-free registry instead, and the `/telemetry`
// subpath (which does import the SDK) installs the real recorder here once the
// SDK has initialised. Until then — SSR, telemetry off, SDK still loading —
// reportException is a no-op, which is what makes it safe to call from any
// catch block unconditionally.

import { SERVER_TRACE_ID_ATTRIBUTE, serverTraceIdOf } from "./trace-id.js";

/** Span/log attribute values the recorder accepts. */
export type ExceptionAttributes = Record<string, string | number | boolean>;

type ExceptionReporter = (
  error: unknown,
  attributes?: ExceptionAttributes,
) => void;

/**
 * Reports which recorder is installed, if any. reportException's return
 * value is "the telemetry SDK has this error" — true both when it was just
 * recorded and when it had already been recorded by another route.
 */

let reporter: ExceptionReporter | null = null;

/**
 * Install (or clear, with null) the recorder reportException forwards to.
 * Called by initBrowserTelemetry; an app has no reason to call it directly.
 */
export function setExceptionReporter(next: ExceptionReporter | null): void {
  reporter = next;
}

/**
 * reportException hands a caught error to the browser telemetry SDK, if one
 * is running. Never throws: reporting an error must not become a second
 * error.
 *
 * Returns true when the SDK has the error and false when no SDK is running —
 * so a caller can fall back to console.error and an error never vanishes:
 *
 *     if (!reportException(error)) console.error(error);
 *
 * Use it where React or Next catch an error for you (error boundaries,
 * `error.tsx`). The SDK already records uncaught exceptions, unhandled
 * rejections and anything passed to console.error, and React logs a
 * boundary-caught error to console.error itself, so the recorder installed
 * by `/telemetry` skips an error the SDK has already seen: one failure is
 * one record, never two.
 *
 * A failed RPC's error (ConnectClientError) carries the trace the server
 * recorded it in; it is added as the `server.trace_id` attribute, so the
 * browser record links to the server's spans and logs. An attribute of that
 * name passed by the caller wins.
 */
export function reportException(
  error: unknown,
  attributes?: ExceptionAttributes,
): boolean {
  if (!reporter) {
    return false;
  }
  try {
    const traceId = serverTraceIdOf(error);
    reporter(
      error,
      traceId
        ? { [SERVER_TRACE_ID_ATTRIBUTE]: traceId, ...attributes }
        : attributes,
    );
  } catch {
    // Telemetry must never break the app.
  }
  return true;
}
