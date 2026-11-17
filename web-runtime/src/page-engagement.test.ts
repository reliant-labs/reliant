// The contract of page_engaged: what time counts (visible, focused, active,
// a 30 s idle cutoff, a 30 min cap), when a view is reported (once, at the
// first of a route change, a hide or an unload; a view the reader returns to
// is reported again as `resumed`, cumulatively), how deep the reader
// scrolled, and that it leaves nothing behind. Times are fake; every
// expected number is worked out by hand in the test that asserts it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  ENGAGEMENT_IDLE_MS,
  EngagementClock,
  installPageEngagement,
  MAX_ENGAGED_MS,
  PAGE_ENGAGED_EVENT,
  SCROLL_SETTLE_MS,
} from "./page-engagement.js";
import { installPageViews, PAGE_VIEWED_EVENT } from "./page-views.js";
import {
  disableTelemetry,
  optOutOfAnalytics,
  resetTrackState,
  setAnalyticsMode,
  setTelemetrySink,
  type TelemetrySink,
} from "./track.js";

function sink() {
  return {
    track: vi.fn<TelemetrySink["track"]>(),
    setUser: vi.fn<TelemetrySink["setUser"]>(),
    disable: vi.fn<TelemetrySink["disable"]>(),
    flush: vi.fn<NonNullable<TelemetrySink["flush"]>>(),
  };
}

type Listener = (event?: unknown) => void;

function listenerMap() {
  const map = new Map<string, Listener[]>();
  return {
    map,
    add: (type: string, fn: Listener) =>
      map.set(type, [...(map.get(type) ?? []), fn]),
    remove: (type: string, fn: Listener) =>
      map.set(
        type,
        (map.get(type) ?? []).filter((l) => l !== fn),
      ),
    fire: (type: string, event?: unknown) => {
      for (const fn of [...(map.get(type) ?? [])]) fn(event);
    },
    count: () => [...map.values()].reduce((n, l) => n + l.length, 0),
  };
}

// A page: a 3000px document in a 1000px viewport, visible and focused, with
// a history that moves location.pathname the way a browser does.
function stubPage(initial = "/blog/hello") {
  const win = listenerMap();
  const doc = listenerMap();
  const state = { focus: true };
  const root = { scrollHeight: 3000, clientHeight: 1000, scrollTop: 0 };
  const document = {
    visibilityState: "visible" as "visible" | "hidden",
    hasFocus: () => state.focus,
    documentElement: root,
    body: { scrollHeight: 3000 },
    referrer: "",
    addEventListener: doc.add,
    removeEventListener: doc.remove,
  };
  const location = { pathname: initial, search: "", host: "app.test" };
  const go = (_s: unknown, _u: string, url?: string | URL | null) => {
    if (url !== undefined && url !== null) {
      location.pathname = new URL(String(url), "http://app.test").pathname;
    }
  };
  const window = {
    innerHeight: 1000,
    scrollY: 0,
    location,
    history: { pushState: go, replaceState: go } as unknown as History,
    addEventListener: win.add,
    removeEventListener: win.remove,
  };
  vi.stubGlobal("window", window);
  vi.stubGlobal("document", document);
  return {
    window,
    document,
    win,
    doc,
    hide() {
      document.visibilityState = "hidden";
      win.fire("visibilitychange");
    },
    show() {
      document.visibilityState = "visible";
      win.fire("visibilitychange");
    },
    blur() {
      state.focus = false;
      win.fire("blur");
    },
    focus() {
      state.focus = true;
      win.fire("focus");
    },
    input(type = "pointermove") {
      win.fire(type);
    },
    scrollTo(y: number) {
      window.scrollY = y;
      doc.fire("scroll", { target: document });
    },
    pagehide() {
      win.fire("pagehide");
    },
    pageshow(persisted: boolean) {
      win.fire("pageshow", { persisted });
    },
  };
}

// The page_engaged reports, without view_id (random; its own tests pin it).
function engaged(s: ReturnType<typeof sink>) {
  return engagedWithIds(s).map(({ view_id: _id, ...rest }) => rest);
}

function engagedWithIds(s: ReturnType<typeof sink>) {
  return s.track.mock.calls
    .filter(([name]) => name === PAGE_ENGAGED_EVENT)
    .map(([, attrs]) => attrs as Record<string, unknown>);
}

// Advance time with input every `every` ms, so the reader never idles.
function readFor(page: ReturnType<typeof stubPage>, ms: number, every = 5000) {
  let left = ms;
  while (left > 0) {
    const step = Math.min(every, left);
    vi.advanceTimersByTime(step);
    page.input();
    left -= step;
  }
}

let s: ReturnType<typeof sink>;

beforeEach(() => {
  vi.useFakeTimers();
  resetTrackState();
  s = sink();
  setTelemetrySink(s);
});

afterEach(() => {
  resetTrackState();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("EngagementClock", () => {
  it("counts from start to stop while there is input", () => {
    const c = new EngagementClock();
    c.start(0);
    c.activity(10_000);
    c.activity(20_000);
    c.stop(25_000);
    expect(c.elapsed(99_000)).toBe(25_000);
  });

  it("stops counting 30 s after the last input, and restarts at the next", () => {
    const c = new EngagementClock();
    c.start(0);
    c.activity(10_000);
    // Last input at 10 s: counted to 10 + 30 = 40 s, not to 100 s.
    expect(c.elapsed(100_000)).toBe(40_000);
    // Input at 100 s restarts it: 40 + (110 - 100) = 50 s.
    c.activity(100_000);
    expect(c.elapsed(110_000)).toBe(50_000);
    c.stop(110_000);
    expect(c.elapsed(500_000)).toBe(50_000);
  });

  it("ignores input while stopped, and adds stretches across stop/start", () => {
    const c = new EngagementClock();
    c.start(0);
    c.stop(5_000);
    c.activity(6_000);
    c.start(60_000);
    c.stop(62_000);
    // 5 s + 2 s.
    expect(c.elapsed(70_000)).toBe(7_000);
    expect(c.counting).toBe(false);
  });

  it("counts a gap of exactly the cutoff in full", () => {
    const c = new EngagementClock();
    c.start(0);
    c.activity(ENGAGEMENT_IDLE_MS);
    expect(c.elapsed(ENGAGEMENT_IDLE_MS + 1_000)).toBe(
      ENGAGEMENT_IDLE_MS + 1_000,
    );
  });
});

describe("installPageEngagement", () => {
  it("reports visible, focused, active time once when the tab is hidden, and flushes", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    readFor(page, 12_000);
    page.hide();
    expect(engaged(s)).toEqual([
      {
        route: "/blog/hello",
        engaged_ms: 12_000,
        scroll_pct: 33,
        entry: "true",
      },
    ]);
    expect(s.flush).toHaveBeenCalledTimes(1);
    // The unload that follows reports nothing more.
    page.pagehide();
    expect(engaged(s)).toHaveLength(1);
    watch.stop();
  });

  it("stops at the idle cutoff: 5 s of input then 2 min of nothing is 35 s", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    vi.advanceTimersByTime(5_000);
    page.input("keydown");
    vi.advanceTimersByTime(120_000);
    page.pagehide();
    expect(engaged(s)[0]?.engaged_ms).toBe(5_000 + ENGAGEMENT_IDLE_MS);
    watch.stop();
  });

  it("does not count time without focus, and reports the view on a route change without flushing", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    readFor(page, 4_000);
    page.blur();
    vi.advanceTimersByTime(20_000); // another window: not counted
    page.focus();
    readFor(page, 6_000);
    watch.view("/pricing", false);
    // 4 s + 6 s; the route change is no unload, so no flush.
    expect(engaged(s)).toEqual([
      {
        route: "/blog/hello",
        engaged_ms: 10_000,
        scroll_pct: 33,
        entry: "true",
      },
    ]);
    expect(s.flush).not.toHaveBeenCalled();
    watch.stop();
  });

  it("counts input over an unfocused but visible window (a wheel over a background window)", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    page.blur();
    vi.advanceTimersByTime(10_000); // not counted
    page.input("wheel"); // counting from here
    vi.advanceTimersByTime(3_000);
    page.input("wheel");
    vi.advanceTimersByTime(2_000);
    page.hide();
    // 3 s + 2 s after the first wheel.
    expect(engaged(s)[0]?.engaged_ms).toBe(5_000);
    watch.stop();
  });

  it("reports a view the reader returns to again, resumed and cumulative", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    readFor(page, 10_000);
    page.hide();
    vi.advanceTimersByTime(300_000); // in another tab: not counted
    page.show();
    readFor(page, 4_000);
    page.scrollTo(2000); // (2000 + 1000) / 3000 = 100%
    page.pagehide();
    expect(engaged(s)).toEqual([
      {
        route: "/blog/hello",
        engaged_ms: 10_000,
        scroll_pct: 33,
        entry: "true",
      },
      {
        route: "/blog/hello",
        engaged_ms: 14_000,
        scroll_pct: 100,
        entry: "true",
        resumed: "true",
      },
    ]);
    expect(s.flush).toHaveBeenCalledTimes(2);
    watch.stop();
  });

  it("names each view with its own view_id, kept by its resumed reports", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    readFor(page, 2_000);
    page.hide(); // first view, first report
    page.show();
    readFor(page, 1_000);
    watch.view("/pricing", false); // first view, resumed report
    readFor(page, 2_000);
    watch.view("/blog/hello", false); // the same route again: a new view
    readFor(page, 2_000);
    page.pagehide();
    const reports = engagedWithIds(s);
    expect(reports.map((r) => [r.route, r.resumed])).toEqual([
      ["/blog/hello", undefined],
      ["/blog/hello", "true"],
      ["/pricing", undefined],
      ["/blog/hello", undefined],
    ]);
    const ids = reports.map((r) => r.view_id);
    for (const id of ids) {
      expect(id).toMatch(/^[0-9a-f]{16}$/);
    }
    expect(ids[1]).toBe(ids[0]);
    expect(new Set([ids[0], ids[2], ids[3]]).size).toBe(3);
    watch.stop();
  });

  it("keeps a resumed view cumulative across an away stretch longer than the idle cutoff", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    readFor(page, 8_000);
    page.hide();
    vi.advanceTimersByTime(ENGAGEMENT_IDLE_MS + 60_000); // away, past the cutoff
    page.show();
    readFor(page, 5_000);
    page.hide();
    vi.advanceTimersByTime(ENGAGEMENT_IDLE_MS * 3);
    page.show();
    readFor(page, 2_000);
    page.pagehide();
    // Each report is the view's total so far: 8 s, 8 + 5 s, 8 + 5 + 2 s.
    expect(engaged(s).map((e) => [e.engaged_ms, e.resumed])).toEqual([
      [8_000, undefined],
      [13_000, "true"],
      [15_000, "true"],
    ]);
    watch.stop();
  });

  it("resumes a view restored from the back/forward cache", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    readFor(page, 2_000);
    page.pagehide();
    page.pageshow(true);
    readFor(page, 1_000);
    page.hide();
    expect(engaged(s).map((e) => [e.engaged_ms, e.resumed])).toEqual([
      [2_000, undefined],
      [3_000, "true"],
    ]);
    watch.stop();
  });

  it("caps a view at 30 minutes", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    readFor(page, 40 * 60_000, 10_000);
    page.hide();
    expect(engaged(s)[0]?.engaged_ms).toBe(MAX_ENGAGED_MS);
    watch.stop();
  });

  it("does not report a view shorter than half a second (a redirect)", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/", true);
    vi.advanceTimersByTime(200);
    watch.view("/login", false);
    readFor(page, 2_000);
    page.hide();
    expect(engaged(s).map((e) => [e.route, e.engaged_ms, e.entry])).toEqual([
      ["/login", 2_000, undefined],
    ]);
    watch.stop();
  });

  it("does not mark a later report resumed when the first was too short to send", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    vi.advanceTimersByTime(100);
    page.hide(); // 100 ms: not sent
    page.show();
    readFor(page, 3_000);
    page.hide();
    expect(engaged(s)).toEqual([
      {
        route: "/blog/hello",
        engaged_ms: 3_100,
        scroll_pct: 33,
        entry: "true",
      },
    ]);
    watch.stop();
  });

  it("starts nothing for a view that begins hidden, until the tab is shown", () => {
    const page = stubPage();
    page.document.visibilityState = "hidden";
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    vi.advanceTimersByTime(60_000);
    page.show();
    readFor(page, 5_000);
    page.pagehide();
    expect(engaged(s)[0]?.engaged_ms).toBe(5_000);
    watch.stop();
  });

  it("keeps the deepest scroll of the view, measured once the route has rendered and on each document scroll", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    vi.advanceTimersByTime(SCROLL_SETTLE_MS); // 1000 / 3000 = 33%
    page.scrollTo(1500); // 2500 / 3000 = 83%
    page.scrollTo(0); // back up: the deepest stays
    page.hide();
    expect(engaged(s)[0]?.scroll_pct).toBe(83);
    watch.stop();
  });

  it("reports a page that fits the viewport as fully read", () => {
    const page = stubPage();
    page.document.documentElement.scrollHeight = 800;
    page.document.body.scrollHeight = 800;
    const watch = installPageEngagement();
    watch.view("/about", true);
    readFor(page, 1_000);
    page.hide();
    expect(engaged(s)[0]?.scroll_pct).toBe(100);
    watch.stop();
  });

  it("counts an inner scroller's scroll as input but not as the document's depth", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    page.blur();
    page.doc.fire("scroll", { target: { scrollTop: 900 } });
    vi.advanceTimersByTime(2_000);
    page.hide();
    expect(engaged(s)[0]).toMatchObject({ engaged_ms: 2_000, scroll_pct: 33 });
    watch.stop();
  });

  it("does not measure the next route's DOM into the view that ended on a route change", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    readFor(page, 2_000);
    page.window.scrollY = 2000; // the router has already rendered and scrolled
    watch.view("/blog/next", false);
    expect(engaged(s)[0]?.scroll_pct).toBe(33);
    watch.stop();
  });

  it("is sent in the aggregate tier and not in off", () => {
    const page = stubPage();
    setAnalyticsMode("aggregate");
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    readFor(page, 1_000);
    page.hide();
    expect(engaged(s)).toHaveLength(1);
    page.show();
    optOutOfAnalytics();
    readFor(page, 1_000);
    page.hide();
    expect(engaged(s)).toHaveLength(1);
    watch.stop();
  });

  it("removes every listener on stop, and reports nothing after it", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    expect(page.win.count() + page.doc.count()).toBeGreaterThan(0);
    watch.view("/blog/hello", true);
    readFor(page, 3_000);
    watch.stop();
    expect(page.win.count() + page.doc.count()).toBe(0);
    page.hide();
    watch.view("/x", false);
    expect(engaged(s)).toEqual([]);
  });

  it("sends nothing after disableTelemetry()", () => {
    const page = stubPage();
    const watch = installPageEngagement();
    watch.view("/blog/hello", true);
    readFor(page, 3_000);
    disableTelemetry();
    page.hide();
    expect(engaged(s)).toEqual([]);
    expect(s.flush).not.toHaveBeenCalled();
    watch.stop();
  });

  it("never throws into the app when the DOM misbehaves", () => {
    const page = stubPage();
    page.document.hasFocus = () => {
      throw new Error("detached");
    };
    const watch = installPageEngagement();
    expect(() => watch.view("/blog/hello", true)).not.toThrow();
    expect(() => page.hide()).not.toThrow();
    watch.stop();
  });

  it("is a no-op without a DOM", () => {
    const watch = installPageEngagement();
    expect(() => watch.view("/", true)).not.toThrow();
    expect(() => watch.stop()).not.toThrow();
  });
});

describe("through installPageViews", () => {
  it("ends each SPA view with page_engaged when engagement is on", () => {
    const page = stubPage("/blog/hello");
    const stop = installPageViews({ pageViews: true, engagement: true });
    readFor(page, 7_000);
    page.window.history.pushState({}, "", "/blog/next");
    readFor(page, 3_000);
    page.hide();
    const names = s.track.mock.calls.map(([name, attrs]) => [
      name,
      attrs.route,
    ]);
    expect(names).toEqual([
      [PAGE_VIEWED_EVENT, "/blog/hello"],
      [PAGE_VIEWED_EVENT, "/blog/next"],
      [PAGE_ENGAGED_EVENT, "/blog/hello"],
      [PAGE_ENGAGED_EVENT, "/blog/next"],
    ]);
    expect(engaged(s).map((e) => [e.engaged_ms, e.entry])).toEqual([
      [7_000, "true"],
      [3_000, undefined],
    ]);
    stop();
    expect(page.win.count() + page.doc.count()).toBe(0);
  });

  it("records no engagement without the option, or for a watch kept only for the session identity", () => {
    const page = stubPage("/blog/hello");
    const stopA = installPageViews({ pageViews: true });
    readFor(page, 2_000);
    page.hide();
    stopA();
    const stopB = installPageViews({ pageViews: false, engagement: true });
    page.show();
    readFor(page, 2_000);
    page.hide();
    stopB();
    expect(engaged(s)).toEqual([]);
  });
});
