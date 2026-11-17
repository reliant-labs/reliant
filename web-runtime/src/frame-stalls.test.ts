// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Frame stalls from rAF gaps: recorded while loading and after input, never
// while hidden, held until the SDK runs, bounded.
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ACTIVE_WINDOW_MS,
  FRAME_STALL_MS,
  type FrameStall,
  LOAD_WINDOW_MS,
  MAX_FRAME_STALLS,
  watchFrameStalls,
} from "./frame-stalls.js";

type Listener = (e: unknown) => void;

// A page whose clock, frames, input and visibility the test drives.
function page() {
  let clock = 0;
  let queued: Array<(t: number) => void> = [];
  const listeners = new Map<string, Listener[]>();
  const docListeners = new Map<string, Listener[]>();
  const doc = {
    visibilityState: "visible" as DocumentVisibilityState,
    addEventListener: (type: string, fn: Listener) =>
      docListeners.set(type, [...(docListeners.get(type) ?? []), fn]),
    removeEventListener: (type: string, fn: Listener) =>
      docListeners.set(
        type,
        (docListeners.get(type) ?? []).filter((l) => l !== fn),
      ),
  };
  vi.stubGlobal("document", doc);
  vi.stubGlobal("performance", { now: () => clock });
  vi.stubGlobal("requestAnimationFrame", (fn: (t: number) => void) => {
    queued.push(fn);
    return queued.length;
  });
  vi.stubGlobal("window", {
    addEventListener: (type: string, fn: Listener) =>
      listeners.set(type, [...(listeners.get(type) ?? []), fn]),
    removeEventListener: (type: string, fn: Listener) =>
      listeners.set(
        type,
        (listeners.get(type) ?? []).filter((l) => l !== fn),
      ),
  });
  return {
    /** Advance the clock by `ms` and paint one frame. */
    frame(ms: number) {
      clock += ms;
      const run = queued;
      queued = [];
      for (const fn of run) fn(clock);
    },
    /** Paint frames every `ms` until `until`. */
    frames(ms: number, until: number) {
      while (clock + ms <= until) this.frame(ms);
    },
    pending: () => queued.length,
    input(type = "pointerdown") {
      for (const fn of listeners.get(type) ?? []) fn({});
    },
    setHidden(hidden: boolean) {
      doc.visibilityState = hidden ? "hidden" : "visible";
      for (const fn of docListeners.get("visibilitychange") ?? []) fn({});
    },
    listening: () =>
      [...listeners.values()].reduce((n, fns) => n + fns.length, 0),
    now: () => clock,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("watchFrameStalls", () => {
  it("records each frame gap of 50 ms or more while the page loads", () => {
    const p = page();
    const out: FrameStall[] = [];
    watchFrameStalls(() => true)!.attach((s) => out.push(s));
    p.frame(16);
    p.frame(16);
    p.frame(FRAME_STALL_MS - 1);
    p.frame(420);
    p.frame(16);
    expect(out).toEqual([{ start: 81, gap: 420, trigger: "load" }]);
  });

  it("stops after the load window, and watches again for a while after input", () => {
    const p = page();
    const out: FrameStall[] = [];
    watchFrameStalls(() => true)!.attach((s) => out.push(s));
    p.frames(16, LOAD_WINDOW_MS + 100);
    expect(p.pending()).toBe(0);
    p.frame(500);
    expect(out).toEqual([]);

    p.input("scroll");
    p.frame(16);
    p.frame(300);
    expect(out).toEqual([{ start: p.now() - 300, gap: 300, trigger: "input" }]);
    p.frames(16, p.now() + ACTIVE_WINDOW_MS + 100);
    expect(p.pending()).toBe(0);
  });

  it("measures nothing across a hidden period", () => {
    const p = page();
    const out: FrameStall[] = [];
    watchFrameStalls(() => true)!.attach((s) => out.push(s));
    p.frame(16);
    p.setHidden(true);
    p.frame(3_000);
    p.setHidden(false);
    p.frame(16);
    p.frame(16);
    expect(out).toEqual([]);
  });

  it("holds the stalls from before the SDK runs, at their own times", () => {
    const p = page();
    const watch = watchFrameStalls(() => true)!;
    p.frame(16);
    p.frame(700);
    const out: FrameStall[] = [];
    watch.attach((s) => out.push(s));
    expect(out).toEqual([{ start: 16, gap: 700, trigger: "load" }]);
  });

  it(`records at most ${MAX_FRAME_STALLS} per page`, () => {
    const p = page();
    const out: FrameStall[] = [];
    watchFrameStalls(() => true)!.attach((s) => out.push(s));
    p.frame(16);
    for (let i = 0; i < MAX_FRAME_STALLS + 10; i++) p.frame(60);
    expect(out).toHaveLength(MAX_FRAME_STALLS);
  });

  it("records nothing while disabled, and stops on stop()", () => {
    const p = page();
    let on = false;
    const out: FrameStall[] = [];
    const watch = watchFrameStalls(() => on)!;
    p.frame(16);
    p.frame(200);
    watch.attach((s) => out.push(s));
    p.frame(200);
    expect(out).toEqual([]);
    on = true;
    watch.stop();
    expect(p.listening()).toBe(0);
    p.frame(200);
    p.frame(200);
    expect(out).toEqual([]);
  });

  it("is null without rAF", () => {
    expect(watchFrameStalls(() => true)).toBeNull();
  });
});
