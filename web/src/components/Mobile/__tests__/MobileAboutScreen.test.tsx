/**
 * MobileAboutScreen — version display only, omitting the update-check UI
 * (`UpdateSection`, an Electron auto-updater panel) and the desktop quick
 * links list that includes the guided-tour restart (spotlights desktop
 * chrome, out of scope on mobile — see MobileLayout's module comment).
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const version = vi.fn();
vi.mock("../../../api/system-grpc", () => ({
  systemGrpc: { version: () => version() },
}));

const { MobileAboutScreen } = await import("../MobileAboutScreen");

describe("MobileAboutScreen", () => {
  it("shows the version once loaded", async () => {
    version.mockResolvedValue({ version: "2.3.4" });
    render(<MobileAboutScreen onBack={vi.fn()} />);
    expect(await screen.findByText("v2.3.4")).toBeInTheDocument();
  });

  it("falls back to a default version on fetch failure", async () => {
    version.mockRejectedValue(new Error("network down"));
    render(<MobileAboutScreen onBack={vi.fn()} />);
    expect(await screen.findByText("v1.0.0")).toBeInTheDocument();
  });

  it("does not render an update-check UI", async () => {
    version.mockResolvedValue({ version: "2.3.4" });
    render(<MobileAboutScreen onBack={vi.fn()} />);
    await screen.findByText("v2.3.4");
    expect(screen.queryByText(/check for updates/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/^updates$/i)).not.toBeInTheDocument();
  });

  it("calls onBack from the header", async () => {
    version.mockResolvedValue({ version: "2.3.4" });
    const { default: userEvent } = await import("@testing-library/user-event");
    const onBack = vi.fn();
    render(<MobileAboutScreen onBack={onBack} />);
    await userEvent.setup().click(
      screen.getByRole("button", { name: /back to settings/i }),
    );
    expect(onBack).toHaveBeenCalled();
  });
});
