// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Engaged time as a product event: one `page_engaged {route, view_id,
// engaged_ms, scroll_pct}` per page view, so "how long did people spend on this post"
// is a median over product events and needs no code in the app.
//
// ── What counts as engaged ───────────────────────────────────────────────
//
// Time is counted only while the page is VISIBLE, the window has FOCUS (or
// the person is interacting with it anyway: a wheel over a background
// window) and the person has been ACTIVE in the last ENGAGEMENT_IDLE_MS:
// a pointer move or press, a key, a wheel, a touch or a scroll. Reading
// without touching anything for longer than that stops the clock at the
// cutoff, and the next input starts it again; nothing runs in the meantime
// (the cutoff is applied when the time is next read, so there is no timer
// per page). A view's total is capped at MAX_ENGAGED_MS: a tab left open
// over lunch with a wiggling mouse is not an hour of reading.
//
// ── When it is reported ──────────────────────────────────────────────────
//
// Once per page view, at the first of:
//
//   a route change      the view that ended (SPAs: the same route watch
//                       that records page_viewed, page-views.ts)
//   visibilitychange    the tab was hidden: switched away, minimised,
//     to hidden         locked, or the first step of closing it
//   pagehide            the page is being unloaded or put in the
//                       back/forward cache (a static multi-page site's
//                       only report)
//
// The event goes through track()'s product-event path, so it carries the
// session, the release and the analytics tier like every other event, and is
// sent in every tier but `off`. Its one identifier, `view_id`, names the page
// VIEW, not a person: 16 random hex characters minted when the view starts,
// kept only in memory, and never reused by another view.
// On hide and unload it then asks the SDK to export at once
// (flushProductEvents): the exporter posts with `fetch(…, {keepalive: true})`,
// which the browser completes after the page is gone, as it does
// sendBeacon. The listeners are on `window` in the capture phase, ahead of
// the SDK's own `document` listener that flushes on hide.
//
// A view the person comes back to (the tab shown again, or the page restored
// from the back/forward cache) keeps counting, and its next report carries
// the same `view_id`, `resumed: "true"` and the view's CUMULATIVE engaged_ms
// and scroll_pct. A reader of the events takes, per view_id, the last (the
// largest) report (see the product-events skill). The id is what makes that
// unambiguous: a back/forward-cache restore can resume a view of a route the
// same visitor has since viewed again, so route and order alone cannot tell
// which view a resumed row belongs to. A view with less than MIN_ENGAGED_MS of
// engagement (a redirect, a route replaced at once) is not reported.
//
// ── scroll_pct ───────────────────────────────────────────────────────────
//
// How far down the DOCUMENT the bottom of the viewport has been during the
// view, 0-100, the maximum seen: (scrollY + innerHeight) / scrollHeight. A
// page that fits the viewport is 100. It is measured on every document
// scroll, shortly after the view starts (the new route has rendered by
// then), and again when the view is hidden. An app whose content scrolls
// inside its own element rather than the document reports the share of the
// document in view, usually 100: depth is meant for content pages.
//
// Dependency-free like page-views.ts: nothing here imports the SDK.
import { flushProductEvents, recordProductEvent } from "./track.js";

/** The product event engaged time is recorded as. */
export const PAGE_ENGAGED_EVENT = "page_engaged";

/** Inactivity after which the clock stops (at the cutoff, not at the next input). */
export const ENGAGEMENT_IDLE_MS = 30_000;

/** Most engaged time one page view reports. */
export const MAX_ENGAGED_MS = 30 * 60_000;

/** Least engaged time a page view must have to be reported at all. */
export const MIN_ENGAGED_MS = 500;

/** How long after a view starts its first scroll depth is measured. */
export const SCROLL_SETTLE_MS = 1_000;

// What counts as the person being there. Passive and in the capture phase:
// the app's own handlers see every event unchanged and first-hand.
const ACTIVITY_EVENTS = [
  "pointerdown",
  "pointermove",
  "keydown",
  "wheel",
  "touchstart",
] as const;

/**
 * The engaged-time clock of one page view: visible, focused and active time,
 * with the idle cutoff applied lazily. Times are milliseconds on any
 * monotonic clock. Pure; exported for tests.
 */
export class EngagementClock {
  private committed = 0;
  private since = 0;
  private lastActive = 0;
  private running = false;

  constructor(private readonly idleMs: number = ENGAGEMENT_IDLE_MS) {}

  /** Whether the page is visible and focused (the clock may count). */
  get counting(): boolean {
    return this.running;
  }

  /** The page became visible and focused at `t`; or, while counting, input. */
  start(t: number): void {
    if (this.running) {
      this.activity(t);
      return;
    }
    this.running = true;
    this.since = t;
    this.lastActive = t;
  }

  /** The page was hidden or lost focus at `t`. */
  stop(t: number): void {
    if (!this.running) {
      return;
    }
    this.committed = this.elapsed(t);
    this.running = false;
  }

  /** Input at `t`. After an idle gap, the time past the cutoff is not counted. */
  activity(t: number): void {
    if (!this.running) {
      return;
    }
    if (t - this.lastActive > this.idleMs) {
      this.committed += Math.max(0, this.lastActive + this.idleMs - this.since);
      this.since = t;
    }
    this.lastActive = Math.max(this.lastActive, t);
  }

  /** Engaged milliseconds up to `t`. */
  elapsed(t: number): number {
    if (!this.running) {
      return this.committed;
    }
    const end = Math.min(t, this.lastActive + this.idleMs);
    return this.committed + Math.max(0, end - this.since);
  }
}

/**
 * The scroll depth of the document now, 0-100 (see the header). Pure over
 * the DOM it reads; exported for tests.
 */
export function documentScrollPercent(): number {
  if (typeof window === "undefined" || typeof document === "undefined") {
    return 0;
  }
  const root = document.documentElement;
  const body = document.body;
  const height = Math.max(root?.scrollHeight ?? 0, body?.scrollHeight ?? 0);
  const viewport = window.innerHeight || root?.clientHeight || 0;
  if (height <= 0 || viewport <= 0) {
    return 0;
  }
  if (height <= viewport) {
    return 100;
  }
  const top = window.scrollY ?? window.pageYOffset ?? root?.scrollTop ?? 0;
  const pct = Math.round(((top + viewport) / height) * 100);
  return Math.min(100, Math.max(0, pct));
}

/** What installPageViews drives: one call per new page view. */
export interface EngagementWatch {
  /**
   * A new page view of `route` began (`entry` on the first of the page
   * load): the view before it is reported and this one starts.
   */
  view(route: string, entry: boolean): void;
  /** Remove every listener; nothing more is reported. */
  stop(): void;
}

interface PageView {
  route: string;
  // view_id: random per view, shared by its resumed reports.
  id: string;
  entry: boolean;
  clock: EngagementClock;
  scroll: number;
  // A report was sent for this view (the next one is `resumed`).
  emitted: boolean;
  // The view's current stretch has been reported; cleared when it resumes.
  reported: boolean;
  settle: ReturnType<typeof setTimeout> | null;
}

/**
 * A page view's id: 16 random hex characters (8 bytes from Web Crypto; Math
 * random only where there is none, which is fine for an id that only tells
 * two views apart). Exported for tests.
 */
export function newViewId(): string {
  const buf = new Uint8Array(8);
  const c = globalThis.crypto;
  if (c && typeof c.getRandomValues === "function") {
    c.getRandomValues(buf);
  } else {
    for (let i = 0; i < buf.length; i++) {
      buf[i] = Math.floor(Math.random() * 256);
    }
  }
  let out = "";
  for (const b of buf) {
    out += b.toString(16).padStart(2, "0");
  }
  return out;
}

function now(): number {
  return typeof performance !== "undefined" &&
    typeof performance.now === "function"
    ? performance.now()
    : Date.now();
}

function visible(): boolean {
  return (
    typeof document === "undefined" || document.visibilityState !== "hidden"
  );
}

function focused(): boolean {
  try {
    return typeof document === "undefined" ||
      typeof document.hasFocus !== "function"
      ? true
      : document.hasFocus();
  } catch {
    return true;
  }
}

/**
 * Watch the page's visibility, focus and input and report each page view's
 * engaged time (see the header). A no-op without a DOM. Called by
 * installPageViews; an app has no reason to call it.
 */
export function installPageEngagement(): EngagementWatch {
  if (typeof window === "undefined" || typeof document === "undefined") {
    return { view: () => {}, stop: () => {} };
  }
  let current: PageView | null = null;
  let active = true;

  const measure = (view: PageView) => {
    view.scroll = Math.max(view.scroll, documentScrollPercent());
  };

  const report = (view: PageView, t: number, flush: boolean) => {
    if (view.reported) {
      return;
    }
    view.reported = true;
    const ms = Math.min(MAX_ENGAGED_MS, Math.round(view.clock.elapsed(t)));
    if (ms < MIN_ENGAGED_MS) {
      return;
    }
    const props: Record<string, string | number> = {
      route: view.route,
      view_id: view.id,
      engaged_ms: ms,
      scroll_pct: view.scroll,
    };
    if (view.entry) {
      props.entry = "true";
    }
    if (view.emitted) {
      props.resumed = "true";
    }
    view.emitted = true;
    recordProductEvent(PAGE_ENGAGED_EVENT, props);
    if (flush) {
      flushProductEvents();
    }
  };

  // Every handler runs through here: telemetry must never break the app.
  const guard = (fn: (t: number) => void) => (): void => {
    if (!active) {
      return;
    }
    try {
      fn(now());
    } catch {
      // Telemetry must never break the app.
    }
  };

  // Hidden or unloading: the view's last chance to be reported.
  const onHide = guard((t) => {
    if (!current) {
      return;
    }
    current.clock.stop(t);
    measure(current);
    report(current, t, true);
  });

  // Shown again: a reported view resumes; the clock starts.
  const onShow = guard((t) => {
    if (!current || !visible()) {
      return;
    }
    current.reported = false;
    if (focused()) {
      current.clock.start(t);
    }
  });

  const onVisibility = () => (visible() ? onShow() : onHide());

  const onFocus = guard((t) => {
    if (current && visible() && !current.reported) {
      current.clock.start(t);
    }
  });

  const onBlur = guard((t) => {
    current?.clock.stop(t);
  });

  // Input means someone is there, focused or not, as long as it is visible.
  const onActivity = guard((t) => {
    if (current && visible() && !current.reported) {
      current.clock.start(t);
    }
  });

  const onScroll = (event: Event) => {
    if (!active || !current) {
      return;
    }
    try {
      const target = event.target;
      if (
        target === document ||
        target === document.documentElement ||
        target === document.body
      ) {
        measure(current);
      }
    } catch {
      // Telemetry must never break the app.
    }
    onActivity();
  };

  const stops: Array<() => void> = [];
  const listen = (
    target: Window | Document,
    type: string,
    fn: EventListener,
    options: AddEventListenerOptions,
  ) => {
    if (typeof target.addEventListener !== "function") {
      return;
    }
    target.addEventListener(type, fn, options);
    stops.push(() => target.removeEventListener?.(type, fn, options));
  };
  const capture = { capture: true, passive: true };
  listen(window, "visibilitychange", onVisibility, capture);
  listen(window, "pagehide", onHide, capture);
  listen(
    window,
    "pageshow",
    ((event: PageTransitionEvent) => {
      if (event?.persisted) {
        onShow();
      }
    }) as EventListener,
    capture,
  );
  // Not captured: an element's focus and blur do not bubble, so only the
  // window's own reach these.
  listen(window, "focus", onFocus, { passive: true });
  listen(window, "blur", onBlur, { passive: true });
  for (const type of ACTIVITY_EVENTS) {
    listen(window, type, onActivity, capture);
  }
  // Scroll does not bubble; captured at the document it sees every
  // scroller's, and measures the document's own.
  listen(document, "scroll", onScroll, capture);

  return {
    view(route: string, entry: boolean) {
      if (!active) {
        return;
      }
      try {
        const t = now();
        if (current) {
          if (current.settle !== null) {
            clearTimeout(current.settle);
          }
          // The DOM may already be the next route's: no measurement here.
          current.clock.stop(t);
          report(current, t, false);
        }
        const view: PageView = {
          route,
          id: newViewId(),
          entry,
          clock: new EngagementClock(),
          scroll: 0,
          emitted: false,
          reported: false,
          settle: null,
        };
        current = view;
        if (visible() && focused()) {
          view.clock.start(t);
        }
        view.settle = setTimeout(() => {
          view.settle = null;
          if (active && current === view) {
            try {
              measure(view);
            } catch {
              // Telemetry must never break the app.
            }
          }
        }, SCROLL_SETTLE_MS);
      } catch {
        // Telemetry must never break the app.
      }
    },
    stop() {
      active = false;
      if (current?.settle != null) {
        clearTimeout(current.settle);
      }
      current = null;
      for (const stop of stops) {
        try {
          stop();
        } catch {
          // Telemetry must never break the app.
        }
      }
    },
  };
}
