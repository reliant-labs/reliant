/**
 * Regression test: ESC in OnboardingSpotlight must NOT end the tour.
 *
 * Skipping has to be explicit (via the OnboardingNavBar's "Skip tour"
 * button). A stray ESC in the chat textarea or anywhere else in the app
 * should leave the tour running.
 */
import { describe, it, expect, vi } from "vitest";
import React from "react";
import { render, act } from "@testing-library/react";
import { OnboardingSpotlight } from "../OnboardingSpotlight";

describe("OnboardingSpotlight ESC handling", () => {
  it("does NOT call onSkipAll when Escape is pressed", () => {
    const onSkipAll = vi.fn();
    const onNext = vi.fn();
    const onBack = vi.fn();

    render(
      React.createElement(OnboardingSpotlight, {
        targetSelector: "body",
        title: "Test step",
        description: React.createElement("span", null, "description"),
        stepNumber: 1,
        totalSteps: 8,
        onNext,
        onBack,
        onSkipAll,
      })
    );

    act(() => {
      document.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true })
      );
    });

    expect(onSkipAll).not.toHaveBeenCalled();
    // ESC is also not a synonym for next or back.
    expect(onNext).not.toHaveBeenCalled();
    expect(onBack).not.toHaveBeenCalled();
  });

  it("still advances on Enter and ArrowRight (sanity check)", () => {
    const onSkipAll = vi.fn();
    const onNext = vi.fn();

    render(
      React.createElement(OnboardingSpotlight, {
        targetSelector: "body",
        title: "Test step",
        description: React.createElement("span", null, "description"),
        stepNumber: 1,
        totalSteps: 8,
        onNext,
        onSkipAll,
      })
    );

    act(() => {
      document.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true })
      );
    });

    expect(onNext).toHaveBeenCalledTimes(1);
    expect(onSkipAll).not.toHaveBeenCalled();
  });
});
