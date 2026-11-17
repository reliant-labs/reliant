// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// When the telemetry SDK starts, and how its startup gives the main thread
// back. See "Loading" in telemetry.ts for why.
//
// ── When ─────────────────────────────────────────────────────────────────
//
// The SDK's chunk costs one long main-thread task to evaluate (~540 ms at 4x
// CPU on a phone-class emulation, measured on reliantlabs.io), and its init
// another. Started as soon as the page was visible, that landed right after
// first paint: inside hydration, where a tap waited behind it. In "idle" mode
// (the default) startup waits for the first of:
//
//   - the page has loaded (the `load` event, or START_LOAD_TIMEOUT_MS) and the
//     browser is then idle (requestIdleCallback, at most START_IDLE_TIMEOUT_MS;
//     where there is none, as in Safari, the first quiet moment a timer probe
//     finds, at most as long);
//   - the page has loaded and the person has interacted (pointerdown or
//     keydown), once that interaction's frame has painted: someone using the
//     page should be measured, and their tap should not wait for the SDK;
//   - startNow(): something needs the SDK now (an RPC whose traceparent
//     should name a span the browser exports; telemetry-gate.ts).
//
// "eager" starts at once, as every release before this one did.
//
// ── Giving the main thread back ──────────────────────────────────────────
//
// yieldToMain() ends the current task and resumes in a new one:
// scheduler.yield() where the browser has it (the continuation keeps its
// priority), else a zero-delay timer. Startup yields between its stages so the
// evaluation of the chunk, the SDK's init and the replay of what was held are
// separate tasks, with input handled in between.

/** When initBrowserTelemetry starts the SDK. */
export type TelemetryStart = "idle" | "eager";

/** The longest an idle start waits for the `load` event. */
export const START_LOAD_TIMEOUT_MS = 10_000;

/** The longest an idle start waits, once the page has loaded, for idle time. */
export const START_IDLE_TIMEOUT_MS = 2_000;

/** How long a quiet-moment probe's timer is (no requestIdleCallback). */
const PROBE_MS = 50;

/** How late a probe timer may fire and still count as a quiet moment. */
const PROBE_SLACK_MS = 10;

/** What made startup begin. */
export type StartReason = "eager" | "idle" | "interaction" | "now" | "timeout";

/** A pending start: resolves once, with what began it. */
export interface StartGate {
  readonly ready: Promise<StartReason>;
  /** Begin now, whatever the browser is doing. */
  startNow(): void;
  /** Release every listener and timer; `ready` never resolves after it. */
  cancel(): void;
}

/**
 * Begins waiting for the moment to start the SDK in `mode`. Without a DOM
 * there is no paint to protect: it resolves at once, as "eager" does.
 */
export function startGate(mode: TelemetryStart | undefined): StartGate {
  let resolve!: (reason: StartReason) => void;
  const ready = new Promise<StartReason>((r) => (resolve = r));
  const undo: Array<() => void> = [];
  let done = false;
  const cleanup = () => {
    for (const fn of undo.splice(0)) {
      try {
        fn();
      } catch {
        // Telemetry must never break the app.
      }
    }
  };
  const fire = (reason: StartReason) => {
    if (done) {
      return;
    }
    done = true;
    cleanup();
    resolve(reason);
  };
  const gate: StartGate = {
    ready,
    startNow: () => fire("now"),
    cancel: () => {
      done = true;
      cleanup();
    },
  };
  if (
    mode === "eager" ||
    typeof window === "undefined" ||
    typeof document === "undefined" ||
    typeof window.addEventListener !== "function"
  ) {
    fire("eager");
    return gate;
  }

  const timer = (fn: () => void, ms: number) => {
    const id = setTimeout(fn, ms);
    undo.push(() => clearTimeout(id));
  };
  const listen = (target: EventTarget, type: string, fn: () => void) => {
    const options = { capture: true, passive: true } as const;
    target.addEventListener(type, fn, options);
    undo.push(() => target.removeEventListener?.(type, fn, options));
  };

  let loaded = false;
  let interacted = false;
  const onLoaded = () => {
    if (loaded || done) {
      return;
    }
    loaded = true;
    if (interacted) {
      afterNextPaint(() => fire("interaction"), timer);
      return;
    }
    whenIdle(fire, timer, undo);
  };
  const onInteraction = () => {
    if (interacted || done) {
      return;
    }
    interacted = true;
    if (loaded) {
      afterNextPaint(() => fire("interaction"), timer);
    }
  };
  listen(window, "pointerdown", onInteraction);
  listen(window, "keydown", onInteraction);

  const state = document.readyState;
  if (state === "loading" || state === "interactive") {
    listen(window, "load", onLoaded);
    timer(onLoaded, START_LOAD_TIMEOUT_MS);
  } else {
    onLoaded();
  }
  return gate;
}

/** Calls `fn` in a new task once the next frame has painted. */
function afterNextPaint(
  fn: () => void,
  timer: (fn: () => void, ms: number) => void,
): void {
  if (typeof requestAnimationFrame === "function") {
    requestAnimationFrame(() => timer(fn, 0));
    // A hidden page paints no frames: do not wait on one forever.
    timer(fn, 250);
    return;
  }
  timer(fn, 0);
}

interface IdleWindow {
  requestIdleCallback?: (
    fn: () => void,
    options?: { timeout: number },
  ) => number;
  cancelIdleCallback?: (id: number) => void;
}

/** Fires "idle" in the browser's next idle period, or "timeout". */
function whenIdle(
  fire: (reason: StartReason) => void,
  timer: (fn: () => void, ms: number) => void,
  undo: Array<() => void>,
): void {
  const w = window as unknown as IdleWindow;
  if (typeof w.requestIdleCallback === "function") {
    const id = w.requestIdleCallback(() => fire("idle"), {
      timeout: START_IDLE_TIMEOUT_MS,
    });
    undo.push(() => w.cancelIdleCallback?.(id));
    return;
  }
  // No idle callbacks (Safari): two short timers in a row that fire on time
  // mean nothing else held the main thread in between.
  timer(() => fire("timeout"), START_IDLE_TIMEOUT_MS);
  let quiet = 0;
  const probe = () => {
    const due = now() + PROBE_MS;
    timer(() => {
      quiet = now() - due <= PROBE_SLACK_MS ? quiet + 1 : 0;
      if (quiet >= 2) {
        fire("idle");
      } else {
        probe();
      }
    }, PROBE_MS);
  };
  probe();
}

function now(): number {
  return typeof performance !== "undefined" &&
    typeof performance.now === "function"
    ? performance.now()
    : Date.now();
}

interface YieldingScheduler {
  yield?: () => Promise<void>;
}

/**
 * Ends the current task; resolves at the start of a new one. Never rejects.
 */
export function yieldToMain(): Promise<void> {
  const scheduler = (globalThis as { scheduler?: YieldingScheduler }).scheduler;
  if (typeof scheduler?.yield === "function") {
    return scheduler.yield().catch(() => {});
  }
  return new Promise((resolve) => setTimeout(resolve, 0));
}
