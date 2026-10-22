/**
 * MobilePrivacyPanel renders and writes through the same usePrivacyStore
 * actions desktop PrivacySettings uses.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const MOBILE_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");

const mocks = vi.hoisted(() => ({
  setCrashReporting: vi.fn(),
  setAnalytics: vi.fn(),
  initialize: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("../../../store/privacyStore", () => ({
  usePrivacyStore: () => ({
    crashReportingEnabled: true,
    analyticsEnabled: true,
    setCrashReporting: mocks.setCrashReporting,
    setAnalytics: mocks.setAnalytics,
    initialize: mocks.initialize,
  }),
}));

const { MobilePrivacyPanel } = await import("../MobilePrivacyPanel");

describe("MobilePrivacyPanel", () => {
  it("renders the same settings desktop PrivacySettings exposes", async () => {
    render(<MobilePrivacyPanel />);
    expect(await screen.findByText(/crash and error reporting/i)).toBeInTheDocument();
    expect(screen.getByText(/analytics and usage data/i)).toBeInTheDocument();
    await waitFor(() => expect(mocks.initialize).toHaveBeenCalled());
  });

  it("writes through the same setCrashReporting action desktop uses", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    render(<MobilePrivacyPanel />);
    await userEvent.setup().click(
      screen.getByRole("switch", { name: /crash and error reporting/i }),
    );
    expect(mocks.setCrashReporting).toHaveBeenCalledWith(false);
  });

  it("writes through the same setAnalytics action desktop uses", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    render(<MobilePrivacyPanel />);
    await userEvent.setup().click(
      screen.getByRole("switch", { name: /analytics and usage data/i }),
    );
    expect(mocks.setAnalytics).toHaveBeenCalledWith(false);
  });

  it("sizes interactive targets at 44px, not rem-based h-9/h-10/h-11", () => {
    const source = readFileSync(join(MOBILE_DIR, "MobilePrivacyPanel.tsx"), "utf8");
    expect(source).not.toMatch(/\bh-(9|10|11)\b/);
  });
});
