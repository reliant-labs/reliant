/**
 * A phone's callout sheet and the reader's own scrolling.
 *
 * On arrival the sheet scrolls its target into the room above it. Once the
 * reader drags the page, it never scrolls for them again, and the dim and
 * ring step aside: they follow the target from the main thread, so on a phone
 * (where touch scrolling runs off it) they trailed every swipe.
 */
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { OnboardingSpotlight } from "../OnboardingSpotlight";

const width = window.innerWidth;
let target: HTMLElement;
let scrollIntoView: Mock<Element["scrollIntoView"]>;

function renderSheet(autoScroll?: boolean) {
  return render(
    <OnboardingSpotlight
      targetSelector="[data-testid='sheet-target']"
      title="Releases"
      description="What shipped"
      stepNumber={1}
      totalSteps={1}
      onNext={vi.fn()}
      onSkipAll={vi.fn()}
      keyboardNav={false}
      callout={{ footer: null, autoScroll }}
    />
  );
}

const dim = () => screen.getByTestId("onboarding-spotlight-dim");

beforeEach(() => {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 390 });
  target = document.createElement("div");
  target.setAttribute("data-testid", "sheet-target");
  scrollIntoView = vi.fn<Element["scrollIntoView"]>();
  target.scrollIntoView = scrollIntoView;
  document.body.appendChild(target);
});

afterEach(() => {
  cleanup();
  target.remove();
  Object.defineProperty(window, "innerWidth", { configurable: true, value: width });
  vi.restoreAllMocks();
});

describe("OnboardingSpotlight sheet and the reader's scroll", () => {
  it("scrolls the target above the sheet on arrival, with the dim up", () => {
    renderSheet();
    expect(screen.getByTestId("tour-callout").getAttribute("data-side")).toBe("sheet");
    expect(scrollIntoView).toHaveBeenCalled();
    expect(dim().getAttribute("data-stepped-aside")).toBeNull();
  });

  it("steps the dim aside on the reader's first drag, and stops tracking the target", () => {
    renderSheet();
    act(() => {
      window.dispatchEvent(new Event("touchmove"));
    });
    expect(dim().getAttribute("data-stepped-aside")).toBe("true");
    // The sheet stays: only the target-tracking layers go.
    expect(screen.getByTestId("tour-callout").getAttribute("data-side")).toBe("sheet");

    const measure = vi.spyOn(target, "getBoundingClientRect");
    act(() => {
      window.dispatchEvent(new Event("scroll"));
      window.dispatchEvent(new Event("resize"));
    });
    expect(measure).not.toHaveBeenCalled();
  });

  it("never scrolls the page when the reader scrolled before the target turned up", () => {
    renderSheet(false);
    expect(scrollIntoView).not.toHaveBeenCalled();
    expect(dim().getAttribute("data-stepped-aside")).toBe("true");
  });

  it("holds the page still under a pending required action, and places the target anyway", () => {
    render(
      <OnboardingSpotlight
        targetSelector="[data-testid='sheet-target']"
        title="Releases"
        description="What shipped"
        stepNumber={1}
        totalSteps={1}
        onNext={vi.fn()}
        onSkipAll={vi.fn()}
        keyboardNav={false}
        blockOutside
        callout={{ footer: null, autoScroll: false }}
      />
    );
    // A required action's target must be in view, whatever the reader did first.
    expect(scrollIntoView).toHaveBeenCalled();
    const drag = new Event("touchmove", { bubbles: true, cancelable: true });
    act(() => {
      document.body.dispatchEvent(drag);
    });
    // The page does not move, so the dim, ring and click blockers never trail it.
    expect(drag.defaultPrevented).toBe(true);
    expect(dim().getAttribute("data-stepped-aside")).toBeNull();
    // The sheet's own content still scrolls.
    const inSheet = new Event("touchmove", { bubbles: true, cancelable: true });
    screen.getByTestId("tour-callout").dispatchEvent(inSheet);
    expect(inSheet.defaultPrevented).toBe(false);
  });

  it("does not take a programmatic scroll for the reader's", () => {
    renderSheet();
    act(() => {
      window.dispatchEvent(new Event("scroll"));
    });
    expect(dim().getAttribute("data-stepped-aside")).toBeNull();
  });
});
