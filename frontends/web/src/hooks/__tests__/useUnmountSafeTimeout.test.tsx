/**
 * useUnmountSafeTimeout — a scheduled timer must not outlive its component.
 *
 * THE BUG THIS CLOSES: OnboardingSpotlight, MobileAIProvidersPanel and
 * QueuedDeployBanner each scheduled a bare `setTimeout(() => setState(...))`
 * that nothing cleared. When the test file finished first, the timer fired
 * after vitest had torn down jsdom; React read `window` to pick an update lane
 * and the run died on an uncaught `ReferenceError: window is not defined`,
 * failing the web lane with every test green.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { useUnmountSafeTimeout } from "../useUnmountSafeTimeout";

describe("useUnmountSafeTimeout", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("runs the callback after the delay while mounted", () => {
    const fn = vi.fn();
    const { result } = renderHook(() => useUnmountSafeTimeout());

    result.current(fn, 50);
    vi.advanceTimersByTime(49);
    expect(fn).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("clears every pending timer on unmount", () => {
    const fn = vi.fn();
    const { result, unmount } = renderHook(() => useUnmountSafeTimeout());

    result.current(fn, 50);
    result.current(fn, 800);
    expect(vi.getTimerCount()).toBe(2);

    unmount();
    expect(vi.getTimerCount()).toBe(0);
    vi.runAllTimers();
    expect(fn).not.toHaveBeenCalled();
  });

  it("schedules nothing when called after unmount (a late promise continuation)", () => {
    const fn = vi.fn();
    const { result, unmount } = renderHook(() => useUnmountSafeTimeout());
    const schedule = result.current;

    unmount();
    schedule(fn, 1500);

    expect(vi.getTimerCount()).toBe(0);
    vi.runAllTimers();
    expect(fn).not.toHaveBeenCalled();
  });

  it("returns a stable function across renders", () => {
    const { result, rerender } = renderHook(() => useUnmountSafeTimeout());
    const first = result.current;
    rerender();
    expect(result.current).toBe(first);
  });
});
