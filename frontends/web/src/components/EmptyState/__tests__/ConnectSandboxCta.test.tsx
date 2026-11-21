// Copyright (c) 2026 Reliant Labs

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("@/components/Machines/AddMachineSheet", () => ({
  AddMachineSheet: ({ open, initialKind }: { open: boolean; initialKind?: string }) =>
    open ? <div role="dialog" aria-label="Add an agent sandbox" data-kind={initialKind} /> : null,
}));

const { ConnectSandboxCta } = await import("../ConnectSandboxCta");

describe("ConnectSandboxCta", () => {
  it("opens the one Add sheet on your own machine or on a Reliant sandbox", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    render(<ConnectSandboxCta />);
    expect(screen.getByRole("heading", { name: "Connect an agent sandbox to start chats" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Use your own machine" }));
    expect(screen.getByRole("dialog")).toHaveAttribute("data-kind", "self");
    await user.click(screen.getByRole("button", { name: "Provision a Reliant sandbox" }));
    expect(screen.getByRole("dialog")).toHaveAttribute("data-kind", "cloud");
  });
});
