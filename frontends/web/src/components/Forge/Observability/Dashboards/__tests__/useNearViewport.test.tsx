import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";

import { NEAR_VIEWPORT_MARGIN, useNearViewport } from "../useNearViewport";

type Callback = (entries: Partial<IntersectionObserverEntry>[]) => void;

class FakeObserver {
  static all: FakeObserver[] = [];
  disconnected = false;
  observed: Element[] = [];
  callback: Callback;
  options?: IntersectionObserverInit;
  constructor(callback: Callback, options?: IntersectionObserverInit) {
    this.callback = callback;
    this.options = options;
    FakeObserver.all.push(this);
  }
  observe(element: Element) {
    this.observed.push(element);
  }
  disconnect() {
    this.disconnected = true;
  }
  fire(isIntersecting: boolean) {
    this.callback([{ isIntersecting }]);
  }
}

function Probe() {
  const [ref, near] = useNearViewport();
  return (
    <div ref={ref} data-testid="probe">
      {near ? "near" : "far"}
    </div>
  );
}

beforeEach(() => {
  FakeObserver.all = [];
  vi.stubGlobal("IntersectionObserver", FakeObserver);
});
afterEach(() => vi.unstubAllGlobals());

describe("useNearViewport", () => {
  it("is far until the element comes near the viewport, then stays near and stops observing", () => {
    render(<Probe />);
    expect(screen.getByTestId("probe")).toHaveTextContent("far");
    const [observer] = FakeObserver.all;
    expect(observer!.options?.rootMargin).toBe(NEAR_VIEWPORT_MARGIN);

    act(() => observer!.fire(false));
    expect(screen.getByTestId("probe")).toHaveTextContent("far");
    act(() => observer!.fire(true));
    expect(screen.getByTestId("probe")).toHaveTextContent("near");
    expect(observer!.disconnected).toBe(true);
  });

  it("releases its observer when the element goes away", () => {
    const { unmount } = render(<Probe />);
    unmount();
    expect(FakeObserver.all.every((observer) => observer.disconnected)).toBe(true);
  });

  it("treats everything as near where IntersectionObserver does not exist", () => {
    vi.unstubAllGlobals();
    vi.stubGlobal("IntersectionObserver", undefined);
    render(<Probe />);
    expect(screen.getByTestId("probe")).toHaveTextContent("near");
  });
});
