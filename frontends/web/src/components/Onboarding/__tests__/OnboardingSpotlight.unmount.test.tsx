/**
 * Regression test: OnboardingSpotlight leaves no timer behind when it unmounts.
 *
 * Its highlight fade-in was a bare `setTimeout(..., 50)` scheduled from
 * updatePosition, which nothing cleared. A spotlight unmounted inside that
 * window (a step change, or a test file ending) fired a state update into a
 * dead tree; under vitest it fired after jsdom teardown and failed the whole
 * run with an uncaught `ReferenceError: window is not defined`.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import React from "react";
import { render } from "@testing-library/react";
import { OnboardingSpotlight } from "../OnboardingSpotlight";

function renderSpotlight(targetSelector: string) {
  return render(
    React.createElement(OnboardingSpotlight, {
      targetSelector,
      title: "Test step",
      description: React.createElement("span", null, "description"),
      stepNumber: 1,
      totalSteps: 8,
      onNext: vi.fn(),
      onSkipAll: vi.fn(),
    })
  );
}

describe("OnboardingSpotlight unmount", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("clears the highlight and settle timers when unmounted with the target found", () => {
    const { unmount } = renderSpotlight("body");
    // Mounting schedules the 50ms highlight fade-in and the 300ms settle pass.
    expect(vi.getTimerCount()).toBeGreaterThan(0);

    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("clears the highlight timers scheduled by resize repositioning", () => {
    const { unmount } = renderSpotlight("body");
    window.dispatchEvent(new Event("resize"));
    window.dispatchEvent(new Event("resize"));

    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("clears the retry interval when unmounted before the target appears", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const { unmount } = renderSpotlight("[data-tour='not-there-yet']");

    unmount();
    expect(vi.getTimerCount()).toBe(0);
    warn.mockRestore();
  });
});
