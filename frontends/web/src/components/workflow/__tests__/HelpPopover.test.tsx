import { describe, expect, it } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";

import { HelpPopover } from "../HelpPopover";

describe("HelpPopover", () => {
  it("opens a dialog named after the field, keeping the help's line breaks", () => {
    render(<HelpPopover title="Inject message" content={"First line.\nSecond line."} />);
    const trigger = screen.getByRole("button", { name: "Help: Inject message" });
    expect(trigger).toHaveAttribute("aria-haspopup", "dialog");
    fireEvent.click(trigger);
    const dialog = screen.getByRole("dialog", { name: "Inject message" });
    expect(dialog).toHaveTextContent("First line.");
    expect(dialog.querySelector(".whitespace-pre-line")).not.toBeNull();
    expect(trigger).toHaveAttribute("aria-expanded", "true");
  });

  it("closes from its close button and on Escape, focus back on the icon", () => {
    render(<HelpPopover content="Help text" />);
    const trigger = screen.getByRole("button", { name: "Help" });
    fireEvent.click(trigger);
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(trigger).toHaveFocus();

    fireEvent.click(trigger);
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(trigger).toHaveFocus();
  });
});
