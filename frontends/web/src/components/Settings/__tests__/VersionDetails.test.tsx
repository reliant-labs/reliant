/**
 * VersionDetails — the version block on both About screens.
 *
 * The regression: prod's About page read "version unknown" because it showed
 * ONLY the server's Version RPC, and prod's server was unstamped. The page's
 * own bundle knew what it was the whole time.
 */

import { screen, waitFor } from "@testing-library/react";
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

const { VersionDetails } = await import("../VersionDetails");

const SHA = "10c676b6f2fa66a4b13e68f2f995289a7245b8f0";

describe("VersionDetails", () => {
  beforeEach(() => {
    version.mockReset();
    appBuild.mockReset();
  });

  it("names this build even when the server is unstamped (prod, 2026-10-11)", async () => {
    appBuild.mockReturnValue({
      version: "1.8.2-dev+10c676b6",
      commit: SHA,
      ref: SHA,
      release: "20261011.184732-0b1d2c3e4f5a",
      built: "2026-10-11T18:47:32.000Z",
    });
    // Exactly what https://api.reliantapi.com answered.
    version.mockResolvedValue({ version: "unknown", commit: "unknown", date: "unknown", branch: "unknown" });

    render(<VersionDetails />);

    expect(screen.getByTestId("about-app-version")).toHaveTextContent("v1.8.2-dev+10c676b6");
    expect(screen.queryByText("version unknown")).not.toBeInTheDocument();
    expect(screen.getByTestId("about-release")).toHaveTextContent("20261011.184732-0b1d2c3e4f5a");
    expect(await screen.findByText("unknown")).toBeInTheDocument();
  });

  it("shows the server's release identity once it answers", async () => {
    appBuild.mockReturnValue({ version: "1.8.1", commit: SHA, ref: "v1.8.1", built: "x" });
    version.mockResolvedValue({ version: "1.8.1", commit: SHA });

    render(<VersionDetails />);

    expect(screen.getByTestId("about-app-version")).toHaveTextContent("v1.8.1");
    expect(screen.getByTestId("about-server-version")).toHaveTextContent("…");
    await waitFor(() =>
      expect(screen.getByTestId("about-server-version")).toHaveTextContent("v1.8.1"),
    );
    // No forge release outside one (the desktop app): no empty row.
    expect(screen.queryByTestId("about-release")).not.toBeInTheDocument();
  });

  it("falls back to the server's commit when it has no version stamp", async () => {
    appBuild.mockReturnValue(null);
    version.mockResolvedValue({ version: "unknown", commit: SHA });

    render(<VersionDetails />);

    expect(screen.getByTestId("about-app-version")).toHaveTextContent("version unknown");
    expect(await screen.findByText("10c676b6")).toBeInTheDocument();
  });
});
