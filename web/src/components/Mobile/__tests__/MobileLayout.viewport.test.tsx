import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MobileLayout } from "../MobileLayout";

class TestViewport extends EventTarget {
  width = 390;
  height = 800;
  offsetTop = 0;
  offsetLeft = 0;
  scale = 1;
}
let viewport: TestViewport;
beforeEach(() => {
  vi.useFakeTimers();
  viewport = new TestViewport();
  vi.stubGlobal("visualViewport", viewport);
  vi.stubGlobal("innerWidth", 390);
  vi.stubGlobal("innerHeight", 800);
});
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });
function shell() { return document.querySelector<HTMLElement>('[data-surface="mobile"]')!; }
function update(event = "resize") {
  act(() => { viewport.dispatchEvent(new Event(event)); vi.advanceTimersByTime(20); });
}

describe("Mobile visual viewport", () => {
  it("locks document scrolling and restores the original styles on leaving mobile", () => {
    const original = document.body.style.position;
    const { unmount } = render(<MobileLayout><div>Chat</div></MobileLayout>);
    expect(document.body.style.position).toBe("fixed");
    expect(document.documentElement.style.overflow).toBe("hidden");
    unmount();
    expect(document.body.style.position).toBe(original);
    expect(document.documentElement.style.getPropertyValue("--mobile-bottom-inset")).toBe("");
  });
  it("follows keyboard height and Safari pan without leaving a home-indicator gap", () => {
    render(<MobileLayout><textarea aria-label="Message" /></MobileLayout>);
    fireEvent.focusIn(screen.getByRole("textbox"));
    screen.getByRole("textbox").focus();
    viewport.height = 360;
    viewport.offsetTop = 120;
    update();
    expect(shell().style.height).toBe("360px");
    expect(shell().style.top).toBe("120px");
    expect(shell()).toHaveAttribute("data-keyboard-visible", "true");
    expect(document.documentElement.style.getPropertyValue("--mobile-bottom-inset")).toBe("0px");
    viewport.offsetTop = 150;
    update("scroll");
    expect(shell().style.top).toBe("150px");
    screen.getByRole("textbox").blur();
    viewport.height = 800;
    viewport.offsetTop = 0;
    update();
    expect(shell().style.height).toBe("800px");
    expect(shell()).not.toHaveAttribute("data-keyboard-visible");
  });
  it("supports tablet rotation and does not reflow while pinch zooming", () => {
    render(<MobileLayout><div>Chat</div></MobileLayout>);
    viewport.width = 1024;
    viewport.height = 768;
    update();
    expect(shell().style.width).toBe("1024px");
    viewport.scale = 2;
    viewport.width = 512;
    viewport.height = 384;
    update();
    expect(shell().style.width).toBe("1024px");
  });
});
