/**
 * A user stuck in onboarding must still be able to leave.
 *
 * Onboarding renders as a fixed z-40 overlay on its own route, so the app
 * Header is suppressed (`showHeader` gates on `!inOnboarding`) and the Sidebar
 * — which needs a selected project — never mounts. Without an affordance on
 * this surface, a user whose daemon fails to provision cannot reach settings
 * and cannot sign out.
 */
/* eslint-disable @typescript-eslint/no-explicit-any */
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const navigate = vi.fn(async () => undefined);
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
}));

const signOut = vi.fn(async () => undefined);
vi.mock("@/store/authStore", () => ({
  useAuthStore: (selector: (s: unknown) => unknown) => selector({ signOut }),
}));

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));

const trackEvent = vi.fn();
vi.mock("@/lib/analytics", () => ({ trackEvent: (...args: unknown[]) => trackEvent(...args) }));

const isElectron = vi.fn(() => false);
vi.mock("@/lib/constants", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/constants")>()),
  isElectron: () => isElectron(),
}));

vi.mock("@/lib/shell", () => ({ forgeAppOrigin: () => "https://forge.reliantlabs.io" }));

import { OnboardingEscapeHatch } from "../OnboardingEscapeHatch";

describe("OnboardingEscapeHatch", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    isElectron.mockReturnValue(false);
  });

  it("links to the forge console's apps page in a new tab", () => {
    render(<OnboardingEscapeHatch />);
    const link = screen.getByTestId("onboarding-forge-console-link");
    expect(link).toHaveTextContent("Only here to deploy apps? Open the Forge console");
    expect(link).toHaveAttribute("href", "https://forge.reliantlabs.io/apps");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
  });

  it("tracks the click with no properties and leaves onboarding alone", async () => {
    render(<OnboardingEscapeHatch />);
    const link = screen.getByTestId("onboarding-forge-console-link");
    // jsdom would try to navigate on a real click; the test only needs the handler.
    link.addEventListener("click", (e) => e.preventDefault());
    await userEvent.click(link);
    expect(trackEvent).toHaveBeenCalledTimes(1);
    expect(trackEvent).toHaveBeenCalledWith("onboarding_forge_console_opened");
    expect(navigate).not.toHaveBeenCalled();
    expect(signOut).not.toHaveBeenCalled();
  });

  it("hides the forge console link in the desktop app", () => {
    isElectron.mockReturnValue(true);
    render(<OnboardingEscapeHatch />);
    expect(screen.queryByTestId("onboarding-forge-console-link")).toBeNull();
  });

  it("navigates to settings without needing a daemon or project", async () => {
    render(<OnboardingEscapeHatch />);
    await userEvent.click(screen.getByTestId("onboarding-settings-link"));
    expect(navigate).toHaveBeenCalledWith({ to: "/settings" });
  });

  it("signs the user out and sends them to /auth", async () => {
    render(<OnboardingEscapeHatch />);
    await userEvent.click(screen.getByTestId("onboarding-sign-out"));

    await waitFor(() => expect(signOut).toHaveBeenCalledTimes(1));
    expect(navigate).toHaveBeenCalledWith({
      to: "/auth",
      search: { redirect: undefined },
    });
  });

  it("surfaces a failed sign-out instead of appearing to succeed", async () => {
    signOut.mockRejectedValueOnce(new Error("network down"));
    render(<OnboardingEscapeHatch />);
    await userEvent.click(screen.getByTestId("onboarding-sign-out"));

    expect(await screen.findByRole("alert")).toHaveTextContent("network down");
    // The button must return to an actionable state so the user can retry.
    expect(screen.getByTestId("onboarding-sign-out")).not.toBeDisabled();
  });
});
