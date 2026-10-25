/**
 * MobilePrivacyScreen renders MobilePrivacyPanel (mobile-native) behind the
 * shared section header.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../../../hooks/privacy-queries", () => ({
  usePrivacyConsent: () => ({ crashReportingEnabled: true, analyticsEnabled: true }),
  useUpdatePrivacySettings: () => ({ isPending: false, setCrashReporting: vi.fn(), setAnalytics: vi.fn() }),
}));

const { MobilePrivacyScreen } = await import("../MobilePrivacyScreen");

describe("MobilePrivacyScreen", () => {
  it("renders the mobile-native privacy panel content", async () => {
    render(<MobilePrivacyScreen onBack={vi.fn()} />);
    expect(await screen.findByText(/crash and error reporting/i)).toBeInTheDocument();
    expect(screen.getByText(/analytics and usage data/i)).toBeInTheDocument();
  });

  it("calls onBack from the header", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const onBack = vi.fn();
    render(<MobilePrivacyScreen onBack={onBack} />);
    await userEvent.setup().click(
      screen.getByRole("button", { name: /back to settings/i }),
    );
    expect(onBack).toHaveBeenCalled();
  });
});
