// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The errors a page raises before the SDK runs are held, bounded and in
// order, and handed over once.
import { afterEach, describe, expect, it, vi } from "vitest";

import { holdEarlyErrors, MAX_HELD_ERRORS } from "./early-errors.js";
import { FakeElement } from "./testdata/fake-dom.js";

type Listener = { fn: (e: unknown) => void; capture: unknown };

function stubWindow() {
  const listeners = new Map<string, Listener[]>();
  vi.stubGlobal("window", {
    addEventListener: (type: string, fn: Listener["fn"], capture: unknown) =>
      listeners.set(type, [...(listeners.get(type) ?? []), { fn, capture }]),
    removeEventListener: (type: string, fn: Listener["fn"]) =>
      listeners.set(
        type,
        (listeners.get(type) ?? []).filter((l) => l.fn !== fn),
      ),
  });
  return {
    listeners,
    dispatch(type: string, event: unknown) {
      for (const { fn } of [...(listeners.get(type) ?? [])]) fn(event);
    },
  };
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("holdEarlyErrors", () => {
  it("holds uncaught errors, unhandled rejections, failed resource loads and reported errors, in order, with when each happened", () => {
    vi.useFakeTimers({ now: 1_000 });
    const w = stubWindow();
    const early = holdEarlyErrors()!;
    // In the capture phase: a resource failure does not bubble.
    expect(w.listeners.get("error")![0]!.capture).toBe(true);

    const thrown = new TypeError("x is undefined");
    w.dispatch("error", { error: thrown, message: "Uncaught TypeError" });
    vi.setSystemTime(1_100);
    w.dispatch("error", { error: null, message: "Script error." });
    vi.setSystemTime(1_200);
    const img = new FakeElement("img");
    w.dispatch("error", { target: img });
    w.dispatch("unhandledrejection", { reason: "nope" });
    early.hold(thrown, { where: "boundary" });

    expect(early.take()).toEqual([
      { kind: "error", value: thrown, at: 1_000 },
      { kind: "error", value: "Script error.", at: 1_100 },
      { kind: "resource", target: img, at: 1_200 },
      { kind: "unhandledrejection", value: "nope", at: 1_200 },
      {
        kind: "reportException",
        value: thrown,
        attributes: { where: "boundary" },
        at: 1_200,
      },
    ]);
  });

  it("stops listening once taken, and hands nothing over twice", () => {
    const w = stubWindow();
    const early = holdEarlyErrors()!;
    w.dispatch("error", { error: new Error("a") });
    expect(early.take()).toHaveLength(1);
    expect(w.listeners.get("error")).toEqual([]);
    expect(w.listeners.get("unhandledrejection")).toEqual([]);
    w.dispatch("error", { error: new Error("b") });
    expect(early.take()).toEqual([]);
  });

  it(`holds at most ${MAX_HELD_ERRORS}`, () => {
    const w = stubWindow();
    const early = holdEarlyErrors()!;
    for (let i = 0; i < MAX_HELD_ERRORS + 5; i++) {
      w.dispatch("unhandledrejection", { reason: i });
    }
    const held = early.take();
    expect(held).toHaveLength(MAX_HELD_ERRORS);
    expect(held.at(-1)).toMatchObject({ value: MAX_HELD_ERRORS - 1 });
  });

  it("drops what it held on stop()", () => {
    const w = stubWindow();
    const early = holdEarlyErrors()!;
    w.dispatch("error", { error: new Error("a") });
    early.stop();
    expect(w.listeners.get("error")).toEqual([]);
    expect(early.take()).toEqual([]);
  });

  it("ignores an error event that names nothing", () => {
    const w = stubWindow();
    const early = holdEarlyErrors()!;
    w.dispatch("error", { message: "", target: null });
    expect(early.take()).toEqual([]);
  });

  it("is null without a DOM", () => {
    expect(holdEarlyErrors()).toBeNull();
  });
});
