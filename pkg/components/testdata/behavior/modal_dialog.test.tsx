// Behaviour of useModalDialog on a custom surface (a drawer that is not a
// <Modal>) in a real DOM (jsdom): the Tab cycle wraps at both ends, an empty
// panel keeps focus, and content added after opening joins the cycle. Run by
// TestComponentBehaviour.
import React, { act, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { createRoot, type Root } from "react-dom/client";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { useModalDialog } from "./modal_dialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const user = userEvent.setup();
const key = async (keys: string) => {
  await act(async () => {
    await user.keyboard(keys);
  });
};
const byText = (text: string) =>
  Array.from(document.querySelectorAll<HTMLElement>("button, a")).find(
    (el) => el.textContent === text,
  )!;
const panel = () => document.querySelector<HTMLElement>('[role="dialog"]');

beforeEach(() => {
  // jsdom loads no stylesheet: model a desktop-only control the way a
  // responsive class hides it on a phone, on its WRAPPER, so the control's
  // own computed display is still "block".
  const style = document.createElement("style");
  style.textContent = ".desktop-only { display: none; }";
  document.head.appendChild(style);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  document.head.innerHTML = "";
  document.body.removeAttribute("style");
});

// A drawer built on the hook alone, as its doc comment invites: the panel
// carries no tabIndex of its own, so the hook must make it focusable.
function Drawer({ children }: { children?: React.ReactNode }) {
  const [open, setOpen] = useState(false);
  const panelRef = useRef<HTMLDivElement>(null);
  useModalDialog({ open, panelRef, onClose: () => setOpen(false) });
  return (
    <>
      <button onClick={() => setOpen(true)}>Open drawer</button>
      <button>Page action</button>
      {open &&
        createPortal(
          <div data-modal-layer="">
            <div role="presentation" data-scrim="" onClick={() => setOpen(false)} />
            <div ref={panelRef} role="dialog" aria-modal="true" aria-label="Drawer">
              {children}
            </div>
          </div>,
          document.body,
        )}
    </>
  );
}

function render(children?: React.ReactNode) {
  act(() => root.render(<Drawer>{children}</Drawer>));
}
async function open() {
  const trigger = byText("Open drawer");
  await act(async () => {
    await user.click(trigger);
  });
  return trigger;
}

test("Tab on the last stop wraps to the first, Shift+Tab on the first wraps to the last", async () => {
  render(
    <>
      <a href="#one">One</a>
      <button>Two</button>
      <a href="#three">Three</a>
      {/* The trap's last DOM focusable, hidden by CSS on a phone: not a stop. */}
      <div className="desktop-only">
        <button>Collapse</button>
      </div>
    </>,
  );
  await open();
  const first = byText("One");
  const last = byText("Three");
  expect(document.activeElement).toBe(first);

  last.focus();
  await key("{Tab}");
  expect(document.activeElement).toBe(first);
  await key("{Shift>}{Tab}{/Shift}");
  expect(document.activeElement).toBe(last);
  // The middle of the cycle is the browser's own order, untouched.
  await key("{Shift>}{Tab}{/Shift}");
  expect(document.activeElement).toBe(byText("Two"));
});

test("Tab from the panel itself, or from a disabled last control, stays inside", async () => {
  render(
    <>
      <button>First</button>
      <button>Last</button>
      <button disabled>Disabled</button>
    </>,
  );
  await open();
  byText("Last").focus();
  await key("{Tab}");
  expect(document.activeElement).toBe(byText("First"));
  panel()!.focus();
  await key("{Shift>}{Tab}{/Shift}");
  expect(document.activeElement).toBe(byText("Last"));
});

test("an empty panel is made focusable and keeps focus on Tab and Shift+Tab", async () => {
  render(
    <>
      <p>Nothing to choose yet.</p>
      <div className="desktop-only">
        <button>Collapse</button>
      </div>
    </>,
  );
  const trigger = await open();
  const dialog = panel()!;
  expect(dialog.getAttribute("tabindex")).toBe("-1");
  expect(document.activeElement).toBe(dialog);
  await key("{Tab}");
  expect(document.activeElement).toBe(dialog);
  await key("{Shift>}{Tab}{/Shift}");
  expect(document.activeElement).toBe(dialog);
  await key("{Tab}{Tab}");
  expect(document.activeElement).toBe(dialog);

  await key("{Escape}");
  expect(panel()).toBeNull();
  expect(document.activeElement).toBe(trigger);
});

test("the panel's own tabindex is kept, and one the hook added leaves on close", async () => {
  // Panels that stay mounted when closed (an in-place drawer).
  function InPlace({ open, tabIndex }: { open: boolean; tabIndex?: number }) {
    const panelRef = useRef<HTMLDivElement>(null);
    useModalDialog({ open, panelRef, onClose: () => {} });
    return (
      <div ref={panelRef} role="dialog" aria-modal={open} tabIndex={tabIndex}>
        <p>empty</p>
      </div>
    );
  }
  act(() => root.render(<InPlace open tabIndex={0} />));
  expect(panel()!.getAttribute("tabindex")).toBe("0");
  expect(document.activeElement).toBe(panel());
  act(() => root.render(<InPlace open={false} tabIndex={0} />));
  expect(panel()!.getAttribute("tabindex")).toBe("0");

  act(() => root.render(<InPlace open={false} />));
  expect(panel()!.hasAttribute("tabindex")).toBe(false);
  act(() => root.render(<InPlace open />));
  expect(panel()!.getAttribute("tabindex")).toBe("-1");
  expect(document.activeElement).toBe(panel());
  act(() => root.render(<InPlace open={false} />));
  expect(panel()!.hasAttribute("tabindex")).toBe(false);
});

test("focusables added after opening are trapped, and the wrap follows them", async () => {
  function Growing() {
    const [more, setMore] = useState(false);
    return (
      <>
        <button>First</button>
        <button onClick={() => setMore(true)}>Load more</button>
        {more && <a href="#late">Late link</a>}
      </>
    );
  }
  render(<Growing />);
  await open();
  const first = byText("First");
  await act(async () => {
    await user.click(byText("Load more"));
  });
  const late = byText("Late link");
  // The old last stop is no longer the end of the cycle.
  byText("Load more").focus();
  await key("{Tab}");
  expect(document.activeElement).toBe(late);
  await key("{Tab}");
  expect(document.activeElement).toBe(first);
  await key("{Shift>}{Tab}{/Shift}");
  expect(document.activeElement).toBe(late);
});

test("Escape and the scrim close it, the page is inert while open, and focus returns to the trigger", async () => {
  render(<button>Inside</button>);
  const trigger = await open();
  expect(document.activeElement).toBe(byText("Inside"));
  expect(host.getAttribute("inert")).toBe("");
  expect(host.getAttribute("aria-hidden")).toBe("true");
  // Focus pulled onto the page behind the scrim is put back in the panel.
  act(() => byText("Page action").focus());
  expect(document.activeElement).toBe(byText("Inside"));

  await key("{Escape}");
  expect(panel()).toBeNull();
  expect(host.hasAttribute("inert")).toBe(false);
  expect(document.activeElement).toBe(trigger);

  await open();
  await act(async () => {
    await user.click(document.querySelector("[data-scrim]")!);
  });
  expect(panel()).toBeNull();
  expect(document.activeElement).toBe(trigger);
});
