import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AddMachineSheet } from "./AddMachineSheet.demo";

// Fail even if a future substitute renders a real provisioning, pairing or
// checkout surface without completing it (and hence without calling onReady).
vi.mock("@/components/Settings/cloud/createMachine", () => ({
  createMachine: vi.fn(() => { throw new Error("demo attempted provisioning"); }),
}));
vi.mock("./PairMachinePanel", () => ({
  PairMachinePanel: () => { throw new Error("demo attempted pairing"); },
}));
vi.mock("@/components/Billing/ComputeSubscriptionCheckout", () => ({
  ComputeSubscriptionCheckout: () => { throw new Error("demo attempted checkout"); },
}));

import { createMachine } from "@/components/Settings/cloud/createMachine";

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("the public demo's add-machine sheet", () => {
  it.each(["cloud", "self"] as const)("is Close-only even when opened for %s", (initialKind) => {
    const onClose = vi.fn();
    const onReady = vi.fn();
    const props = { open: false, onClose, onReady, initialKind };
    const { rerender } = render(<AddMachineSheet {...props} />);
    expect(screen.queryByRole("dialog")).toBeNull();

    rerender(<AddMachineSheet {...props} open />);
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("This demo is read-only, so it can't start a sandbox.")).toBeTruthy();
    // The modal's header dismiss and footer Close are both dismissal only.
    const buttons = within(dialog).getAllByRole("button");
    expect(buttons.length).toBeGreaterThan(0);
    for (const button of buttons) expect(button.textContent?.trim() || button.getAttribute("aria-label")).toMatch(/^close$/i);
    expect(within(dialog).queryByRole("link")).toBeNull();
    expect(within(dialog).queryByRole("textbox")).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(onReady).not.toHaveBeenCalled();
    expect(createMachine).not.toHaveBeenCalled();

    fireEvent.click(buttons.find((button) => button.textContent?.trim() === "Close")!);
    expect(onClose).toHaveBeenCalledTimes(1);
    rerender(<AddMachineSheet {...props} />);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(onReady).not.toHaveBeenCalled();
    expect(createMachine).not.toHaveBeenCalled();
  });
});
