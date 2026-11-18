// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Frame stalls: the main thread held a frame back long enough for a person
// to feel it, measured in every browser.
//
// ── Why ──────────────────────────────────────────────────────────────────
//
// The SDK records long tasks from the Long Tasks API, which WebKit does not
// have: an iPhone (every iOS browser is WebKit) reports none, so real-user
// data from the devices most likely to stall shows no stalls at all. What
// every browser has is requestAnimationFrame. Its callbacks run once per
// frame, so the time between two consecutive callbacks is how long that frame
// took; a gap over FRAME_STALL_MS (50 ms, the long-task threshold) is a frame
// the page could not paint, whatever held it (script, style, layout, GC).
//
// ── When it watches ──────────────────────────────────────────────────────
//
// A rAF loop that never stops would wake the main thread 60 times a second
// for the life of the page, a battery cost for nothing while nobody looks.
// So it runs only while a stall would be felt: for LOAD_WINDOW_MS from the
// start, and for ACTIVE_WINDOW_MS after each input (pointerdown, keydown,
// wheel, touchmove, scroll). Never while the page is hidden: rAF pauses then,
// and the gap would measure the time away.
//
// ── What it records ──────────────────────────────────────────────────────
//
// One `frame.stall` span per stall, from the last frame to the late one, with
// `frame.gap_ms` and `frame.trigger` (`load` or `input`: which window caught
// it). At most MAX_FRAME_STALLS per page. Stalls before the SDK runs (load is
// when most happen) are held and recorded once it does, at their own times.

/** A frame gap at least this long (ms) is a stall. */
export const FRAME_STALL_MS = 50;

/** How long (ms) the watch runs from the start. */
export const LOAD_WINDOW_MS = 10_000;

/** How long (ms) the watch runs after each input. */
export const ACTIVE_WINDOW_MS = 5_000;

/** Most stalls recorded per page. */
export const MAX_FRAME_STALLS = 100;

/** The span name of a stall. */
export const FRAME_STALL_SPAN_NAME = "frame.stall";

/** The tracer stalls are recorded on. */
export const FRAME_STALL_SCOPE = "@reliantlabs/forge-web-runtime/frames";

const INPUT_EVENTS = [
  "pointerdown",
  "keydown",
  "wheel",
  "touchmove",
  "scroll",
] as const;

/** One stall: when the last good frame was, and how long the next took. */
export interface FrameStall {
  /** ms since the time origin, as performance.now(). */
  start: number;
  gap: number;
  trigger: "load" | "input";
}

/** The watch initBrowserTelemetry holds for the page. */
export interface FrameStallWatch {
  /** Hands every held stall, in order, and every later one to `emit`. */
  attach(emit: (stall: FrameStall) => void): void;
  /** Stops watching and drops anything held. */
  stop(): void;
}

/**
 * Starts watching for frame stalls. Null without a DOM or rAF. `enabled` is
 * asked before a stall is handed on.
 */
export function watchFrameStalls(
  enabled: () => boolean,
): FrameStallWatch | null {
  if (
    typeof window === "undefined" ||
    typeof document === "undefined" ||
    typeof requestAnimationFrame !== "function" ||
    typeof window.addEventListener !== "function"
  ) {
    return null;
  }
  // The browser's own rAF, taken now, before the SDK starts: its context
  // manager wraps requestAnimationFrame to carry the active span into the
  // callback, and a loop that re-schedules itself would then hold whatever
  // context the input that woke it ran in (an action's span) open for good.
  const raf = requestAnimationFrame.bind(globalThis);
  let held: FrameStall[] = [];
  let emit: ((stall: FrameStall) => void) | null = null;
  let recorded = 0;
  let stopped = false;
  let running = false;
  let last: number | undefined;
  let until = now() + LOAD_WINDOW_MS;
  let trigger: FrameStall["trigger"] = "load";

  const record = (stall: FrameStall) => {
    if (recorded >= MAX_FRAME_STALLS) {
      return;
    }
    recorded++;
    if (!emit) {
      held.push(stall);
      return;
    }
    if (enabled()) {
      try {
        emit(stall);
      } catch {
        // Telemetry must never break the app.
      }
    }
  };
  const hidden = () => document.visibilityState === "hidden";
  const frame = (t: number) => {
    if (stopped) {
      running = false;
      return;
    }
    if (hidden()) {
      // rAF stops while hidden; the next frame after showing measures nothing.
      last = undefined;
    } else {
      if (last !== undefined && t - last >= FRAME_STALL_MS) {
        record({ start: last, gap: t - last, trigger });
      }
      last = t;
    }
    if (t < until && !hidden()) {
      raf(frame);
    } else {
      running = false;
      last = undefined;
    }
  };
  const run = () => {
    if (!running && !stopped) {
      running = true;
      raf(frame);
    }
  };
  const onInput = () => {
    until = Math.max(until, now() + ACTIVE_WINDOW_MS);
    if (!running) {
      trigger = "input";
    }
    run();
  };
  const onVisibility = () => {
    last = undefined;
    if (!hidden() && now() < until) {
      run();
    }
  };
  const options = { capture: true, passive: true } as const;
  for (const type of INPUT_EVENTS) {
    window.addEventListener(type, onInput, options);
  }
  document.addEventListener?.("visibilitychange", onVisibility);
  if (!hidden()) {
    run();
  }

  return {
    attach(next) {
      emit = next;
      const queued = held;
      held = [];
      if (!enabled()) {
        return;
      }
      for (const stall of queued) {
        try {
          next(stall);
        } catch {
          // Telemetry must never break the app.
        }
      }
    },
    stop() {
      stopped = true;
      emit = null;
      held = [];
      for (const type of INPUT_EVENTS) {
        window.removeEventListener?.(type, onInput, options);
      }
      document.removeEventListener?.("visibilitychange", onVisibility);
    },
  };
}

function now(): number {
  return typeof performance !== "undefined" &&
    typeof performance.now === "function"
    ? performance.now()
    : Date.now();
}
