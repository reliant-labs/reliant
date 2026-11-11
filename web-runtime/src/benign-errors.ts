// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Errors a browser raises that are not defects, kept out of the error pipeline
// and counted instead.
//
// Two shapes made up most of a real app's browser error_events once Sentry's
// defaults stopped filtering them (reliant, 2026-10-29: 41 of 45 rows):
//
//   - ABORT. An `AbortError` is a cancelled operation: the user navigated away,
//     a component unmounted, a newer request superseded an older one. When
//     nobody awaited the promise it surfaces as an unhandled rejection
//     ("signal is aborted without reason"), and it describes nothing that broke.
//   - NETWORK. The browser's own generic fetch failure — a TypeError reading
//     "Failed to fetch" (Chromium, now with " (host)" appended), "Load failed"
//     (Safari) or "NetworkError when attempting to fetch resource." (Firefox).
//     The browser says only that the request did not complete: offline, a
//     captive portal, a tab suspended mid-request. One flaky connection writes
//     dozens, and they bury the errors that are bugs.
//
// A filtered error is not discarded silently: each one is COUNTED, by kind and
// by where it was seen, and the counts are emitted as one
// `forge.errors_filtered` action per kind and source at most once a minute (and
// when the page is hidden), so a real wave of network failures — an outage, a
// bad deploy of an API — still shows as a spike in that count.
//
// What is never filtered: anything that is not exactly those shapes. A
// ConnectError (a Connect RPC's error, including one that wraps a failed fetch)
// is the app's own request failing and carries a code and a server trace; it is
// kept even when its message reads "Failed to fetch". So is a TypeError with
// any other message, and a non-Error value.
//
// The classifier is a seam: BrowserTelemetryConfig.classifyBenignError replaces
// it, and `() => null` turns filtering off.

/** Why an error was filtered. */
export type BenignErrorKind = "abort" | "network";

/** Where a filtered error was seen. */
export type BenignErrorSource =
  | "unhandledrejection"
  | "error"
  | "console.error"
  | "reportException";

/** A classifier: the kind of a benign error, or null for one to keep. */
export type BenignErrorClassifier = (error: unknown) => string | null;

/** The action name the counts are emitted under. */
export const FILTERED_ERRORS_ACTION = "forge.errors_filtered";

/** How often, at most, the counts are flushed while the page is visible. */
export const FILTERED_ERRORS_FLUSH_MS = 60_000;

// The browsers' generic fetch failures, whole message only. Chromium appends
// the host in parentheses ("Failed to fetch (api.example.com)").
const GENERIC_NETWORK_MESSAGE =
  /^(?:Failed to fetch|Load failed|NetworkError when attempting to fetch resource\.?|Network request failed)(?: \([^()]*\))?$/;

function nameOf(error: object): string {
  const name = (error as { name?: unknown }).name;
  return typeof name === "string" ? name : "";
}

/**
 * The default classifier. See the module header for exactly what it filters
 * and what it never does.
 */
export function classifyBenignError(error: unknown): BenignErrorKind | null {
  if (typeof error !== "object" || error === null) {
    return null;
  }
  const name = nameOf(error);
  // A ConnectError is the app's own RPC failing: always kept.
  if (name === "ConnectError" || name === "ConnectClientError") {
    return null;
  }
  if (name === "AbortError") {
    return "abort";
  }
  if (name === "TypeError") {
    const message = (error as { message?: unknown }).message;
    if (
      typeof message === "string" &&
      GENERIC_NETWORK_MESSAGE.test(message.trim())
    ) {
      return "network";
    }
  }
  return null;
}

/** Emits one action. Never throws on the caller's behalf: the caller guards it. */
type Emit = (name: string, attributes: Record<string, string | number>) => void;

/**
 * Counts filtered errors and emits them as `forge.errors_filtered` actions.
 * One instance per running SDK.
 */
export class FilteredErrorCounter {
  private counts = new Map<
    string,
    { kind: string; source: string; count: number }
  >();
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly emit: Emit,
    private readonly flushMs: number = FILTERED_ERRORS_FLUSH_MS,
  ) {}

  /** Count one filtered error. */
  note(kind: string, source: BenignErrorSource): void {
    const key = `${kind}\u0000${source}`;
    const entry = this.counts.get(key);
    if (entry) {
      entry.count += 1;
    } else {
      this.counts.set(key, { kind, source, count: 1 });
    }
    if (this.timer === null) {
      this.timer = setTimeout(() => this.flush(), this.flushMs);
    }
  }

  /** Emit every pending count and reset. A no-op with nothing counted. */
  flush(): void {
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    const pending = [...this.counts.values()];
    this.counts.clear();
    for (const { kind, source, count } of pending) {
      try {
        this.emit(FILTERED_ERRORS_ACTION, { kind, source, count });
      } catch {
        // Telemetry must never break the app.
      }
    }
  }
}
