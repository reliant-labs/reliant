/**
 * MobileAboutScreen — version display only, omitting the update-check UI
 * (`UpdateSection`, an Electron auto-updater panel) and the desktop quick
 * links list that includes the guided-tour restart (spotlights desktop
 * chrome, out of scope on mobile — see MobileLayout's module comment).
 */

import { screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactElement } from "react";

import { mockTransport, renderWithTransport } from "@/lib/test-utils";

// The server's Version RPC, answered through the real transport seam.
const version = vi.fn();
const render = (ui: ReactElement) =>
  renderWithTransport(ui, {
    transport: mockTransport({ "SystemService.Version": () => version() }),
  });
const appBuild = vi.fn();
vi.mock("../../../lib/appBuild", () => ({ appBuild: () => appBuild() }));

const { MobileAboutScreen } = await import("../MobileAboutScreen");

describe("MobileAboutScreen", () => {
  beforeEach(() => {
    appBuild.mockReturnValue({
      version: "1.8.2-dev+10c676b6",
      commit: "10c676b6f2fa66a4b13e68f2f995289a7245b8f0",
      ref: "10c676b6f2fa66a4b13e68f2f995289a7245b8f0",
      built: "2026-10-11T18:47:32.000Z",
    });
  });

  it("shows this app's build and the server's", async () => {
    version.mockResolvedValue({ version: "2.3.4", commit: "abc" });
    render(<MobileAboutScreen onBack={vi.fn()} />);
    expect(screen.getByText("v1.8.2-dev+10c676b6")).toBeInTheDocument();
    expect(await screen.findByText("v2.3.4")).toBeInTheDocument();
  });

  it("never invents a version when the server cannot be reached", async () => {
    // It used to print "v1.0.0" here — a release that never shipped.
    version.mockRejectedValue(new Error("network down"));
    render(<MobileAboutScreen onBack={vi.fn()} />);
    expect(await screen.findByText("unreachable")).toBeInTheDocument();
    expect(screen.queryByText("v1.0.0")).not.toBeInTheDocument();
    expect(screen.getByText("v1.8.2-dev+10c676b6")).toBeInTheDocument();
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
