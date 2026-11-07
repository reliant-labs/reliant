// Behaviour of modal in a real DOM (jsdom): the overlay follows the visual
// viewport, which is what keeps a sheet's footer and focused field above a
// phone's software keyboard. Run by TestComponentBehaviour.
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test } from "vitest";
import Modal from "./modal";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// jsdom has no visualViewport: stand one in, as an EventTarget whose size the
// test moves the way a keyboard opening does.
class FakeVisualViewport extends EventTarget {
  height = 800;
  offsetTop = 0;
  listeners = 0;
  override addEventListener(...args: Parameters<EventTarget["addEventListener"]>) {
    this.listeners++;
    super.addEventListener(...args);
  }
  override removeEventListener(...args: Parameters<EventTarget["removeEventListener"]>) {
    this.listeners--;
    super.removeEventListener(...args);
  }
}

let host: HTMLDivElement;
let root: Root;
let vv: FakeVisualViewport;

beforeEach(() => {
  vv = new FakeVisualViewport();
  Object.defineProperty(window, "visualViewport", { configurable: true, value: vv });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  Object.defineProperty(window, "visualViewport", { configurable: true, value: undefined });
});

function render(open: boolean) {
  act(() =>
    root.render(
      <Modal open={open} onClose={() => {}} title="Compose" footer={<button>Send</button>}>
        <textarea aria-label="Message" />
      </Modal>,
    ),
  );
}

const overlay = () =>
  document.querySelector<HTMLElement>('[role="dialog"]')?.parentElement as HTMLElement;

test("the overlay is sized to the visual viewport, and follows it as the keyboard opens", () => {
  render(true);
  expect(overlay().style.getPropertyValue("--modal-vv-height")).toBe("800px");
  expect(overlay().style.getPropertyValue("--modal-vv-top")).toBe("0px");

  // The keyboard opens: the visual viewport shrinks and pans.
  vv.height = 420;
  vv.offsetTop = 120;
  act(() => {
    vv.dispatchEvent(new Event("resize"));
    vv.dispatchEvent(new Event("scroll"));
  });
  expect(overlay().style.getPropertyValue("--modal-vv-height")).toBe("420px");
  expect(overlay().style.getPropertyValue("--modal-vv-top")).toBe("120px");
});

test("closing stops listening to the visual viewport", () => {
  render(true);
  expect(vv.listeners).toBe(2);
  render(false);
  expect(vv.listeners).toBe(0);
});

test("without visualViewport the overlay falls back to its dvh classes", () => {
  Object.defineProperty(window, "visualViewport", { configurable: true, value: undefined });
  render(true);
  expect(overlay().style.getPropertyValue("--modal-vv-height")).toBe("");
  expect(overlay().className).toContain("h-[var(--modal-vv-height,100dvh)]");
});
