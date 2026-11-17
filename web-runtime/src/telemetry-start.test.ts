// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// When the SDK starts: after load, then idle or the first interaction's paint,
// or at once when asked; and how startup yields.
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  START_IDLE_TIMEOUT_MS,
  START_LOAD_TIMEOUT_MS,
  type StartReason,
  startGate,
  yieldToMain,
} from "./telemetry-start.js";

type Listener = (e: unknown) => void;

// A window and document whose events, idle callbacks and frames the test
// drives by hand.
function browser(readyState: DocumentReadyState, idle = true) {
  const listeners = new Map<string, Listener[]>();
  const idleCallbacks: Array<{ fn: () => void; timeout?: number }> = [];
  const frames: Array<() => void> = [];
  const win: Record<string, unknown> = {
    addEventListener: (type: string, fn: Listener) =>
      listeners.set(type, [...(listeners.get(type) ?? []), fn]),
    removeEventListener: (type: string, fn: Listener) =>
      listeners.set(
        type,
        (listeners.get(type) ?? []).filter((l) => l !== fn),
      ),
  };
  if (idle) {
    win.requestIdleCallback = (fn: () => void, o?: { timeout: number }) =>
      idleCallbacks.push({ fn, timeout: o?.timeout });
    win.cancelIdleCallback = () => {};
  }
  vi.stubGlobal("window", win);
  vi.stubGlobal("document", { readyState });
  vi.stubGlobal("requestAnimationFrame", (fn: () => void) => frames.push(fn));
  return {
    dispatch(type: string) {
      for (const fn of [...(listeners.get(type) ?? [])]) fn({});
    },
    listening: () =>
      [...listeners.values()].reduce((n, fns) => n + fns.length, 0),
    idleCallbacks,
    runIdle() {
      for (const { fn } of idleCallbacks.splice(0)) fn();
    },
    paint() {
      for (const fn of frames.splice(0)) fn();
    },
  };
}

function track(ready: Promise<StartReason>) {
  const seen: { reason?: StartReason } = {};
  void ready.then((r) => (seen.reason = r));
  return seen;
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("startGate", () => {
  it("starts at once in eager mode, and without a DOM", async () => {
    browser("loading");
    await expect(startGate("eager").ready).resolves.toBe("eager");
    vi.unstubAllGlobals();
    await expect(startGate(undefined).ready).resolves.toBe("eager");
  });

  it("waits for load, then for the browser's first idle period", async () => {
    vi.useFakeTimers();
    const b = browser("loading");
    const seen = track(startGate("idle").ready);

    await vi.advanceTimersByTimeAsync(5_000);
    expect(b.idleCallbacks).toHaveLength(0);
    expect(seen.reason).toBeUndefined();

    b.dispatch("load");
    expect(b.idleCallbacks).toEqual([
      { fn: expect.any(Function), timeout: START_IDLE_TIMEOUT_MS },
    ]);
    await vi.advanceTimersByTimeAsync(0);
    expect(seen.reason).toBeUndefined();

    b.runIdle();
    await vi.advanceTimersByTimeAsync(0);
    expect(seen.reason).toBe("idle");
    expect(b.listening()).toBe(0);
  });

  it("asks for idle time at once on a page that has already loaded", () => {
    const b = browser("complete");
    startGate(undefined);
    expect(b.idleCallbacks).toHaveLength(1);
  });

  it("starts after the first interaction's frame has painted, once loaded, without waiting for idle", async () => {
    vi.useFakeTimers();
    const b = browser("complete");
    const seen = track(startGate("idle").ready);

    b.dispatch("pointerdown");
    await vi.advanceTimersByTimeAsync(0);
    expect(seen.reason).toBeUndefined();
    b.paint();
    await vi.advanceTimersByTimeAsync(0);
    expect(seen.reason).toBe("interaction");
  });

  it("does not start on an interaction before load; starts after load's next paint instead of waiting for idle", async () => {
    vi.useFakeTimers();
    const b = browser("interactive");
    const seen = track(startGate("idle").ready);

    b.dispatch("keydown");
    b.paint();
    await vi.advanceTimersByTimeAsync(100);
    expect(seen.reason).toBeUndefined();

    b.dispatch("load");
    expect(b.idleCallbacks).toHaveLength(0);
    b.paint();
    await vi.advanceTimersByTimeAsync(0);
    expect(seen.reason).toBe("interaction");
  });

  it("stops waiting for load after START_LOAD_TIMEOUT_MS", async () => {
    vi.useFakeTimers();
    const b = browser("loading");
    startGate("idle");
    await vi.advanceTimersByTimeAsync(START_LOAD_TIMEOUT_MS - 1);
    expect(b.idleCallbacks).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(b.idleCallbacks).toHaveLength(1);
  });

  it("starts at once when asked, whatever the page is doing", async () => {
    const b = browser("loading");
    const gate = startGate("idle");
    gate.startNow();
    await expect(gate.ready).resolves.toBe("now");
    expect(b.listening()).toBe(0);
  });

  it("without idle callbacks (Safari), starts at the first quiet moment a timer probe finds", async () => {
    vi.useFakeTimers();
    browser("complete", false);
    const seen = track(startGate("idle").ready);
    await vi.advanceTimersByTimeAsync(50);
    expect(seen.reason).toBeUndefined();
    await vi.advanceTimersByTimeAsync(50);
    expect(seen.reason).toBe("idle");
  });

  it("never starts once cancelled, and leaves no listener behind", async () => {
    vi.useFakeTimers();
    const b = browser("loading");
    const gate = startGate("idle");
    const seen = track(gate.ready);
    gate.cancel();
    expect(b.listening()).toBe(0);
    gate.startNow();
    await vi.advanceTimersByTimeAsync(START_LOAD_TIMEOUT_MS * 2);
    expect(seen.reason).toBeUndefined();
  });
});

describe("yieldToMain", () => {
  it("uses scheduler.yield where the browser has it", async () => {
    const y = vi.fn(async () => {});
    vi.stubGlobal("scheduler", { yield: y });
    await yieldToMain();
    expect(y).toHaveBeenCalledTimes(1);
  });

  it("falls back to a new task from a zero-delay timer", async () => {
    vi.useFakeTimers();
    let done = false;
    void yieldToMain().then(() => (done = true));
    await Promise.resolve();
    expect(done).toBe(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(done).toBe(true);
  });
});
