import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useDelayedVisibility } from "../useDelayedVisibility";

// A loading indicator that shows only for work that outlasts its delay, and
// never keeps finished content covered: it is "leaving" (still painted, to
// fade) from the render the work ends in.

const SHOW = 400;
const EXIT = 150;

function setup(active: boolean) {
  return renderHook(
    ({ active }) => useDelayedVisibility(active, { showDelayMs: SHOW, exitMs: EXIT }),
    { initialProps: { active } },
  );
}

const advance = (ms: number) => act(() => vi.advanceTimersByTime(ms));

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe("useDelayedVisibility", () => {
  it("stays hidden for work that ends before the delay", () => {
    const { result, rerender } = setup(true);
    advance(SHOW - 1);
    expect(result.current).toBe("hidden");

    rerender({ active: false });
    advance(1000);
    expect(result.current).toBe("hidden");
  });

  it("shows once the work outlasts the delay", () => {
    const { result } = setup(true);
    advance(SHOW);
    expect(result.current).toBe("shown");
  });

  it("is leaving in the very render the work ends, however briefly it was shown", () => {
    const { result, rerender } = setup(true);
    advance(SHOW);
    advance(1);

    rerender({ active: false });

    expect(result.current).toBe("leaving");
    advance(EXIT - 1);
    expect(result.current).toBe("leaving");
    advance(1);
    expect(result.current).toBe("hidden");
  });

  it("comes straight back when the work resumes while it leaves", () => {
    const { result, rerender } = setup(true);
    advance(SHOW);
    rerender({ active: false });
    advance(EXIT - 50);

    rerender({ active: true });

    expect(result.current).toBe("shown");
    advance(1000);
    expect(result.current).toBe("shown");
  });

  it("waits the full delay again once it has gone", () => {
    const { result, rerender } = setup(true);
    advance(SHOW);
    rerender({ active: false });
    advance(EXIT);

    rerender({ active: true });

    expect(result.current).toBe("hidden");
    advance(SHOW);
    expect(result.current).toBe("shown");
  });
});
