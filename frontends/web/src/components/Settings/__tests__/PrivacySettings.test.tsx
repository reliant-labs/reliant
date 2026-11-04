/**
 * The Privacy page says where data actually lives (the servers keep chats;
 * code stays where its machine runs) and treats opting out as a choice, not
 * a fault: no "Privacy Mode Active" warning.
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const consent = vi.hoisted(() => ({ crashReportingEnabled: true, analyticsEnabled: true }));
vi.mock("../../../hooks/privacy-queries", () => ({
  usePrivacyConsent: () => consent,
  useUpdatePrivacySettings: () => ({ setCrashReporting: vi.fn(), setAnalytics: vi.fn() }),
}));
vi.mock("../../../services/controlPlane/config", () => ({ hasControlPlane: true }));

const { PrivacySettings } = await import("../PrivacySettings");

describe("Privacy settings", () => {
  it("does not claim chats and code are stored locally", () => {
    render(<PrivacySettings />);
    expect(screen.queryByText(/stored locally/i)).not.toBeInTheDocument();
    expect(screen.getByText(/Chats are stored on Reliant's servers/i)).toBeInTheDocument();
    expect(screen.getByText(/Your code stays on the machine it runs on/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /what is stored where/i })).toHaveAttribute(
      "href",
      "https://docs.reliantlabs.io/help/privacy",
    );
  });

  it("shows no warning when the user opts out", () => {
    consent.crashReportingEnabled = false;
    consent.analyticsEnabled = false;
    render(<PrivacySettings />);
    expect(screen.queryByText(/privacy mode/i)).not.toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Crash and error reporting" })).toHaveAttribute("aria-checked", "false");
    expect(screen.getByRole("switch", { name: "Analytics and usage data" })).toHaveAttribute("aria-checked", "false");
  });
});
