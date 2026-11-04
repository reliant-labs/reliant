/**
 * forge's dropdown_menu and popover, as this app relies on them: the cases
 * ui/Dropdown's own test pinned (portal, uncontrolled toggle, controlled
 * trigger, outside press) plus the menu-button keyboard contract the S2
 * migration gained (research/forge-migration/13-p9-frontend-plan.md §6).
 */
import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";

import DropdownMenu from "../dropdown_menu";
import Popover from "../popover";
import Modal from "../modal";

function press(key: string, init: KeyboardEventInit = {}) {
  act(() => {
    (document.activeElement ?? document.body).dispatchEvent(
      new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }),
    );
  });
}

const actions = (onDelete = vi.fn()) => [
  {
    items: [
      { label: "Duplicate", onClick: vi.fn() },
      { label: "Rename", onClick: vi.fn() },
      { label: "Delete", onClick: onDelete, variant: "danger" as const },
    ],
  },
];

describe("DropdownMenu", () => {
  it("portals its menu out of a clipping ancestor", () => {
    const { container } = render(
      <div className="overflow-hidden">
        <DropdownMenu trigger="Actions" groups={actions()} />
      </div>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Actions" }));
    expect(container).not.toContainElement(screen.getByRole("menu"));
  });

  it("toggles from its trigger and names the menu after it", () => {
    render(<DropdownMenu trigger="Actions" groups={actions()} />);
    const trigger = screen.getByRole("button", { name: "Actions" });
    fireEvent.click(trigger);
    expect(screen.getByRole("menu", { name: "Actions" })).toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(trigger);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("leaves a controlled menu's open state to the caller", () => {
    const onOpenChange = vi.fn();
    render(<DropdownMenu trigger="Actions" groups={actions()} open={false} onOpenChange={onOpenChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Actions" }));
    expect(onOpenChange).toHaveBeenCalledWith(true);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("closes on a press outside the trigger and the menu", () => {
    render(<DropdownMenu trigger="Actions" groups={actions()} />);
    fireEvent.click(screen.getByRole("button", { name: "Actions" }));
    fireEvent.pointerDown(screen.getByRole("menuitem", { name: "Rename" }));
    expect(screen.getByRole("menu")).toBeInTheDocument();
    fireEvent.pointerDown(document.body);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("opens from the keyboard on the first item and moves with the arrows", () => {
    render(<DropdownMenu trigger="Actions" groups={actions()} />);
    const trigger = screen.getByRole("button", { name: "Actions" });
    trigger.focus();
    act(() => {
      fireEvent.keyDown(trigger, { key: "ArrowDown" });
    });
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Duplicate" }));
    press("ArrowDown");
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
    press("End");
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Delete" }));
    press("ArrowDown");
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Duplicate" }));
    press("r");
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
  });

  it("closes on Escape with focus back on the trigger, and keeps an enclosing dialog open", () => {
    const onCloseDialog = vi.fn();
    render(
      <Modal open onClose={onCloseDialog} title="Row">
        <DropdownMenu trigger="Actions" groups={actions()} />
      </Modal>,
    );
    const trigger = screen.getByRole("button", { name: "Actions" });
    trigger.focus();
    act(() => {
      fireEvent.keyDown(trigger, { key: "ArrowDown" });
    });
    press("Escape");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(trigger);
    expect(onCloseDialog).not.toHaveBeenCalled();
  });

  it("runs an item after closing and refocusing the trigger", () => {
    let focusedWhenRun: Element | null = null;
    const onDelete = vi.fn(() => {
      focusedWhenRun = document.activeElement;
    });
    render(<DropdownMenu trigger="Actions" groups={actions(onDelete)} />);
    const trigger = screen.getByRole("button", { name: "Actions" });
    fireEvent.click(trigger);
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    expect(onDelete).toHaveBeenCalledTimes(1);
    expect(focusedWhenRun).toBe(trigger);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("renders radio and checkbox groups, and keeps a multi-select open", () => {
    function Filter() {
      const [picked, setPicked] = useState<string[]>(["failed"]);
      return (
        <DropdownMenu
          trigger="State"
          groups={[
            {
              selection: "multiple",
              items: ["failed", "live"].map((v) => ({
                label: v,
                checked: picked.includes(v),
                keepOpen: true,
                onClick: () => setPicked((p) => (p.includes(v) ? p.filter((x) => x !== v) : [...p, v])),
              })),
            },
            { selection: "single", items: [{ label: "Newest", checked: true }] },
          ]}
        />
      );
    }
    render(<Filter />);
    fireEvent.click(screen.getByRole("button", { name: "State" }));
    expect(screen.getByRole("menuitemcheckbox", { name: "failed" })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("menuitemradio", { name: "Newest" })).toHaveAttribute("aria-checked", "true");
    fireEvent.click(screen.getByRole("menuitemcheckbox", { name: "live" }));
    expect(screen.getByRole("menuitemcheckbox", { name: "live" })).toHaveAttribute("aria-checked", "true");
  });

  it("spreads its trigger props on a render-prop trigger", () => {
    render(
      <DropdownMenu
        label="More actions"
        trigger={(props) => (
          <button type="button" {...props} data-testid="mine" aria-label="More actions">
            …
          </button>
        )}
        groups={actions()}
      />,
    );
    const trigger = screen.getByTestId("mine");
    expect(trigger).toHaveAttribute("aria-haspopup", "menu");
    fireEvent.click(trigger);
    expect(screen.getByRole("menu", { name: "More actions" })).toBeInTheDocument();
  });

  it("shows its empty message when every group is empty", () => {
    render(<DropdownMenu trigger="Projects" groups={[{ items: [] }]} emptyMessage="No projects yet." />);
    fireEvent.click(screen.getByRole("button", { name: "Projects" }));
    expect(screen.getByText("No projects yet.")).toBeInTheDocument();
  });
});

describe("Popover", () => {
  it("is a dialog that holds controls, and closes when focus leaves it", () => {
    render(
      <>
        <Popover trigger="Ports" label="Detected ports">
          <button type="button">Make public</button>
        </Popover>
        <button type="button">Elsewhere</button>
      </>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Ports" }));
    const panel = screen.getByRole("dialog", { name: "Detected ports" });
    const inner = screen.getByRole("button", { name: "Make public" });
    inner.focus();
    expect(panel).toContainElement(inner);
    act(() => {
      screen.getByRole("button", { name: "Elsewhere" }).focus();
    });
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
