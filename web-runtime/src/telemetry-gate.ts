// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The hand-off between browser telemetry startup and the first RPCs.
//
// The HyperDX SDK is a ~600 KB lazy chunk, so it patches `fetch` some hundreds
// of milliseconds after the app is interactive — and a page fires its first
// queries on mount, inside that window. A call made before the patch carries a
// SYNTHESIZED traceparent (trace.ts) that names a span no browser ever
// exported: the API's span has a parent id that matches nothing, and "this page
// load, end to end" has no answer exactly for the requests the page makes
// first.
//
// So initBrowserTelemetry registers its startup here, and the trace interceptor
// waits for it — at most TELEMETRY_WAIT_MS, so a blocked chunk or an ad blocker
// delays a request by that much and no more, then falls back to the synthesized
// traceparent. With telemetry off nothing is registered and nothing waits.
//
// DOM-free and import-free on purpose: the transport entry point
// (@reliantlabs/forge-web-runtime/interceptors) imports this and must stay
// light enough for React Native.

/** The longest an RPC waits for browser telemetry to finish starting. */
export const TELEMETRY_WAIT_MS = 2000;

let startup: Promise<unknown> | null = null;

/** Called once by initBrowserTelemetry with the promise of its startup. */
export function registerTelemetryStartup(p: Promise<unknown>): void {
  startup = p;
}

/**
 * Resolves when telemetry has finished starting (or failed to), or after
 * `maxMs`, whichever is first. Resolves immediately when none is starting or it
 * has already settled.
 */
export function telemetryReady(
  maxMs: number = TELEMETRY_WAIT_MS,
): Promise<void> {
  const p = startup;
  if (!p) {
    return Promise.resolve();
  }
  return new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, maxMs);
    const done = () => {
      clearTimeout(timer);
      resolve();
    };
    p.then(done, done);
  });
}

/** Test seam. */
export function resetTelemetryGate(): void {
  startup = null;
}
