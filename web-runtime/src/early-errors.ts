// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The errors a page raises before the telemetry SDK runs.
//
// The SDK records uncaught errors, unhandled rejections and failed resource
// loads from the moment it is initialised, and it starts after the page has
// loaded (telemetry.ts, "Loading"). What fails first, in the seconds a page
// is most likely to fail in, would be lost. So initBrowserTelemetry installs
// these listeners at once and holds what they see, bounded, until the SDK
// runs; then the held errors are recorded, each with when it happened and
// how long it waited, and the listeners go.
//
// The listeners only observe: every app handler sees every event exactly as
// it would with no telemetry at all. The window `error` listener is in the
// capture phase, so it also sees a resource that failed to load (an <img>,
// a <script>), which does not bubble.

import type { ExceptionAttributes } from "./error-reporter.js";

/** Most errors held before the SDK runs. Later ones are dropped. */
export const MAX_HELD_ERRORS = 50;

/** One error held for the SDK. */
export type HeldError =
  | {
      /** An uncaught error, an unhandled rejection, or reportException. */
      kind: "error" | "unhandledrejection" | "reportException";
      /** The thrown value, rejection reason, or message of a cross-origin "Script error.". */
      value: unknown;
      attributes?: ExceptionAttributes;
      /** When it happened, ms since the epoch. */
      at: number;
    }
  | {
      /** A resource (an <img>, a <script>) that failed to load. */
      kind: "resource";
      target: Element;
      at: number;
    };

/** The early listeners initBrowserTelemetry holds until the SDK runs. */
export interface EarlyErrors {
  /** Hold one error handed to reportException. */
  hold(value: unknown, attributes?: ExceptionAttributes): void;
  /** Stop listening, and hand over everything held, in order. */
  take(): HeldError[];
  /** Stop listening, and drop everything held. */
  stop(): void;
}

/** Starts holding the page's errors. Null without a DOM. */
export function holdEarlyErrors(): EarlyErrors | null {
  if (
    typeof window === "undefined" ||
    typeof window.addEventListener !== "function"
  ) {
    return null;
  }
  let held: HeldError[] = [];
  const push = (error: HeldError) => {
    if (held.length < MAX_HELD_ERRORS) {
      held.push(error);
    }
  };
  const onError = (event: Event) => {
    try {
      const e = event as ErrorEvent;
      if (e.error !== undefined && e.error !== null) {
        push({ kind: "error", value: e.error, at: Date.now() });
      } else if (typeof e.message === "string" && e.message !== "") {
        push({ kind: "error", value: e.message, at: Date.now() });
      } else if (isElement(event.target)) {
        push({ kind: "resource", target: event.target, at: Date.now() });
      }
    } catch {
      // Telemetry must never break the app.
    }
  };
  const onRejection = (event: Event) => {
    try {
      push({
        kind: "unhandledrejection",
        value: (event as PromiseRejectionEvent).reason,
        at: Date.now(),
      });
    } catch {
      // Telemetry must never break the app.
    }
  };
  window.addEventListener("error", onError, true);
  window.addEventListener("unhandledrejection", onRejection, true);
  const unlisten = () => {
    window.removeEventListener?.("error", onError, true);
    window.removeEventListener?.("unhandledrejection", onRejection, true);
  };
  return {
    hold(value, attributes) {
      push({ kind: "reportException", value, attributes, at: Date.now() });
    },
    take() {
      unlisten();
      const out = held;
      held = [];
      return out;
    },
    stop() {
      unlisten();
      held = [];
    },
  };
}

function isElement(target: EventTarget | null): target is Element {
  return (
    typeof target === "object" &&
    target !== null &&
    (target as { nodeType?: unknown }).nodeType === 1 &&
    typeof (target as { getAttribute?: unknown }).getAttribute === "function"
  );
}
