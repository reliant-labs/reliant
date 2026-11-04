/**
 * Modal a11y parity: the behaviours a dialog in this app must have, run
 * against every Modal implementation the app has used.
 *
 * Written against reliant's own ui/Modal first (what its 56 call sites relied
 * on), then run against forge-ui/modal before those sites moved to it (R5 S2,
 * research/forge-migration/13-p9-frontend-plan.md §6). Each gap forge's modal
 * had was fixed IN FORGE (forge#1227) rather than worked around here. An
 * implementation's known gaps are asserted as failing (`gaps`), so the list
 * stays honest — a gap that closes turns its `it.fails` red.
 *
 * The dispatcher cases mount the app's real keyboard dispatcher the way
 * useAppKeyboardShortcuts does (one capture-phase listener on document), so
 * "Escape closes the dialog" is proven in the workspace, where the
 * dispatcher owns Escape, and not only on a bare page.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useState, type ReactNode } from "react";

import ForgeModal from "../modal";
import { ImagePreviewModal } from "../../Chat/ImagePreviewModal";
import { defaultShortcuts } from "../../../store/shortcutsData.generated";
import { parseBinding } from "../../../lib/keyboard/chord";
import { ShortcutRegistry, type ResolvedShortcut } from "../../../lib/keyboard/registry";
import { createDispatcher } from "../../../lib/keyboard/dispatcher";

const fallback = vi.hoisted(() => vi.fn());
vi.mock("../../../hooks/useFocusManager", () => ({ focusChatInput: fallback }));

interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
}

interface Impl {
  name: string;
  Dialog: (props: DialogProps) => ReactNode;
  /** Behaviours this implementation is known not to have. */
  gaps: ReadonlySet<string>;
}

const IMPLS: Impl[] = [
  // reliant's ui/Modal ran here too until R5 S2 moved its 56 call sites onto
  // forge's and deleted it. Its gaps, for the record: no Tab trap; both of two
  // stacked dialogs closed on Escape; an Escape an inner control had handled
  // still closed it; a stacked dialog closing unlocked the page; it "restored"
  // focus to <body> when the opener unmounted in the same render.
  {
    name: "forge-ui/modal",
    Dialog: (props) => <ForgeModal size="md" fallbackFocus={fallback} {...props} />,
    gaps: new Set(),
  },
];

function buildRegistry(): ShortcutRegistry {
  const resolved: ResolvedShortcut[] = Object.values(defaultShortcuts).map((shortcut) => ({
    id: shortcut.id,
    handler: shortcut.handler,
    binding: parseBinding(shortcut.defaultBinding, true),
    context: shortcut.context,
    allowInInput: shortcut.allowInInput,
    passthrough: shortcut.passthrough,
  }));
  return new ShortcutRegistry(resolved);
}

const teardown: Array<() => void> = [];

/** The app's dispatcher, mounted as useAppKeyboardShortcuts mounts it. */
function mountDispatcher(): { fired: string[] } {
  const fired: string[] = [];
  const dispatcher = createDispatcher({
    registry: buildRegistry(),
    getHandler: (name) => () => fired.push(name),
  });
  const onKeyDown = (event: KeyboardEvent) => dispatcher.handleKeyDown(event);
  document.addEventListener("keydown", onKeyDown, true);
  teardown.push(() => document.removeEventListener("keydown", onKeyDown, true));
  return { fired };
}

function press(key: string, init: KeyboardEventInit = {}) {
  act(() => {
    (document.activeElement ?? document.body).dispatchEvent(
      new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }),
    );
  });
}

beforeEach(() => fallback.mockReset());

afterEach(() => {
  while (teardown.length) teardown.pop()!();
  document.body.style.overflow = "";
});

describe.each(IMPLS)("$name", ({ Dialog, gaps }) => {
  const check = (name: string, fn: () => void) => (gaps.has(name) ? it.fails(name, fn) : it(name, fn));

  function Form({ onClose = () => undefined, footer }: { onClose?: () => void; footer?: ReactNode }) {
    return (
      <Dialog open onClose={onClose} title="Rename environment" footer={footer}>
        <label htmlFor="parity-name">Name</label>
        <input id="parity-name" />
        <button type="button">Save</button>
      </Dialog>
    );
  }

  check("renders nothing while closed", () => {
    render(
      <Dialog open={false} onClose={() => undefined} title="Hidden">
        <p>body</p>
      </Dialog>,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  check("is a modal dialog named by its title", () => {
    render(<Form />);
    const dialog = screen.getByRole("dialog", { name: "Rename environment" });
    expect(dialog).toHaveAttribute("aria-modal", "true");
  });

  check("portals out of a clipping, transformed ancestor", () => {
    const { container } = render(
      <div style={{ overflow: "hidden", transform: "translateZ(0)" }}>
        <Form />
      </div>,
    );
    expect(container).not.toContainElement(screen.getByRole("dialog"));
  });

  check("moves focus to the first field, never the close button", () => {
    render(<Form />);
    expect(document.activeElement).toBe(screen.getByLabelText("Name"));
  });

  check("traps Tab inside the dialog", () => {
    render(<Form />);
    const dialog = screen.getByRole("dialog");
    const close = within(dialog).getByRole("button", { name: /close/i });
    const save = within(dialog).getByRole("button", { name: "Save" });
    // Save is the last control and the header's close button the first, so
    // Tab from one wraps to the other (jsdom moves no focus on its own).
    save.focus();
    press("Tab");
    expect(document.activeElement).toBe(close);
    press("Tab", { shiftKey: true });
    expect(document.activeElement).toBe(save);
  });

  check("closes on Escape", () => {
    const onClose = vi.fn();
    render(<Form onClose={onClose} />);
    press("Escape");
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  check("leaves Escape to an inner control that handled it", () => {
    const onClose = vi.fn();
    render(
      <Dialog open onClose={onClose} title="With a menu">
        {/* An open menu inside the dialog claims Escape for itself. */}
        <input aria-label="combobox" onKeyDown={(e) => e.key === "Escape" && e.preventDefault()} />
      </Dialog>,
    );
    press("Escape");
    expect(onClose).not.toHaveBeenCalled();
  });

  check("closes from the overlay but not from a click inside", () => {
    const onClose = vi.fn();
    render(<Form onClose={onClose} />);
    fireEvent.click(screen.getByLabelText("Name"));
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.click(document.querySelector<HTMLElement>('[role="presentation"]')!);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  check("closes from its close button", () => {
    const onClose = vi.fn();
    render(<Form onClose={onClose} />);
    fireEvent.click(screen.getByRole("button", { name: /close/i }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  check("keeps the footer outside the scrolling body", () => {
    render(<Form footer={<button type="button">Turn on</button>} />);
    const footer = screen.getByTestId("modal-footer");
    expect(footer).toContainElement(screen.getByRole("button", { name: "Turn on" }));
    expect(footer.previousElementSibling).toContainElement(screen.getByLabelText("Name"));
  });

  check("returns focus to the opener on close", async () => {
    function Opener() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            Open
          </button>
          <Dialog open={open} onClose={() => setOpen(false)} title="Dialog">
            <input aria-label="field" />
          </Dialog>
        </>
      );
    }
    render(<Opener />);
    const opener = screen.getByRole("button", { name: "Open" });
    opener.focus();
    fireEvent.click(opener);
    expect(document.activeElement).toBe(screen.getByLabelText("field"));
    press("Escape");
    expect(document.activeElement).toBe(opener);
  });

  check("falls back when the opener is gone", async () => {
    function Host() {
      const [open, setOpen] = useState(false);
      const [menu, setMenu] = useState(true);
      return (
        <>
          {menu && (
            <button
              type="button"
              onClick={() => {
                setMenu(false);
                setOpen(true);
              }}
            >
              Rename…
            </button>
          )}
          <Dialog open={open} onClose={() => setOpen(false)} title="Dialog">
            <input aria-label="field" />
          </Dialog>
        </>
      );
    }
    render(<Host />);
    const item = screen.getByRole("button", { name: "Rename…" });
    item.focus();
    fireEvent.click(item);
    press("Escape");
    await new Promise((resolve) => requestAnimationFrame(resolve));
    expect(fallback).toHaveBeenCalled();
  });

  check("locks page scroll while open and restores it on close", () => {
    document.body.style.overflow = "auto";
    const { rerender } = render(<Form />);
    expect(document.body.style.overflow).toBe("hidden");
    rerender(<div />);
    expect(["auto", "unset", ""]).toContain(document.body.style.overflow);
    expect(document.body.style.overflow).not.toBe("hidden");
  });

  check("closes only the top dialog when two are stacked", () => {
    const outer = vi.fn();
    const inner = vi.fn();
    render(
      <>
        <Dialog open onClose={outer} title="Outer">
          <input aria-label="outer field" />
        </Dialog>
        <Dialog open onClose={inner} title="Inner">
          <input aria-label="inner field" />
        </Dialog>
      </>,
    );
    press("Escape");
    expect(inner).toHaveBeenCalledTimes(1);
    expect(outer).not.toHaveBeenCalled();
  });

  check("keeps the page locked after a stacked dialog closes", () => {
    function Host({ inner }: { inner: boolean }) {
      return (
        <>
          <Dialog open onClose={() => undefined} title="Outer">
            <input aria-label="outer field" />
          </Dialog>
          <Dialog open={inner} onClose={() => undefined} title="Inner">
            <input aria-label="inner field" />
          </Dialog>
        </>
      );
    }
    const { rerender } = render(<Host inner />);
    rerender(<Host inner={false} />);
    expect(document.body.style.overflow).toBe("hidden");
  });

  check("does not pull focus out of a preview stacked above it", () => {
    render(
      <Dialog open onClose={() => undefined} title="Attachments">
        <input aria-label="attachment name" />
        <ImagePreviewModal isOpen onClose={() => undefined} imageUrl="blob:x" filename="shot.png" />
      </Dialog>,
    );
    const close = screen.getByRole("button", { name: "Close (ESC)" });
    close.focus();
    press("Tab");
    expect(screen.getByRole("dialog", { name: "shot.png" })).toContainElement(document.activeElement as HTMLElement);
  });

  check("lets a nested image preview take Escape first", () => {
    const outer = vi.fn();
    const preview = vi.fn();
    render(
      <Dialog open onClose={outer} title="Attachments">
        <ImagePreviewModal isOpen onClose={preview} imageUrl="blob:x" filename="shot.png" />
      </Dialog>,
    );
    press("Escape");
    expect(preview).toHaveBeenCalledTimes(1);
    expect(outer).not.toHaveBeenCalled();
  });

  check("keeps the page locked after a nested preview closes", () => {
    function Host({ preview }: { preview: boolean }) {
      return (
        <Dialog open onClose={() => undefined} title="Attachments">
          <ImagePreviewModal isOpen={preview} onClose={() => undefined} imageUrl="blob:x" filename="shot.png" />
        </Dialog>
      );
    }
    // The preview opens over the dialog, after it: the order its scroll
    // lock is taken in is the point.
    const { rerender } = render(<Host preview={false} />);
    rerender(<Host preview />);
    rerender(<Host preview={false} />);
    expect(document.body.style.overflow).toBe("hidden");
  });

  check("closes on Escape with the app's keyboard dispatcher live", () => {
    const { fired } = mountDispatcher();
    const onClose = vi.fn();
    render(<Form onClose={onClose} />);
    press("Escape");
    expect(onClose).toHaveBeenCalledTimes(1);
    // ...and the chat behind it keeps streaming.
    expect(fired).not.toContain("onStopStreaming");
  });
});
