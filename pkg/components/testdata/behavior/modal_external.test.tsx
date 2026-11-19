import React, { act, useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import Modal from "./modal";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
  document.body.removeAttribute("style");
});
const settle = async () => {
  await act(async () => {
    await Promise.resolve();
  });
};
function press(key: string, shiftKey = false) {
  const event = new KeyboardEvent("keydown", { key, shiftKey, bubbles: true, cancelable: true });
  act(() => {
    (document.activeElement ?? document.body).dispatchEvent(event);
  });
  return event;
}
const panel = (name: string) =>
  document.querySelector<HTMLElement>(`[role="dialog"][aria-label="${name}"]`)!;

// Existing application shape: body portal, aria-modal + data-modal-open,
// its own passive focus/overflow effect and window-capture Escape. No hook.
function Preview({ open, onClose }: { open: boolean; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    ref.current?.focus();
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopImmediatePropagation();
      onClose();
    };
    window.addEventListener("keydown", escape, true);
    return () => {
      window.removeEventListener("keydown", escape, true);
      document.body.style.overflow = previous;
    };
  }, [open, onClose]);
  return open
    ? createPortal(
        <div
          ref={ref}
          tabIndex={-1}
          role="dialog"
          aria-modal="true"
          data-modal-open="true"
          aria-label="Preview"
          style={{ position: "fixed", zIndex: 9999 }}
        >
          <button onClick={onClose}>Close (ESC)</button>
        </div>,
        document.body,
      )
    : null;
}

test("existing external preview stays accessible above a same-render parent and owns focus and Escape", async () => {
  let outerCloses = 0;
  let previewCloses = 0;
  const closePreview = () => {
    previewCloses++;
  };
  const render = (preview: boolean) =>
    act(() =>
      root.render(
        <Modal
          open
          onClose={() => {
            outerCloses++;
          }}
          title="Attachments"
        >
          <input aria-label="Attachment name" />
          <Preview open={preview} onClose={closePreview} />
        </Modal>,
      ),
    );
  render(true);
  await settle();
  const preview = panel("Preview");
  const close = preview.querySelector<HTMLButtonElement>("button")!;
  expect(preview.closest('[inert], [aria-hidden="true"]')).toBeNull();
  close.focus();
  expect(document.activeElement).toBe(close);
  expect(press("Tab").defaultPrevented).toBe(false); // external owns its Tab policy
  expect(preview.contains(document.activeElement)).toBe(true);
  const background = document.createElement("button");
  document.body.appendChild(background);
  background.focus();
  expect(preview.contains(document.activeElement)).toBe(true);
  press("Escape");
  expect(previewCloses).toBe(1);
  expect(outerCloses).toBe(0);
  // onClose can begin an animation instead of immediately unmounting.
  await settle();
  expect(preview.closest('[inert], [aria-hidden="true"]')).toBeNull();
  render(false);
  await settle();
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Attachment name");
  expect(document.body.style.overflow).toBe("hidden");
});

test("removing the underlying Modal leaves an external preview usable and restores page styles after delayed external unmount", async () => {
  document.body.style.overflow = "scroll";
  let closes = 0;
  const onClose = () => {
    closes++;
  };
  const render = (outer: boolean, preview: boolean) =>
    act(() =>
      root.render(
        <>
          <Modal open={outer} onClose={() => {}} title="Attachments">
            <input aria-label="Name" />
          </Modal>
          <Preview open={preview} onClose={onClose} />
        </>,
      ),
    );
  render(true, false);
  render(true, true);
  await settle();
  const preview = panel("Preview");
  const close = preview.querySelector<HTMLButtonElement>("button")!;
  close.focus();
  render(false, true);
  await settle();
  expect(document.activeElement).toBe(close);
  expect(preview.closest('[inert], [aria-hidden="true"]')).toBeNull();
  expect(document.body.style.overflow).toBe("hidden");
  press("Escape");
  expect(closes).toBe(1);
  render(false, false);
  await settle();
  expect(document.body.style.overflow).toBe("scroll");
});

test.each(["unmount", "hidden", "aria-modal", "aria-hidden", "display", "ancestor hidden"])(
  "external lock lease ends on %s after no-op close and repeated Forge reopen, restoring exact styles and disconnecting observers",
  async (end) => {
    // Count live native observers without replacing their delivery semantics.
    const NativeObserver = window.MutationObserver;
    const active = new Set<MutationObserver>();
    class TrackedObserver extends NativeObserver {
      override observe(...args: Parameters<MutationObserver["observe"]>) {
        active.add(this);
        super.observe(...args);
      }
      override disconnect() {
        active.delete(this);
        super.disconnect();
      }
    }
    vi.stubGlobal("MutationObserver", TrackedObserver);
    try {
      document.body.style.setProperty("overscroll-behavior", "contain", "important");
      document.body.style.setProperty("padding-right", "7px", "important");
      document.body.style.setProperty("overflow", "scroll");
      const original = ["overscroll-behavior", "padding-right", "overflow"].map((name) => [
        name,
        document.body.style.getPropertyValue(name),
        document.body.style.getPropertyPriority(name),
      ]);
      const render = (open: boolean) =>
        act(() =>
          root.render(
            <Modal open={open} onClose={() => {}} title="Underlying">
              <input />
            </Modal>,
          ),
        );
      render(true);
      const previousOverflow = document.body.style.overflow;
      const wrapper = document.createElement("div");
      const external = document.createElement("div");
      external.setAttribute("role", "dialog");
      external.setAttribute("aria-modal", "true");
      external.setAttribute("data-modal-open", "true");
      external.style.cssText = "position:fixed;z-index:9999";
      external.innerHTML = "<button>Close external</button>";
      // The independent legacy close action can legitimately be a no-op.
      external.querySelector("button")!.addEventListener("click", () => {});
      wrapper.appendChild(external);
      document.body.appendChild(wrapper);
      document.body.style.overflow = "hidden";
      await settle();
      render(false);
      await settle();
      expect(active.size).toBe(1); // precisely one cooperative lease
      external.querySelector<HTMLButtonElement>("button")!.click();
      await settle();
      expect(document.body.style.overflow).toBe("hidden");
      expect(active.size).toBe(1);
      render(true);
      render(false);
      await settle();
      expect(active.size).toBe(1); // reopening doesn't duplicate the lease
      // Closing need not remove the node. Model the legacy cleanup restoring
      // the value saved while Forge was open ("hidden") in the same turn.
      act(() => {
        if (end === "unmount") external.remove();
        else if (end === "hidden") external.hidden = true;
        else if (end === "aria-modal") external.setAttribute("aria-modal", "false");
        else if (end === "aria-hidden") external.setAttribute("aria-hidden", "true");
        else if (end === "display") external.style.display = "none";
        else wrapper.hidden = true;
        document.body.style.overflow = previousOverflow;
      });
      await settle(); // removal's own event is sufficient: no later DOM writes
      for (const [name, value, priority] of original) {
        expect(document.body.style.getPropertyValue(name!)).toBe(value);
        expect(document.body.style.getPropertyPriority(name!)).toBe(priority);
      }
      expect(active.size).toBe(0);
      render(true);
      expect(document.body.style.overflow).toBe("hidden");
      render(false);
      await settle();
      expect(active.size).toBe(0);
      for (const [name, value, priority] of original) {
        expect(document.body.style.getPropertyValue(name!)).toBe(value);
        expect(document.body.style.getPropertyPriority(name!)).toBe(priority);
      }
    } finally {
      vi.unstubAllGlobals();
    }
  },
);

test("external cleanup during a reopened Forge lease keeps the counted lock until the reopened dialog closes", async () => {
  document.body.style.setProperty("overscroll-behavior", "contain");
  document.body.style.setProperty("overflow", "scroll");
  const render = (open: boolean) =>
    act(() =>
      root.render(
        <Modal open={open} onClose={() => {}} title="Reopened">
          <input />
        </Modal>,
      ),
    );
  render(true);
  const external = document.createElement("div");
  external.setAttribute("role", "dialog");
  external.setAttribute("aria-modal", "true");
  external.style.cssText = "position:fixed;z-index:9999";
  external.innerHTML = "<button>External</button>";
  document.body.appendChild(external);
  await settle();
  render(false);
  render(true);
  act(() => {
    external.setAttribute("aria-modal", "false");
    document.body.style.overflow = "auto"; // external's independent cleanup
  });
  await settle();
  expect(document.body.style.overflow).toBe("hidden");
  expect(document.body.style.getPropertyValue("overscroll-behavior")).toBe("none");
  render(false);
  await settle();
  expect(document.body.style.overflow).toBe("scroll");
  expect(document.body.style.getPropertyValue("overscroll-behavior")).toBe("contain");
});

test("same-subtree external passive cleanup cannot re-lock the page after parent closes", async () => {
  document.body.style.overflow = "scroll";
  document.body.style.setProperty("overscroll-behavior", "contain", "important");
  document.body.style.setProperty("padding-right", "7px", "important");
  const render = (outer: boolean, preview: boolean) =>
    act(() =>
      root.render(
        <Modal open={outer} onClose={() => {}} title="Parent">
          <input aria-label="Parent field" />
          <Preview open={preview} onClose={() => {}} />
        </Modal>,
      ),
    );
  render(true, false);
  render(true, true);
  await settle();
  expect(panel("Preview").contains(document.activeElement)).toBe(true);
  render(false, true); // both portals leave in the same React commit
  await settle();
  expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(0);
  expect(document.body.style.overflow).toBe("scroll");
  expect(document.body.style.getPropertyValue("overscroll-behavior")).toBe("contain");
  expect(document.body.style.paddingRight).toBe("7px");
  expect(document.body.style.getPropertyPriority("padding-right")).toBe("important");
});

test("nested passive settlement never restores over another live Forge owner", async () => {
  document.body.style.overflow = "auto";
  const render = (nested: boolean, survivor: boolean) =>
    act(() =>
      root.render(
        <>
          <Modal open={nested} onClose={() => {}} title="Nested owner">
            <Preview open onClose={() => {}} />
          </Modal>
          <Modal open={survivor} onClose={() => {}} title="Survivor">
            <input aria-label="Survivor field" />
          </Modal>
        </>,
      ),
    );
  render(true, false);
  await settle();
  render(false, true); // new owner enters in the commit that removes legacy subtree
  await settle();
  expect(document.body.style.overflow).toBe("hidden");
  expect(document.querySelector('[role="dialog"]')?.contains(document.activeElement)).toBe(true);
  render(false, false);
  await settle();
  expect(document.body.style.overflow).toBe("auto");
});

test("unrelated lower, hidden or app-inert aria-modal nodes are not exemptions", async () => {
  const lower = document.createElement("div");
  lower.setAttribute("role", "dialog");
  lower.setAttribute("aria-modal", "true");
  lower.style.cssText = "position:fixed;z-index:1";
  lower.innerHTML = "<button>Unrelated</button>";
  document.body.appendChild(lower);
  act(() =>
    root.render(
      <Modal open onClose={() => {}} title="Active">
        <input aria-label="Active field" />
      </Modal>,
    ),
  );
  // CSS isn't loaded in jsdom: give the actual overlay its production level.
  const overlay = host.ownerDocument.querySelector(
    '[role="dialog"][aria-labelledby]',
  )!.parentElement!;
  overlay.style.cssText = "position:fixed;z-index:50";
  // Child-list mutation schedules reconciliation after the modeled CSS change.
  document.body.appendChild(document.createElement("span"));
  await settle();
  expect(lower.getAttribute("inert")).toBe("");
  lower.querySelector<HTMLButtonElement>("button")!.focus();
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Active field");
  lower.style.zIndex = "9999";
  lower.hidden = true;
  await settle();
  lower.querySelector<HTMLButtonElement>("button")!.focus();
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Active field");
  const inert = document.createElement("div");
  inert.setAttribute("role", "dialog");
  inert.setAttribute("aria-modal", "true");
  inert.setAttribute("inert", "preexisting");
  inert.style.cssText = "position:fixed;z-index:10000";
  inert.innerHTML = "<button>Inert external</button>";
  document.body.appendChild(inert);
  await settle();
  inert.querySelector<HTMLButtonElement>("button")!.focus();
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Active field");
  expect(inert.getAttribute("inert")).toBe("");
  act(() => root.render(null));
  expect(inert.getAttribute("inert")).toBe("preexisting");
});

test("an external preview with a bubble Escape listener is not dismissed together with its underlying Modal", async () => {
  let outerCloses = 0;
  let previewCloses = 0;
  act(() =>
    root.render(
      <Modal
        open
        onClose={() => {
          outerCloses++;
        }}
        title="Outer"
      >
        <input />
      </Modal>,
    ),
  );
  const preview = document.createElement("div");
  preview.setAttribute("role", "dialog");
  preview.setAttribute("aria-modal", "true");
  preview.style.cssText = "position:fixed;z-index:9999";
  preview.innerHTML = "<button>Close external</button>";
  preview.addEventListener("keydown", (event) => {
    if (event.key === "Escape") previewCloses++;
  });
  document.body.appendChild(preview);
  await settle();
  preview.querySelector<HTMLButtonElement>("button")!.focus();
  press("Escape");
  expect(previewCloses).toBe(1);
  expect(outerCloses).toBe(0);
  preview.remove();
  await settle();
  expect(document.querySelector('[role="dialog"]')?.contains(document.activeElement)).toBe(true);
});

test("same-render nested registered Modal stays above parent despite child-first layout effects", async () => {
  let outerCloses = 0;
  let innerCloses = 0;
  const render = (inner: boolean) =>
    act(() =>
      root.render(
        <Modal
          open
          onClose={() => {
            outerCloses++;
          }}
          title="Parent"
        >
          <input aria-label="Parent field" />
          <Modal
            open={inner}
            onClose={() => {
              innerCloses++;
            }}
            title="Child"
          >
            <input aria-label="Child field" />
          </Modal>
        </Modal>,
      ),
    );
  render(true);
  await settle();
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Child field");
  press("Escape");
  expect(innerCloses).toBe(1);
  expect(outerCloses).toBe(0);
  const child = document
    .querySelector<HTMLInputElement>('input[aria-label="Child field"]')!
    .closest('[role="dialog"]')!;
  const parent = document
    .querySelector<HTMLInputElement>('input[aria-label="Parent field"]')!
    .closest('[role="dialog"]')!;
  expect(Number((child.parentElement as HTMLElement).style.zIndex)).toBeGreaterThan(
    Number((parent.parentElement as HTMLElement).style.zIndex),
  );
  render(false);
  await settle();
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Parent field");
  expect(document.body.style.overflow).toBe("hidden");
});
