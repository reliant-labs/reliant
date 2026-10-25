/**
 * forge's tooltip, as this app relies on it: the cases ui/Tooltip's own test
 * pinned (a long hint wraps, a disabled button still explains itself) plus
 * the WAI-ARIA tooltip contract R5 gained (research/forge-migration/
 * 13-p9-frontend-plan.md §6): an always-present description, keyboard-only
 * focus showing, Escape, hoverable, one visible at a time.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";

import Tooltip from "../tooltip";

function hover(el: Element, init: { pointerType?: string } = {}) {
  act(() => {
    const event = new Event("pointerenter", { bubbles: false });
    Object.assign(event, { pointerType: init.pointerType ?? "mouse" });
    el.dispatchEvent(event);
  });
}

function unhover(el: Element) {
  act(() => {
    el.dispatchEvent(new Event("pointerleave", { bubbles: false }));
  });
}

function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

describe("Tooltip", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("describes its trigger whether or not the bubble shows", () => {
    render(
      <Tooltip content="Save the workflow">
        <button>Save</button>
      </Tooltip>,
    );
    const button = screen.getByRole("button", { name: "Save" });
    expect(button).toHaveAccessibleDescription("Save the workflow");
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("names an icon-only trigger with type=label", () => {
    render(
      <Tooltip content="Copy" type="label">
        {(props) => (
          <button {...props}>
            <svg aria-hidden />
          </button>
        )}
      </Tooltip>,
    );
    expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
  });

  it("shows after the hover delay and hides when the pointer leaves", () => {
    render(
      <Tooltip content="Rename" delay={300}>
        <button>Edit</button>
      </Tooltip>,
    );
    const wrapper = screen.getByRole("button", { name: "Edit" }).parentElement!;
    hover(wrapper);
    advance(299);
    expect(screen.queryByRole("tooltip")).toBeNull();
    advance(1);
    expect(screen.getByRole("tooltip")).toHaveTextContent("Rename");
    unhover(wrapper);
    advance(200);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("stays open while the pointer crosses onto the bubble (hoverable)", () => {
    render(
      <Tooltip content="A long explanation" delay={0}>
        <button>Why</button>
      </Tooltip>,
    );
    const wrapper = screen.getByRole("button", { name: "Why" }).parentElement!;
    hover(wrapper);
    advance(0);
    const bubble = screen.getByRole("tooltip");
    unhover(wrapper);
    hover(bubble);
    advance(500);
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    unhover(bubble);
    advance(200);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("never shows for a touch", () => {
    render(
      <Tooltip content="Hint" delay={0}>
        <button>Tap</button>
      </Tooltip>,
    );
    hover(screen.getByRole("button").parentElement!, { pointerType: "touch" });
    advance(1000);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("still shows for a disabled button, and makes the wrapper reachable from the keyboard", () => {
    render(
      <Tooltip content="Save the workflow first" delay={0}>
        <button disabled>Add</button>
      </Tooltip>,
    );
    const wrapper = screen.getByText("Add").parentElement!;
    expect(wrapper).toHaveAttribute("tabindex", "0");
    expect(wrapper).toHaveAccessibleDescription("Save the workflow first");
    hover(wrapper);
    advance(0);
    expect(screen.getByRole("tooltip")).toHaveTextContent("Save the workflow first");
  });

  it("leaves a wrapper around a focusable control out of the tab order", () => {
    render(
      <Tooltip content="Hint">
        <button>Go</button>
      </Tooltip>,
    );
    expect(screen.getByRole("button", { name: "Go" }).parentElement).not.toHaveAttribute("tabindex");
  });

  it("shows on keyboard focus at once, hides on blur", () => {
    render(
      <Tooltip content="This is a deliberately long tooltip that explains why the control is disabled">
        <button>Go</button>
      </Tooltip>,
    );
    const button = screen.getByRole("button", { name: "Go" });
    act(() => button.focus());
    expect(screen.getByRole("tooltip")).toHaveClass("max-w-[min(20rem,calc(100vw-16px))]");
    act(() => button.blur());
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("hides on Escape and claims the key", () => {
    const behind = vi.fn();
    window.addEventListener("keydown", behind);
    render(
      <Tooltip content="Hint" delay={0}>
        <button>Go</button>
      </Tooltip>,
    );
    hover(screen.getByRole("button").parentElement!);
    advance(0);
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(screen.queryByRole("tooltip")).toBeNull();
    expect(behind).not.toHaveBeenCalled();
    // Hidden, it no longer takes Escape.
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(behind).toHaveBeenCalledTimes(1);
    window.removeEventListener("keydown", behind);
  });

  it("hides when its trigger is pressed and stays hidden until the pointer leaves", () => {
    render(
      <Tooltip content="Hint" delay={0}>
        <button>Go</button>
      </Tooltip>,
    );
    const wrapper = screen.getByRole("button").parentElement!;
    hover(wrapper);
    advance(0);
    fireEvent.pointerDown(screen.getByRole("button"));
    expect(screen.queryByRole("tooltip")).toBeNull();
    hover(wrapper);
    advance(1000);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("never shows over the trigger's own open menu", () => {
    render(
      <Tooltip content="More actions" delay={0}>
        <button aria-expanded="true">More</button>
      </Tooltip>,
    );
    hover(screen.getByRole("button").parentElement!);
    advance(0);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("shows one tooltip at a time, the next with no delay", () => {
    render(
      <>
        <Tooltip content="First" delay={500}>
          <button>A</button>
        </Tooltip>
        <Tooltip content="Second" delay={500}>
          <button>B</button>
        </Tooltip>
      </>,
    );
    const a = screen.getByRole("button", { name: "A" }).parentElement!;
    const b = screen.getByRole("button", { name: "B" }).parentElement!;
    hover(a);
    advance(500);
    expect(screen.getByRole("tooltip")).toHaveTextContent("First");
    unhover(a);
    hover(b);
    advance(0);
    expect(screen.getAllByRole("tooltip")).toHaveLength(1);
    expect(screen.getByRole("tooltip")).toHaveTextContent("Second");
  });

  it("portals out of a clipping ancestor and shows a shortcut", () => {
    const { container } = render(
      <div className="overflow-hidden">
        <Tooltip content="Search" shortcut="⌘K" delay={0}>
          <button>Find</button>
        </Tooltip>
      </div>,
    );
    hover(screen.getByRole("button").parentElement!);
    advance(0);
    const tip = screen.getByRole("tooltip");
    expect(container).not.toContainElement(tip);
    expect(tip.querySelector("kbd")).toHaveTextContent("⌘K");
    expect(screen.getByRole("button", { name: "Find" })).toHaveAccessibleDescription("Search (⌘K)");
  });

  it("renders the trigger untouched when disabled or empty", () => {
    render(
      <>
        <Tooltip content="" delay={0}>
          <button>Empty</button>
        </Tooltip>
        <Tooltip content="Hint" disabled delay={0}>
          <button>Off</button>
        </Tooltip>
      </>,
    );
    for (const name of ["Empty", "Off"]) {
      const button = screen.getByRole("button", { name });
      expect(button).not.toHaveAttribute("aria-describedby");
      hover(button.parentElement!);
    }
    advance(1000);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });
});
