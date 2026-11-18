// Status's dot versus its busy ring, rendered in a real DOM (jsdom). Run by
// TestComponentBehaviour. Mirrors reliant's forge-ui status test.
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test } from "vitest";
import Status from "./status";

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

function render(ui: React.ReactElement) {
  act(() => root.render(ui));
}

function textNode(text: string): HTMLElement {
  const el = Array.from(host.querySelectorAll<HTMLElement>("span")).find(
    (span) => span.children.length === 0 && span.textContent === text,
  );
  if (!el) throw new Error(`no element with text ${JSON.stringify(text)}`);
  return el;
}

test("is a coloured dot and a word by default", () => {
  render(<Status tone="danger" label="Drifted" />);
  expect(textNode("Drifted")).toBeTruthy();
  expect(host.querySelector("[data-status-spinner]")).toBeNull();
  expect(host.querySelector(".bg-danger")).not.toBeNull();
  expect(host.firstElementChild?.hasAttribute("data-busy")).toBe(false);
});

test("is a neutral spinning ring of the dot's size while busy, whatever the tone", () => {
  render(<Status tone="danger" label="Settling" size="sm" busy />);
  const ring = host.querySelector("[data-status-spinner]");
  expect(ring).not.toBeNull();
  expect(ring?.getAttribute("aria-hidden")).toBe("true");
  expect(ring?.className).toContain("animate-spin");
  expect(ring?.className).toContain("h-1.5 w-1.5");
  expect(ring?.className).toContain("border-ink-muted");
  // No status hue on the ring, and no dot beside it.
  expect(host.querySelector(".bg-danger")).toBeNull();
  expect(host.firstElementChild?.getAttribute("data-busy")).toBe("true");
});

test("holds still as a whole ring under reduced motion", () => {
  render(<Status label="Settling" busy />);
  const ring = host.querySelector("[data-status-spinner]");
  expect(ring?.className).toContain("motion-reduce:animate-none");
  expect(ring?.className).toContain("motion-reduce:border-t-ink-muted/40");
});

test("keeps the word for screen readers when only the spinner shows", () => {
  render(<Status label="Settling" hideLabel busy />);
  expect(textNode("Settling").classList.contains("sr-only")).toBe(true);
});
