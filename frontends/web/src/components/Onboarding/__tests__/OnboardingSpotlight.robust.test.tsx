/**
 * The spotlight follows the page under it and always fits the screen:
 *
 *  - a target that goes away (a navigation, a re-render) takes its ring and
 *    dim with it, and the card waits on screen in the corner; it re-anchors
 *    when a target comes back, with no scroll or resize to prompt it;
 *  - a hidden copy of the target (a desktop table behind its phone layout) is
 *    never what it rings;
 *  - the card stays inside the viewport and off its target, however tall it
 *    measures and wherever the target sits.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import React from "react";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { OnboardingSpotlight } from "../OnboardingSpotlight";
import { overlapArea } from "../calloutPlacement";

type Rect = { top: number; left: number; width: number; height: number };
const rects = new Map<string, Rect>();
let cardSize = { width: 320, height: 200 };

const box = ({ top, left, width, height }: Rect) =>
  ({ top, left, width, height, right: left + width, bottom: top + height, x: left, y: top, toJSON: () => ({}) }) as DOMRect;

function spotlight(selector: string, compact = true) {
  return render(
    React.createElement(OnboardingSpotlight, {
      targetSelector: selector,
      title: "A step",
      description: "What this is",
      stepNumber: 1,
      totalSteps: 3,
      onNext: vi.fn(),
      onSkipAll: vi.fn(),
      keyboardNav: false,
      callout: compact ? { footer: React.createElement("span", null, "controls") } : undefined,
    })
  );
}

function addTarget(id: string, rect: Rect, visible = true) {
  const el = document.createElement("div");
  el.setAttribute("data-testid", id);
  rects.set(id, rect);
  (el as HTMLElement & { checkVisibility: () => boolean }).checkVisibility = () => visible;
  document.body.appendChild(el);
  return el;
}

const card = () => screen.getByTestId("tour-callout");
const cardBox = () => ({ top: parseFloat(card().style.top), left: parseFloat(card().style.left), ...cardSize });

beforeEach(() => {
  rects.clear();
  cardSize = { width: 320, height: 200 };
  Object.defineProperty(window, "innerWidth", { configurable: true, value: 1440 });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 900 });
  const original = Element.prototype.getBoundingClientRect;
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    if (this.getAttribute("data-testid") === "tour-callout" && this instanceof HTMLElement) {
      const cap = parseFloat(this.style.maxHeight);
      return box({ top: parseFloat(this.style.top) || 0, left: parseFloat(this.style.left) || 0, width: cardSize.width, height: Number.isNaN(cap) ? cardSize.height : Math.min(cardSize.height, cap) });
    }
    const rect = rects.get(this.getAttribute("data-testid") ?? "");
    return rect ? box(rect) : original.call(this);
  });
  vi.spyOn(console, "warn").mockImplementation(() => undefined);
});
afterEach(() => {
  cleanup();
  document.querySelectorAll("[data-testid^='target']").forEach((el) => el.remove());
  vi.restoreAllMocks();
});

describe("when the page changes under it", () => {
  it("drops the ring and dim when its target goes away, keeps the card on screen, and re-anchors when it returns", async () => {
    const target = addTarget("target-row", { top: 300, left: 400, width: 600, height: 40 });
    spotlight("[data-testid='target-row']");
    await waitFor(() => expect(screen.getByTestId("onboarding-spotlight-ring")).toBeInTheDocument());

    // A navigation removes the row: no scroll, no resize.
    act(() => target.remove());
    await waitFor(() => expect(screen.queryByTestId("onboarding-spotlight-ring")).toBeNull());
    expect(card()).toHaveAttribute("data-target-missing", "true");
    expect(screen.getByTestId("onboarding-spotlight-dim").getAttribute("class")).toContain("opacity-0");
    const { top, left } = cardBox();
    expect(top).toBeGreaterThanOrEqual(16);
    expect(left).toBeGreaterThanOrEqual(16);
    expect(top + cardSize.height).toBeLessThanOrEqual(900 - 16);
    expect(left + cardSize.width).toBeLessThanOrEqual(1440 - 16);

    // The page renders the row again, somewhere else.
    addTarget("target-row", { top: 500, left: 400, width: 600, height: 40 });
    await waitFor(() => expect(screen.getByTestId("onboarding-spotlight-ring").style.top).toBe(`${500 - 8}px`));
    expect(card()).not.toHaveAttribute("data-target-missing");
  });

  it("never rings a hidden copy of its target", async () => {
    // The same row twice: a desktop table hidden on this screen, and the layout that shows.
    addTarget("target-dup-hidden", { top: 0, left: 0, width: 0, height: 0 }, false).setAttribute("data-row", "dup");
    addTarget("target-dup-shown", { top: 420, left: 300, width: 500, height: 30 }).setAttribute("data-row", "dup");
    spotlight("[data-row='dup']");
    await waitFor(() => expect(screen.getByTestId("onboarding-spotlight-ring").style.top).toBe(`${420 - 8}px`));
  });
});

describe("a target outside the window", () => {
  it("brings a journey step's target into view on arrival", async () => {
    const el = addTarget("target-below", { top: 1400, left: 300, width: 400, height: 40 });
    const scrollIntoView = vi.fn();
    el.scrollIntoView = scrollIntoView;
    spotlight("[data-testid='target-below']");
    await waitFor(() => expect(scrollIntoView).toHaveBeenCalledWith({ block: "center", behavior: "auto" }));
  });

  it("leaves the page alone when told to, and waits in the corner with no dim", async () => {
    const el = addTarget("target-below", { top: 1400, left: 300, width: 400, height: 40 });
    const scrollIntoView = vi.fn();
    el.scrollIntoView = scrollIntoView;
    render(
      React.createElement(OnboardingSpotlight, {
        targetSelector: "[data-testid='target-below']",
        title: "A tip",
        description: "What this is",
        stepNumber: 1,
        totalSteps: 1,
        onNext: vi.fn(),
        onSkipAll: vi.fn(),
        keyboardNav: false,
        callout: { footer: null, autoScroll: false },
      })
    );
    await waitFor(() => expect(card()).toHaveAttribute("data-target-offscreen", "true"));
    expect(scrollIntoView).not.toHaveBeenCalled();
    expect(screen.getByTestId("onboarding-spotlight-dim").getAttribute("class")).toContain("opacity-0");
    const { top, left } = cardBox();
    expect(top + cardSize.height).toBeLessThanOrEqual(900 - 16);
    expect(left + cardSize.width).toBeLessThanOrEqual(1440 - 16);
  });
});

describe("it always fits the viewport", () => {
  it.each([
    ["a tall card on a short window", { w: 1024, h: 500 }, { top: 200, left: 300, width: 200, height: 40 }, { width: 320, height: 900 }],
    ["a target in the bottom-right corner", { w: 1024, h: 768 }, { top: 720, left: 900, width: 110, height: 40 }, { width: 320, height: 240 }],
    ["a target filling most of the screen", { w: 800, h: 600 }, { top: 20, left: 20, width: 760, height: 560 }, { width: 320, height: 200 }],
  ])("%s", async (_name, viewport, target, size) => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: viewport.w });
    Object.defineProperty(window, "innerHeight", { configurable: true, value: viewport.h });
    cardSize = size;
    addTarget("target-fit", target);
    spotlight("[data-testid='target-fit']");
    await waitFor(() => expect(card().style.maxHeight).not.toBe(""));
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
    const placed = cardBox();
    const height = Math.min(size.height, parseFloat(card().style.maxHeight));
    expect(placed.top).toBeGreaterThanOrEqual(16);
    expect(placed.left).toBeGreaterThanOrEqual(16);
    expect(placed.top + height).toBeLessThanOrEqual(viewport.h - 16);
    expect(placed.left + size.width).toBeLessThanOrEqual(viewport.w - 16);
    if (target.width < viewport.w / 2) expect(overlapArea(target, { ...placed, height })).toBe(0);
  });
});
