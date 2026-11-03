/**
 * forge's popover, as this app relies on it. Dismissal treats the panel's
 * REACT tree as inside: a control in the panel may portal its own layer to
 * <body> (the touch PickerSheet a Chat Settings param opens), and pressing or
 * focusing in that layer must not close the panel that owns it — closing it
 * would unmount the very picker being used. forge pins the source
 * (pkg/components/library_popover_test.go); the behaviour is tested here.
 */
import { describe, expect, it } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { createPortal } from "react-dom";

import Popover from "../popover";

function Harness() {
  const [open, setOpen] = useState(true);
  return (
    <>
      <button type="button">Elsewhere</button>
      <Popover trigger="Open" open={open} onOpenChange={setOpen} label="Settings">
        <div>
          <button type="button">In panel</button>
          {createPortal(
            <div>
              <button type="button">In portalled sheet</button>
            </div>,
            document.body,
          )}
        </div>
      </Popover>
    </>
  );
}

describe("Popover dismissal", () => {
  it("stays open for a press in a layer its content portals, and closes for one outside", () => {
    render(<Harness />);
    fireEvent.pointerDown(screen.getByRole("button", { name: "In portalled sheet" }));
    expect(screen.getByRole("dialog", { name: "Settings" })).toBeInTheDocument();
    fireEvent.pointerDown(screen.getByRole("button", { name: "Elsewhere" }));
    expect(screen.queryByRole("dialog", { name: "Settings" })).toBeNull();
  });

  it("stays open when focus moves into a portalled layer, and closes when it leaves for the page", async () => {
    render(<Harness />);
    const inPanel = screen.getByRole("button", { name: "In panel" });
    act(() => inPanel.focus());
    act(() => screen.getByRole("button", { name: "In portalled sheet" }).focus());
    // The close is decided after focusin; give it the tick it waits for.
    await act(() => new Promise((r) => setTimeout(r, 10)));
    expect(screen.getByRole("dialog", { name: "Settings" })).toBeInTheDocument();

    act(() => inPanel.focus());
    act(() => screen.getByRole("button", { name: "Elsewhere" }).focus());
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Settings" })).toBeNull());
  });
});
