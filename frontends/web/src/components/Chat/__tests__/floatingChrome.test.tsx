/**
 * The mobile chat screen's floating chrome frame (mobile-chat-redesign §4.3):
 * it measures the top bar and the bottom band, publishes their heights as
 * `--chat-top-inset` / `--chat-bottom-inset` and as numbers, and carries the
 * scroll edge as attributes the bars style their hairline from.
 */

import { act, render, screen } from "@testing-library/react";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import {
  FLOATING_BAR_SURFACE,
  FLOATING_BOTTOM_EDGE,
  FLOATING_TOP_EDGE,
  FloatingChromeFrame,
  useFloatingChrome,
  useFloatingChromeInsets,
} from "../floatingChrome";

// jsdom lays nothing out: each bar reports the height this map gives it, and
// the ResizeObserver stub lets a test announce that a bar changed size.
const heights = new Map<string, number>();
const observers = new Set<{ callback: ResizeObserverCallback; elements: Set<Element> }>();

class ResizeObserverStub {
  private entry: { callback: ResizeObserverCallback; elements: Set<Element> };
  constructor(callback: ResizeObserverCallback) {
    this.entry = { callback, elements: new Set() };
    observers.add(this.entry);
  }
  observe(element: Element) {
    this.entry.elements.add(element);
  }
  unobserve(element: Element) {
    this.entry.elements.delete(element);
  }
  disconnect() {
    observers.delete(this.entry);
  }
}

function resize(testId: string, height: number) {
  heights.set(testId, height);
  act(() => {
    for (const { callback, elements } of observers) {
      for (const element of elements) {
        if (element.getAttribute("data-testid") === testId) {
          callback([], {} as ResizeObserver);
        }
      }
    }
  });
}

const originalResizeObserver = globalThis.ResizeObserver;
const originalRect = HTMLElement.prototype.getBoundingClientRect;
beforeAll(() => {
  globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
  HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
    const height = heights.get(this.getAttribute("data-testid") ?? "") ?? 0;
    return { top: 0, bottom: height, height, left: 0, right: 0, width: 0, x: 0, y: 0, toJSON: () => ({}) } as DOMRect;
  };
});
afterAll(() => {
  globalThis.ResizeObserver = originalResizeObserver;
  HTMLElement.prototype.getBoundingClientRect = originalRect;
});
beforeEach(() => {
  heights.clear();
});

function Bars({ withBand = true }: { withBand?: boolean }) {
  const chrome = useFloatingChrome();
  const insets = useFloatingChromeInsets();
  return (
    <>
      <header ref={chrome?.topBarRef} data-testid="bar" />
      {withBand && <div ref={chrome?.bottomBandRef} data-testid="band" />}
      <output data-testid="insets">{`${insets.top}/${insets.bottom}`}</output>
      <button type="button" onClick={() => chrome?.reportScrollEdges({ top: true, bottom: false })}>
        under top
      </button>
      <button type="button" onClick={() => chrome?.reportScrollEdges({ top: false, bottom: true })}>
        under bottom
      </button>
    </>
  );
}

function frame(): HTMLElement {
  return screen.getByTestId("insets").closest("[data-floating-chrome]") as HTMLElement;
}

describe("FloatingChromeFrame", () => {
  it("publishes the measured bar heights as CSS insets and as numbers", () => {
    heights.set("bar", 48);
    heights.set("band", 120);
    render(
      <FloatingChromeFrame>
        <Bars />
      </FloatingChromeFrame>,
    );

    expect(frame().style.getPropertyValue("--chat-top-inset")).toBe("48px");
    expect(frame().style.getPropertyValue("--chat-bottom-inset")).toBe("120px");
    expect(screen.getByTestId("insets")).toHaveTextContent("48/120");
  });

  it("follows a bar that changes height, and drops a bar that unmounts to 0", () => {
    heights.set("bar", 48);
    heights.set("band", 120);
    const { rerender } = render(
      <FloatingChromeFrame>
        <Bars />
      </FloatingChromeFrame>,
    );

    // The status line appears under the title; the composer grows a line.
    resize("bar", 64);
    resize("band", 141.6);
    expect(frame().style.getPropertyValue("--chat-top-inset")).toBe("64px");
    // Whole pixels: a subpixel wobble is not a new geometry.
    expect(frame().style.getPropertyValue("--chat-bottom-inset")).toBe("142px");

    rerender(
      <FloatingChromeFrame>
        <Bars withBand={false} />
      </FloatingChromeFrame>,
    );
    expect(frame().style.getPropertyValue("--chat-bottom-inset")).toBe("0px");
    expect(screen.getByTestId("insets")).toHaveTextContent("64/0");
  });

  it("carries the scroll edge as frame attributes, toggled only on a change", () => {
    render(
      <FloatingChromeFrame>
        <Bars />
      </FloatingChromeFrame>,
    );
    expect(frame()).not.toHaveAttribute("data-scroll-edge-top");
    expect(frame()).not.toHaveAttribute("data-scroll-edge-bottom");

    act(() => screen.getByRole("button", { name: "under top" }).click());
    expect(frame()).toHaveAttribute("data-scroll-edge-top");
    expect(frame()).not.toHaveAttribute("data-scroll-edge-bottom");

    act(() => screen.getByRole("button", { name: "under bottom" }).click());
    expect(frame()).not.toHaveAttribute("data-scroll-edge-top");
    expect(frame()).toHaveAttribute("data-scroll-edge-bottom");
  });

  it("is the group the bars' hairline variants key on", () => {
    render(
      <FloatingChromeFrame>
        <Bars />
      </FloatingChromeFrame>,
    );
    expect(frame()).toHaveClass("group/chat-chrome");
    // No hairline at rest; one while content is under the bar.
    expect(FLOATING_TOP_EDGE).toMatch(/\bborder-transparent\b/);
    expect(FLOATING_TOP_EDGE).toMatch(/group-data-\[scroll-edge-top\]\/chat-chrome:border-border\/60/);
    expect(FLOATING_BOTTOM_EDGE).toMatch(/group-data-\[scroll-edge-bottom\]\/chat-chrome:border-border\/60/);
  });

  it("turns opaque with the hairline always shown under reduced transparency", () => {
    expect(FLOATING_BAR_SURFACE).toMatch(/\bbg-background\/85\b/);
    expect(FLOATING_BAR_SURFACE).toMatch(/\bbackdrop-blur-xl\b/);
    expect(FLOATING_BAR_SURFACE).toMatch(/reduce-transparency:bg-background(\s|$)/);
    expect(FLOATING_BAR_SURFACE).toMatch(/reduce-transparency:backdrop-blur-none/);
    expect(FLOATING_TOP_EDGE).toMatch(/reduce-transparency:border-border\/60/);
    expect(FLOATING_BOTTOM_EDGE).toMatch(/reduce-transparency:border-border\/60/);
  });

  it("leaves the chrome docked outside a frame", () => {
    render(<Bars />);
    expect(screen.getByTestId("insets")).toHaveTextContent("0/0");
    expect(screen.getByTestId("insets").closest("[data-floating-chrome]")).toBeNull();
  });
});
