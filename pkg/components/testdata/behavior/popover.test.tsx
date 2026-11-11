// Behaviour of popover's outside-press and focus-out dismissal, in a real DOM
// (jsdom) with real React event delegation. Run by TestComponentBehaviour.
import React, { act, useState } from "react";
import { createPortal } from "react-dom";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test } from "vitest";
import Popover from "./popover";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
});

// A control in the panel that portals its own layer to <body>, outside the
// panel's DOM but inside its React tree (a full-screen picker sheet).
function PortalledLayer() {
  return createPortal(
    <div data-testid="layer">
      <button type="button" data-testid="layer-button">
        pick
      </button>
    </div>,
    document.body,
  );
}

// stopPropagation in the panel's React tree stops the native event at React's
// listener, so the popover's document listener never sees that press.
function Stopper() {
  return (
    <button type="button" data-testid="stopper" onPointerDown={(e) => e.stopPropagation()}>
      stop
    </button>
  );
}

function Harness() {
  const [open, setOpen] = useState(true);
  return (
    <>
      <button type="button" data-testid="outside">
        outside
      </button>
      <Popover trigger="Open" open={open} onOpenChange={setOpen} label="panel">
        <button type="button" data-testid="inside">
          inside
        </button>
        <Stopper />
        <PortalledLayer />
      </Popover>
    </>
  );
}

function render() {
  act(() => root.render(<Harness />));
}

const byId = (id: string) => document.querySelector<HTMLElement>(`[data-testid="${id}"]`);
const isOpen = () => document.querySelector('[role="dialog"]') !== null;

function press(el: Element) {
  act(() => {
    el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, cancelable: true }));
  });
}

function focus(el: HTMLElement) {
  act(() => el.focus());
}

test("a press on a portalled descendant keeps the popover open", () => {
  render();
  expect(isOpen()).toBe(true);
  press(byId("layer-button")!);
  expect(isOpen()).toBe(true);
});

test("a press outside the popover closes it", () => {
  render();
  press(byId("outside")!);
  expect(isOpen()).toBe(false);
});

test("a press whose propagation was stopped leaves no stale inside mark", () => {
  render();
  press(byId("stopper")!);
  expect(isOpen()).toBe(true);
  press(byId("outside")!);
  expect(isOpen()).toBe(false);
});

test("focus moving to a portalled descendant keeps the popover open", () => {
  render();
  focus(byId("inside")!);
  focus(byId("layer-button")!);
  expect(isOpen()).toBe(true);
});

test("focus leaving the panel for the page closes it", () => {
  render();
  focus(byId("inside")!);
  focus(byId("outside")!);
  expect(isOpen()).toBe(false);
});
