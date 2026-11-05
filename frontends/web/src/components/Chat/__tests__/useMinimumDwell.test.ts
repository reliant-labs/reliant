import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useMinimumDwell } from "../useMinimumDwell";

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

describe("useMinimumDwell", () => {
  it("holds each value for the minimum, then jumps to the newest", () => {
    const { result, rerender } = renderHook(({ v }) => useMinimumDwell(v, 1000), {
      initialProps: { v: "a" },
    });
    expect(result.current).toBe("a");

    rerender({ v: "b" });
    rerender({ v: "c" });
    expect(result.current).toBe("a");

    advance(999);
    expect(result.current).toBe("a");
    advance(1);
    // "b" arrived and was superseded during the hold: never shown.
    expect(result.current).toBe("c");
  });

  it("shows a change at once when the current value has already been up long enough", () => {
    const { result, rerender } = renderHook(({ v }) => useMinimumDwell(v, 1000), {
      initialProps: { v: "a" },
    });
    advance(5000);
    rerender({ v: "b" });
    expect(result.current).toBe("b");
  });

  it("restarts the hold from when the new value was shown", () => {
    const { result, rerender } = renderHook(({ v }) => useMinimumDwell(v, 1000), {
      initialProps: { v: "a" },
    });
    advance(5000);
    rerender({ v: "b" });
    rerender({ v: "c" });
    expect(result.current).toBe("b");
    advance(1000);
    expect(result.current).toBe("c");
  });
});
