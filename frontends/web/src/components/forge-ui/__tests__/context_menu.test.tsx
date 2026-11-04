/**
 * forge's context_menu (dropdown_menu anchored to a point), as this app
 * relies on it: what ui/ContextMenu did for the sidebar and the file tree
 * (open at the pointer, a press outside or Escape closes, choosing runs the
 * item and closes, disabled items do nothing) plus the menu contract R5
 * gained (research/forge-migration/13-p9-frontend-plan.md §6): roles, the
 * keyboard, focus handed back, opening from the keyboard.
 */
import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";

import ContextMenu from "../context_menu";
import type { MenuGroup } from "../dropdown_menu";

function press(key: string, init: KeyboardEventInit = {}) {
  act(() => {
    (document.activeElement ?? document.body).dispatchEvent(
      new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }),
    );
  });
}

const groups = (onRename: () => void = vi.fn(), onDelete: () => void = vi.fn()): MenuGroup[] => [
  {
    items: [
      { label: "Rename", onClick: onRename },
      { label: "Duplicate", onClick: vi.fn(), disabled: true },
    ],
  },
  { items: [{ label: "Delete", onClick: onDelete, variant: "danger" }] },
];

/** One menu for a list, driven by state, the way the sidebar uses it. */
function Driven({ onRename = vi.fn() }: { onRename?: () => void }) {
  const [at, setAt] = useState<{ x: number; y: number } | null>(null);
  return (
    <>
      <input aria-label="Composer" />
      <button
        type="button"
        onContextMenu={(e) => {
          e.preventDefault();
          setAt({ x: e.clientX, y: e.clientY });
        }}
      >
        Row
      </button>
      <ContextMenu
        label="Row actions"
        groups={groups(onRename)}
        open={at !== null}
        position={at ?? undefined}
        onOpenChange={(open) => !open && setAt(null)}
      />
    </>
  );
}

describe("ContextMenu", () => {
  it("opens a named menu where the region is right-clicked, and suppresses the browser's", () => {
    render(
      <ContextMenu label="File actions" groups={groups()}>
        <div>main.go</div>
      </ContextMenu>,
    );
    const event = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 120, clientY: 80 });
    act(() => {
      screen.getByText("main.go").dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(true);
    const menu = screen.getByRole("menu", { name: "File actions" });
    expect(menu.style.left).toBe("120px");
    expect(menu.style.top).toBe("80px");
    expect(screen.getAllByRole("group")).toHaveLength(2);
    expect(screen.getByRole("separator")).toBeInTheDocument();
  });

  it("flips left and up when the point is near the viewport's edge", () => {
    render(
      <ContextMenu label="File actions" groups={groups()} open position={{ x: window.innerWidth - 4, y: window.innerHeight - 4 }} />,
    );
    const menu = screen.getByRole("menu");
    // jsdom lays nothing out (0×0), so the flip lands on the point itself
    // and the clamp keeps it 8px inside the viewport.
    expect(parseFloat(menu.style.left)).toBeLessThanOrEqual(window.innerWidth - 8);
    expect(parseFloat(menu.style.top)).toBeLessThanOrEqual(window.innerHeight - 8);
  });

  it("runs the chosen item, closes, and hands focus back to what had it", () => {
    const onRename = vi.fn();
    render(<Driven onRename={onRename} />);
    const composer = screen.getByRole("textbox", { name: "Composer" });
    act(() => composer.focus());
    fireEvent.contextMenu(screen.getByRole("button", { name: "Row" }), { clientX: 10, clientY: 10 });
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    expect(onRename).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("menu")).toBeNull();
    expect(composer).toHaveFocus();
  });

  it("ignores a disabled item", () => {
    render(<Driven />);
    fireEvent.contextMenu(screen.getByRole("button", { name: "Row" }), { clientX: 10, clientY: 10 });
    const item = screen.getByRole("menuitem", { name: "Duplicate" });
    expect(item).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(item);
    expect(screen.getByRole("menu")).toBeInTheDocument();
  });

  it("closes on Escape, returning focus, and on a press outside", () => {
    render(<Driven />);
    const row = screen.getByRole("button", { name: "Row" });
    act(() => row.focus());
    fireEvent.contextMenu(row, { clientX: 10, clientY: 10 });
    press("Escape");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(row).toHaveFocus();

    fireEvent.contextMenu(row, { clientX: 10, clientY: 10 });
    fireEvent.pointerDown(document.body);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("moves between enabled items with the arrow keys", () => {
    render(<Driven />);
    fireEvent.contextMenu(screen.getByRole("button", { name: "Row" }), { clientX: 10, clientY: 10 });
    press("ArrowDown");
    expect(screen.getByRole("menuitem", { name: "Rename" })).toHaveFocus();
    press("ArrowDown");
    expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveFocus();
    press("ArrowDown");
    expect(screen.getByRole("menuitem", { name: "Rename" })).toHaveFocus();
  });

  it("opens from the keyboard (Shift+F10) at the focused element, on the first item", () => {
    render(
      <ContextMenu label="File actions" groups={groups()}>
        <button type="button">main.go</button>
      </ContextMenu>,
    );
    const file = screen.getByRole("button", { name: "main.go" });
    act(() => file.focus());
    // A keyboard-opened contextmenu carries no pointer position.
    act(() => {
      file.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, detail: 0 }));
    });
    expect(screen.getByRole("menuitem", { name: "Rename" })).toHaveFocus();
  });

  it("opens only the innermost of nested regions (a folder inside a folder)", () => {
    render(
      <ContextMenu label="Folder actions" groups={groups()}>
        <div>
          src
          <ContextMenu label="File actions" groups={groups()}>
            <div>main.go</div>
          </ContextMenu>
        </div>
      </ContextMenu>,
    );
    act(() => {
      screen.getByText("main.go").dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    expect(screen.getAllByRole("menu")).toHaveLength(1);
    expect(screen.getByRole("menu", { name: "File actions" })).toBeInTheDocument();
  });

  it("leaves the browser's menu alone when disabled", () => {
    render(
      <ContextMenu label="File actions" groups={groups()} disabled>
        <div>main.go</div>
      </ContextMenu>,
    );
    const event = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    act(() => {
      screen.getByText("main.go").dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(false);
    expect(screen.queryByRole("menu")).toBeNull();
  });
});
