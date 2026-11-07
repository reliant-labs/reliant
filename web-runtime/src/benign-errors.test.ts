// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// What counts as benign browser noise, and how the filtered errors are counted.
// The wiring into the SDK's inputs is pinned in telemetry.test.ts.
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  classifyBenignError,
  FILTERED_ERRORS_ACTION,
  FilteredErrorCounter,
} from "./benign-errors.js";

function named(name: string, message = ""): Error {
  const err = new Error(message);
  err.name = name;
  return err;
}

describe("classifyBenignError", () => {
  it.each([
    [
      "a DOMException AbortError",
      new DOMException("signal is aborted without reason", "AbortError"),
      "abort",
    ],
    [
      "an Error named AbortError",
      named("AbortError", "The operation was aborted."),
      "abort",
    ],
    ["Chromium's fetch failure", new TypeError("Failed to fetch"), "network"],
    [
      "Chromium's fetch failure naming the host",
      new TypeError("Failed to fetch (dash.example.io)"),
      "network",
    ],
    ["Safari's", new TypeError("Load failed"), "network"],
    [
      "Firefox's",
      new TypeError("NetworkError when attempting to fetch resource."),
      "network",
    ],
  ])("filters %s", (_, error, kind) => {
    expect(classifyBenignError(error)).toBe(kind);
  });

  it.each([
    [
      "a ConnectError whose message is a fetch failure",
      named("ConnectError", "[unavailable] Failed to fetch"),
    ],
    [
      "a ConnectError for a cancelled call",
      named("ConnectError", "[canceled] signal is aborted without reason"),
    ],
    [
      "the runtime's ConnectClientError",
      named("ConnectClientError", "Failed to fetch"),
    ],
    [
      "a TypeError that is a bug",
      new TypeError("Cannot read properties of undefined (reading 'id')"),
    ],
    [
      "a TypeError that only mentions a fetch failure",
      new TypeError("Failed to fetch workflow definition: not found"),
    ],
    ["a plain Error saying Failed to fetch", new Error("Failed to fetch")],
    ["a string", "Failed to fetch"],
    ["undefined", undefined],
    ["null", null],
  ])("keeps %s", (_, error) => {
    expect(classifyBenignError(error)).toBeNull();
  });
});

describe("FilteredErrorCounter", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("emits one action per kind and source with its count, then starts again", () => {
    const emit = vi.fn();
    const counter = new FilteredErrorCounter(emit);
    counter.note("network", "console.error");
    counter.note("network", "console.error");
    counter.note("abort", "unhandledrejection");
    counter.flush();

    expect(emit).toHaveBeenCalledTimes(2);
    expect(emit).toHaveBeenCalledWith(FILTERED_ERRORS_ACTION, {
      kind: "network",
      source: "console.error",
      count: 2,
    });
    expect(emit).toHaveBeenCalledWith(FILTERED_ERRORS_ACTION, {
      kind: "abort",
      source: "unhandledrejection",
      count: 1,
    });

    emit.mockClear();
    counter.flush();
    expect(emit).not.toHaveBeenCalled();
  });

  it("flushes on its own once the interval passes, and not before", () => {
    vi.useFakeTimers();
    const emit = vi.fn();
    const counter = new FilteredErrorCounter(emit, 60_000);
    counter.note("abort", "unhandledrejection");

    vi.advanceTimersByTime(59_999);
    expect(emit).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(emit).toHaveBeenCalledTimes(1);
  });

  it("never throws when emitting does", () => {
    const counter = new FilteredErrorCounter(() => {
      throw new Error("sdk gone");
    });
    counter.note("abort", "reportException");
    expect(() => counter.flush()).not.toThrow();
  });
});
