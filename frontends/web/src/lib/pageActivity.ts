/**
 * How long the page has actually been running, and when it comes back from
 * not running.
 *
 * Wall-clock time lies about a web page. A phone locks, an iPhone switches
 * apps, a laptop lid closes: the page stops executing, and every timer and
 * stopwatch in it keeps reading `Date.now()` as though it had been waiting the
 * whole time. Measured in prod (Sentry ELECTRON-B2): an iOS tab reported
 * "ListChats in flight for 619s" against a server that had answered it in 14ms
 * ten minutes earlier — the response was lost while the tab was frozen, and the
 * page could not tell "the server is slow" from "I was asleep".
 *
 * This module answers two questions for the transport:
 *
 *   1. `activeNow()` — a clock that advances only while the page is visible
 *      and running. A request's deadline measured on it is a budget of time
 *      the user actually spent waiting, not time the phone spent in a pocket.
 *
 *   2. `onResume()` — the moments the page comes back: hidden → visible after
 *      a real absence, the network coming back (`online`), a back/forward-cache
 *      restore (`pageshow` persisted), a Page Lifecycle `resume`, and a wall
 *      clock that jumped while visible (a laptop that slept with the tab in
 *      front, where no visibility event fires at all). A request that was in
 *      flight across one of these rode a connection that may no longer exist;
 *      the transport uses the signal to replace it instead of waiting out its
 *      timeout.
 */

/**
 * Why the page is considered to have resumed. `connection-lost` is not a page
 * event: the app's long-lived update stream reports it when its connection
 * fails at the network level, which every request multiplexed onto the same
 * HTTP/2 connection shares.
 */
export type ResumeReason =
  | "visible"
  | "online"
  | "pageshow"
  | "resume"
  | "clock-jump"
  | "connection-lost";

export interface ResumeEvent {
  reason: ResumeReason;
  /** Wall-clock time the resume was observed. */
  at: number;
  /** How long the page was hidden or suspended, when known (0 for `online`). */
  inactiveMs: number;
}

/**
 * Hidden for less than this is a tab flick, not an absence. The connection
 * survives it, and treating it as a resume would replace healthy requests.
 */
export const RESUME_MIN_INACTIVE_MS = 3_000;

/** How often the visible page checks its own clock for a jump. */
export const CLOCK_TICK_MS = 1_000;

/**
 * A tick that arrives this much later than scheduled means the page did not
 * run in between (machine sleep, OS freeze). Generous on purpose: a busy main
 * thread can delay a tick by a second or two, and mistaking that for a sleep
 * would needlessly replace in-flight requests.
 */
export const CLOCK_JUMP_THRESHOLD_MS = 10_000;

type Listener = (event: ResumeEvent) => void;

interface EventSourceLike {
  addEventListener(type: string, listener: (event: Event) => void): void;
  removeEventListener(type: string, listener: (event: Event) => void): void;
}

interface DocumentLike extends EventSourceLike {
  visibilityState?: string;
}

export interface PageActivityOptions {
  now?: () => number;
  doc?: DocumentLike | null;
  win?: EventSourceLike | null;
}

export class PageActivity {
  private readonly now: () => number;
  private readonly doc: DocumentLike | null;
  private readonly win: EventSourceLike | null;
  private readonly listeners = new Set<Listener>();

  /** Active time banked before the current visible stretch began. */
  private activeBase = 0;
  /** Wall time the current visible stretch began, or null while hidden. */
  private activeSince: number | null = null;
  /** Wall time the page was last hidden, or null while visible. */
  private hiddenAt: number | null = null;
  private lastTick = 0;
  private ticker: ReturnType<typeof setInterval> | null = null;
  private started = false;

  constructor(options: PageActivityOptions = {}) {
    this.now = options.now ?? (() => Date.now());
    this.doc = options.doc !== undefined ? options.doc : typeof document !== "undefined" ? document : null;
    this.win = options.win !== undefined ? options.win : typeof window !== "undefined" ? window : null;
  }

  /** Begin observing. Idempotent; the clock reads 0 until started. */
  start(): void {
    if (this.started) return;
    this.started = true;
    const now = this.now();
    if (this.isHidden()) {
      this.hiddenAt = now;
    } else {
      this.activeSince = now;
    }
    this.lastTick = now;
    this.doc?.addEventListener("visibilitychange", this.onVisibilityChange);
    this.win?.addEventListener("online", this.onOnline);
    this.win?.addEventListener("pageshow", this.onPageShow);
    // Page Lifecycle API (Chromium): fired when a frozen page is thawed.
    this.doc?.addEventListener("resume", this.onLifecycleResume);
    this.ticker = setInterval(this.onTick, CLOCK_TICK_MS);
  }

  stop(): void {
    if (!this.started) return;
    this.started = false;
    this.doc?.removeEventListener("visibilitychange", this.onVisibilityChange);
    this.win?.removeEventListener("online", this.onOnline);
    this.win?.removeEventListener("pageshow", this.onPageShow);
    this.doc?.removeEventListener("resume", this.onLifecycleResume);
    if (this.ticker !== null) clearInterval(this.ticker);
    this.ticker = null;
  }

  /** Milliseconds the page has spent visible and running since `start()`. */
  activeNow(): number {
    if (this.activeSince === null) return this.activeBase;
    return this.activeBase + Math.max(0, this.now() - this.activeSince);
  }

  isHidden(): boolean {
    return this.doc?.visibilityState === "hidden";
  }

  /**
   * The connection to the API failed under the app (the update stream saw a
   * network error, or went silent past its watchdog). Requests started before
   * now may be riding the same dead connection.
   */
  noteConnectionLost(): void {
    this.emit({ reason: "connection-lost", at: this.now(), inactiveMs: 0 });
  }

  onResume(listener: Listener): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  private emit(event: ResumeEvent): void {
    for (const listener of [...this.listeners]) {
      try {
        listener(event);
      } catch {
        // One listener's failure must not stop the others from recovering.
      }
    }
  }

  private onVisibilityChange = (): void => {
    const now = this.now();
    if (this.isHidden()) {
      if (this.activeSince !== null) {
        this.activeBase += Math.max(0, now - this.activeSince);
        this.activeSince = null;
      }
      this.hiddenAt ??= now;
      return;
    }
    if (this.activeSince !== null) return; // already visible
    this.activeSince = now;
    this.lastTick = now;
    const inactiveMs = this.hiddenAt === null ? 0 : Math.max(0, now - this.hiddenAt);
    this.hiddenAt = null;
    if (inactiveMs >= RESUME_MIN_INACTIVE_MS) {
      this.emit({ reason: "visible", at: now, inactiveMs });
    }
  };

  private onOnline = (): void => {
    this.emit({ reason: "online", at: this.now(), inactiveMs: 0 });
  };

  private onPageShow = (event: Event): void => {
    if (!(event as PageTransitionEvent).persisted) return;
    this.emit({ reason: "pageshow", at: this.now(), inactiveMs: 0 });
  };

  private onLifecycleResume = (): void => {
    this.emit({ reason: "resume", at: this.now(), inactiveMs: 0 });
  };

  private onTick = (): void => {
    const now = this.now();
    const sinceLast = now - this.lastTick;
    this.lastTick = now;
    // Hidden tabs are throttled by design; a late tick there means nothing.
    if (this.activeSince === null) return;
    if (sinceLast < CLOCK_TICK_MS + CLOCK_JUMP_THRESHOLD_MS) return;
    // The page did not run between the last tick and this one. Bank the time
    // up to the last tick plus one interval as active, drop the rest.
    const lastTick = now - sinceLast;
    this.activeBase += Math.max(0, lastTick - this.activeSince) + CLOCK_TICK_MS;
    this.activeSince = now;
    this.emit({ reason: "clock-jump", at: now, inactiveMs: sinceLast - CLOCK_TICK_MS });
  };
}

let _shared: PageActivity | null = null;

/** The app-wide instance, started on first use. */
export function pageActivity(): PageActivity {
  if (!_shared) {
    _shared = new PageActivity();
    _shared.start();
  }
  return _shared;
}

/** Test seam: drop the shared instance so the next caller starts fresh. */
export function resetPageActivityForTests(): void {
  _shared?.stop();
  _shared = null;
}
