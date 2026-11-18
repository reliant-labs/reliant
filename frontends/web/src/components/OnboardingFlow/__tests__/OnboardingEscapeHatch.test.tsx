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
  useAuthStore: (selector: (s: unknown) => unknown) => selector({ signOut, user: { id: "auth-user-1" } }),
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

const isForgeShell = vi.fn(() => false);
vi.mock("@/lib/shell", () => ({
  forgeAppOrigin: () => "https://forge.reliantlabs.io",
  isForgeShell: () => isForgeShell(),
}));

import { OnboardingEscapeHatch } from "../OnboardingEscapeHatch";
import { FORGE_SETUP_SKIP_KEY, forgeSetupSkippedBy } from "../onboardingGate";

describe("OnboardingEscapeHatch", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    isElectron.mockReturnValue(false);
    isForgeShell.mockReturnValue(false);
    localStorage.removeItem(FORGE_SETUP_SKIP_KEY);
  });

  // On the forge host the wizard runs before the console (OnboardingGate), so
  // a link to the console would loop straight back here.
  it("on the forge site, skips to the page the user was going to and remembers the choice", async () => {
    isForgeShell.mockReturnValue(true);
    render(<OnboardingEscapeHatch returnTo="/forge/p/simplebutton/releases?env=prod" />);
    expect(screen.queryByTestId("onboarding-forge-console-link")).toBeNull();

    await userEvent.click(screen.getByTestId("onboarding-forge-console-skip"));

    expect(forgeSetupSkippedBy("auth-user-1")).toBe(true);
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({ href: "/forge/p/simplebutton/releases?env=prod", replace: true }),
    );
    // A skip, not a completion: nothing here talks to the server.
    expect(signOut).not.toHaveBeenCalled();
  });

  it("on the forge site with nowhere in particular to go, skips to the Apps list", async () => {
    isForgeShell.mockReturnValue(true);
    render(<OnboardingEscapeHatch />);
    await userEvent.click(screen.getByTestId("onboarding-forge-console-skip"));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: "/apps", search: {} }));
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
