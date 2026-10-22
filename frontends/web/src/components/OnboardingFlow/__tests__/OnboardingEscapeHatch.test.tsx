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

import { OnboardingEscapeHatch } from "../OnboardingEscapeHatch";

describe("OnboardingEscapeHatch", () => {
  beforeEach(() => {
    vi.clearAllMocks();
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
