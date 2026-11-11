import React, { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import SidebarLayout from "./sidebar_layout";
import Modal from "./modal";
import Popover from "./popover";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let media: EventTarget & { matches: boolean };
const user = userEvent.setup();
const button = (label: string) =>
  document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)!;
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]')!;
const click = async (el: Element) => {
  await act(async () => {
    await user.click(el);
  });
};
const key = async (keys: string) => {
  await act(async () => {
    await user.keyboard(keys);
  });
};

beforeEach(() => {
  media = Object.assign(new EventTarget(), { matches: true });
  Object.defineProperty(window, "matchMedia", { configurable: true, value: () => media });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  document.body.removeAttribute("style");
});

function Shell({ current = "one", extra = false }: { current?: string; extra?: boolean }) {
  const [nested, setNested] = useState(false);
  return (
    <SidebarLayout
      brand="Forge"
      navLabel="Project navigation"
      navItems={[
        { label: "One", href: "#one", active: current === "one" },
        { label: "Two", href: "#two", active: current === "two" },
      ]}
      navAppendix={extra ? <button onClick={() => setNested(true)}>Open nested</button> : undefined}
    >
      <button>Background action</button>
      <Modal open={nested} onClose={() => setNested(false)} title="Nested">
        <button>Nested action</button>
      </Modal>
    </SidebarLayout>
  );
}
function render(current = "one", extra = false) {
  act(() => root.render(<Shell current={current} extra={extra} />));
}
async function open() {
  const trigger = button("Open navigation");
  await click(trigger);
  return trigger;
}

test("mobile dialog has a title, description, visible touch close and real keyboard focus cycle", async () => {
  render();
  const trigger = await open();
  const panel = dialog();
  expect(panel.getAttribute("aria-modal")).toBe("true");
  expect(document.getElementById(panel.getAttribute("aria-labelledby")!)?.textContent).toBe(
    "Project navigation",
  );
  expect(document.getElementById(panel.getAttribute("aria-describedby")!)?.textContent).toContain(
    "Choose a page",
  );
  expect(panel.closest("[data-modal-layer]")?.parentElement).toBe(document.body);
  const close = button("Close navigation");
  expect(getComputedStyle(close).display).not.toBe("none");
  expect(getComputedStyle(close).visibility).not.toBe("hidden");
  expect(close.className).toContain("h-[44px]");
  expect(close.className).toContain("w-[44px]");
  const links = panel.querySelectorAll("nav a");
  expect(document.activeElement).toBe(links[0]);
  await key("{Shift>}{Tab}{/Shift}");
  expect(document.activeElement).toBe(close);
  // Desktop collapse is hidden by CSS (supplied below in this CSS-free harness).
  await key("{Shift>}{Tab}{/Shift}");
  expect(document.activeElement).toBe(links[1]);
  await key("{Tab}");
  expect(document.activeElement).toBe(close);
  await key("{Tab}");
  expect(document.activeElement).toBe(links[0]);
  expect(host.getAttribute("inert")).toBe("");
  expect(host.getAttribute("aria-hidden")).toBe("true");
  await click(close);
  expect(dialog()).toBeNull();
  expect(host.hasAttribute("inert")).toBe(false);
  expect(document.activeElement).toBe(trigger);
});

// jsdom does not load Tailwind. Model only the desktop control's mobile
// visibility; everything else (focus, events, portal and isolation) is real DOM.
beforeEach(() => {
  const style = document.createElement("style");
  style.textContent =
    'button[aria-label="Collapse sidebar"],button[aria-label="Expand sidebar"] { display: none; }';
  document.body.appendChild(style);
});

test.each(["escape", "route", "active route", "scrim", "unmount"])(
  "%s closes and restores preexisting background/scroll state",
  async (reason) => {
    host.setAttribute("aria-hidden", "false");
    const preexisting = document.createElement("div");
    preexisting.setAttribute("inert", "already");
    preexisting.setAttribute("aria-hidden", "false");
    document.body.appendChild(preexisting);
    document.body.style.setProperty("overscroll-behavior", "contain");
    document.body.style.setProperty("overflow", "scroll");
    document.body.style.setProperty("padding-right", "7px", "important");
    render();
    const trigger = await open();
    expect(document.body.style.overflow).toBe("hidden");
    expect(document.body.style.getPropertyValue("overscroll-behavior")).toBe("none");
    if (reason === "escape") await key("{Escape}");
    else if (reason === "route") await click(dialog().querySelector("a")!);
    else if (reason === "active route") render("two");
    else if (reason === "scrim") await click(dialog().previousElementSibling!);
    else act(() => root.render(null));
    expect(dialog()).toBeNull();
    expect(document.body.style.overflow).toBe("scroll");
    expect(document.body.style.paddingRight).toBe("7px");
    expect(document.body.style.getPropertyPriority("padding-right")).toBe("important");
    expect(document.body.style.getPropertyValue("overscroll-behavior")).toBe("contain");
    expect(host.hasAttribute("inert")).toBe(false);
    expect(host.getAttribute("aria-hidden")).toBe("false");
    expect(preexisting.getAttribute("inert")).toBe("already");
    expect(preexisting.getAttribute("aria-hidden")).toBe("false");
    if (reason !== "unmount") expect(document.activeElement).toBe(trigger);
  },
);

test("desktop transition removes modal behavior and narrowing again stays closed", async () => {
  render();
  await open();
  act(() => {
    media.matches = false;
    media.dispatchEvent(new Event("change"));
  });
  expect(dialog()).toBeNull();
  expect(document.body.style.overflow).toBe("");
  expect(host.hasAttribute("inert")).toBe(false);
  expect(host.querySelector("aside")?.hasAttribute("aria-modal")).toBe(false);
  expect(document.activeElement).toBe(host.querySelector("nav a"));
  await key("{Escape}");
  act(() => {
    media.matches = true;
    media.dispatchEvent(new Event("change"));
  });
  expect(dialog()).toBeNull();
  expect(button("Open navigation").getAttribute("aria-expanded")).toBe("false");
});

test("a Modal above the drawer owns Escape/focus, and closing it keeps drawer isolation and scroll lock", async () => {
  render("one", true);
  await open();
  const drawer = dialog();
  const nestedTrigger = Array.from(drawer.querySelectorAll("button")).find(
    (el) => el.textContent === "Open nested",
  )!;
  await click(nestedTrigger);
  expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(2);
  expect(drawer.closest("[data-modal-layer]")?.hasAttribute("inert")).toBe(true);
  expect(document.activeElement?.textContent).toBe("Nested action");
  await key("{Escape}");
  expect(dialog()).toBe(drawer);
  expect(document.activeElement).toBe(nestedTrigger);
  expect(drawer.closest("[data-modal-layer]")?.hasAttribute("inert")).toBe(false);
  expect(host.hasAttribute("inert")).toBe(true);
  expect(document.body.style.overflow).toBe("hidden");
  await key("{Escape}");
  expect(dialog()).toBeNull();
  expect(document.body.style.overflow).toBe("");
});

test("portaled popover inside the drawer stays interactive and handles Escape before the drawer", async () => {
  act(() =>
    root.render(
      <SidebarLayout
        brand="Forge"
        navItems={[]}
        brandAccessory={
          <Popover trigger="Choose scope" label="Scopes">
            <button>Scope one</button>
          </Popover>
        }
      >
        <button>Page action</button>
      </SidebarLayout>,
    ),
  );
  await open();
  const drawer = dialog();
  const trigger = Array.from(drawer.querySelectorAll("button")).find(
    (el) => el.textContent === "Choose scope",
  )!;
  await click(trigger);
  const popup = document.getElementById(trigger.getAttribute("aria-controls")!)!;
  // Let the child-list observer see the newly created portal.
  await act(async () => {
    await Promise.resolve();
  });
  expect(popup.closest("[inert]")).toBeNull();
  expect(host.hasAttribute("inert")).toBe(true);
  await click(popup.querySelector("button")!);
  expect(document.activeElement?.textContent).toBe("Scope one");
  await key("{Escape}");
  expect(popup.isConnected).toBe(false);
  expect(dialog()).toBe(drawer);
  await key("{Escape}");
  expect(dialog()).toBeNull();
});

test("removing a lower drawer while a Modal is open preserves top focus and scroll lock", async () => {
  const renderStack = (lower: boolean, upper: boolean) =>
    act(() =>
      root.render(
        <>
          {lower && <Shell key="drawer" />}
          <Modal key="top" open={upper} onClose={() => {}} title="Top">
            <button>Top action</button>
          </Modal>
        </>,
      ),
    );
  renderStack(true, false);
  await open();
  renderStack(true, true);
  const top = document.querySelector('[role="dialog"]:not(aside)');
  const focused = document.activeElement;
  renderStack(false, true);
  expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
  expect(document.querySelector('[role="dialog"]')).toBe(top);
  expect(document.activeElement).toBe(focused);
  expect(top?.contains(document.activeElement)).toBe(true);
  expect(document.body.style.overflow).toBe("hidden");
  expect(host.hasAttribute("inert")).toBe(true);
  renderStack(false, false);
  expect(host.hasAttribute("inert")).toBe(false);
  expect(document.body.style.overflow).toBe("");
});

test.each(["remove", "desktop"])(
  "%s of a lower drawer cannot steal focus from a surviving portaled Modal",
  async (change) => {
    const renderStack = (lower: boolean, upper: boolean, replaceFocused = false) =>
      act(() =>
        root.render(
          <>
            {lower && <Shell key="drawer" />}
            <Modal key="top" open={upper} onClose={() => {}} title="Top">
              <button key={replaceFocused ? "replacement" : "original"}>Top action</button>
            </Modal>
          </>,
        ),
      );
    renderStack(true, false);
    await open();
    renderStack(true, true);
    const top = document.querySelector<HTMLElement>('[role="dialog"]:not(aside)')!;
    expect(top.contains(document.activeElement)).toBe(true);
    if (change === "remove") {
      // Removal of the active element makes body active without a focusin event
      // in browsers. Replace it in the same commit as lower-dialog teardown.
      renderStack(false, true, true);
    } else {
      act(() => {
        media.matches = false;
        media.dispatchEvent(new Event("change"));
      });
    }
    await act(async () => {
      await Promise.resolve();
    });
    expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
    expect(top.contains(document.activeElement)).toBe(true);
    expect(document.body.style.overflow).toBe("hidden");
    // Programmatic/page focus (including a lower responsive transition effect)
    // is contained immediately, without needing the user to press Tab.
    const outside = document.createElement("button");
    document.body.appendChild(outside);
    act(() => outside.focus());
    expect(top.contains(document.activeElement)).toBe(true);
    renderStack(false, false);
    outside.focus();
    expect(document.activeElement).toBe(outside); // no leaked focusin listener
    expect(document.body.style.overflow).toBe("");
  },
);

test("empty drawer focuses its close button and restores the explicit trigger after Safari-style activation", async () => {
  act(() =>
    root.render(
      <SidebarLayout brand="Forge" navItems={[]}>
        <button>Page action</button>
      </SidebarLayout>,
    ),
  );
  const trigger = button("Open navigation");
  host.querySelector("main button")?.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
  (host.querySelector("main button") as HTMLElement).focus();
  // Safari click activation doesn't move focus to the button.
  act(() => trigger.click());
  expect(document.activeElement).toBe(button("Close navigation"));
  await key("{Tab}");
  expect(document.activeElement).toBe(button("Close navigation"));
  await key("{Shift>}{Tab}{/Shift}");
  expect(document.activeElement).toBe(button("Close navigation"));
  await key("{Escape}");
  expect(document.activeElement).toBe(trigger);
});

test("ordinary in-place Modal preserves its props, inner Escape handling, and disabled-opener fallback", async () => {
  let closes = 0;
  const fallback = document.createElement("button");
  fallback.textContent = "Fallback";
  document.body.appendChild(fallback);
  const opener = document.createElement("button");
  document.body.appendChild(opener);
  opener.focus();
  const show = (open: boolean) =>
    act(() =>
      root.render(
        <Modal
          open={open}
          onClose={() => {
            closes++;
          }}
          title="Compose"
          description="A message"
          portal={false}
          fallbackFocus={() => fallback.focus()}
          footer={<button>Send</button>}
        >
          <input
            aria-label="Message"
            onKeyDown={(e) => {
              if (e.key === "Escape") e.preventDefault();
            }}
          />
        </Modal>,
      ),
    );
  show(true);
  expect(host.contains(dialog())).toBe(true);
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Message");
  await key("{Escape}");
  expect(closes).toBe(0);
  button("Close dialog").focus();
  await key("{Shift>}{Tab}{/Shift}");
  expect(document.activeElement?.textContent).toBe("Send");
  await key("{Tab}");
  expect(document.activeElement).toBe(button("Close dialog"));
  await key("{Escape}");
  expect(closes).toBe(1);
  opener.disabled = true;
  show(false);
  expect(document.activeElement).toBe(fallback);
  expect(document.body.style.overflow).toBe("");
});

test("multiple consumers have distinct controls and each closes independently", async () => {
  act(() =>
    root.render(
      <>
        <Shell />
        <Shell />
      </>,
    ),
  );
  const triggers = document.querySelectorAll<HTMLButtonElement>(
    'button[aria-label="Open navigation"]',
  );
  expect(triggers[0].getAttribute("aria-controls")).not.toBe(
    triggers[1].getAttribute("aria-controls"),
  );
  await click(triggers[0]);
  // A second consumer can be opened by external app state/programmatic action.
  act(() => triggers[1].click());
  expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(2);
  await key("{Escape}");
  expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
  expect(document.body.style.overflow).toBe("hidden");
  await key("{Escape}");
  expect(dialog()).toBeNull();
  expect(document.body.style.overflow).toBe("");
});
