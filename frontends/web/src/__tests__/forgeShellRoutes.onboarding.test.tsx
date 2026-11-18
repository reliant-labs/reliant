// Copyright (c) 2025 Reliant Labs

/**
 * The forge site runs Reliant's own onboarding wizard: a signed-in user who
 * has not finished it is taken through it on the forge host (no second
 * sign-in) before the Apps list or a console page, and comes back to the page
 * they asked for. A claim landing and the wizard's own forge-only skip are
 * left alone.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRouter, RouterProvider, useSearch } from "@tanstack/react-router";
import type { ReactNode } from "react";

const mockUseCurrentUser = vi.fn();
vi.mock("@/hooks/useOnboardingQueries", () => ({ useCurrentUser: () => mockUseCurrentUser() }));
vi.mock("@/store/authStore", () => ({
  useAuthStore: (selector: (s: unknown) => unknown) => selector({ user: { id: "auth-1" } }),
}));

// The root's other residents read stores and the network; they are not what this tests.
vi.mock("@/components/Orgs/ActiveOrgBinding", () => ({ ActiveOrgBinding: () => null }));
vi.mock("@/components/PageVisitTracker", () => ({ PageVisitTracker: () => null }));
vi.mock("@/components/Modals/ModalLayer", () => ({ ModalLayer: () => null }));
vi.mock("@/components/NewBuildPrompt", () => ({ NewBuildPrompt: () => null }));
vi.mock("@/lib/toast", () => ({ Toaster: () => null }));
vi.mock("@/components/Onboarding/JourneyTour", () => ({ JourneyTour: () => null }));
vi.mock("@/components/ForgeShell/ForgeLandingJourney", () => ({ ForgeLandingJourney: () => null }));
vi.mock("@/components/ForgeShell/ForgeAppsPage", () => ({ ForgeAppsPage: () => <div data-testid="apps-page" /> }));
vi.mock("@/components/ForgeShell/ForgeShellLayout", async () => {
  const { Outlet } = await import("@tanstack/react-router");
  return { ForgeShellLayout: () => <Outlet /> };
});
// The wizard itself: what matters here is that it is mounted, at Reliant's route id.
vi.mock("@/components/OnboardingFlow/OnboardingRoute", () => ({
  OnboardingRoute: function Wizard() {
    const search = useSearch({ from: "/_authenticated/onboarding" as never }) as { returnTo?: string };
    return <div data-testid="wizard">{search.returnTo ?? ""}</div>;
  },
}));

import { buildForgeShellTree } from "@/forgeShellRoutes";
import { OnboardingGate } from "@/components/OnboardingFlow/OnboardingGate";
import {
  CLAIM_LANDING_KEY,
  FORGE_SETUP_SKIP_KEY,
  FORGE_SHELL_ONBOARDING_EXEMPT_PREFIXES,
  markClaimLanding,
  markForgeSetupSkipped,
} from "@/components/OnboardingFlow/onboardingGate";

/** The production guard minus ForgeAuthGuard (sign-in is not what this tests). */
function Guard({ children }: { children: ReactNode }) {
  return (
    <OnboardingGate exempt={FORGE_SHELL_ONBOARDING_EXEMPT_PREFIXES} honourForgeSkip>
      {children}
    </OnboardingGate>
  );
}

function renderShell(path: string) {
  const router = createRouter({
    routeTree: buildForgeShellTree({ Guard }),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  vi.clearAllMocks();
  sessionStorage.removeItem(CLAIM_LANDING_KEY);
  localStorage.removeItem(FORGE_SETUP_SKIP_KEY);
});

describe("the forge site's onboarding", () => {
  it("takes a not-yet-onboarded user from /apps through the wizard, remembering /apps", async () => {
    mockUseCurrentUser.mockReturnValue({ data: { onboardingCompleted: false }, isLoading: false });
    const router = renderShell("/apps");

    expect(await screen.findByTestId("wizard", {}, { timeout: 15_000 })).toHaveTextContent("/apps");
    expect(router.state.location.pathname).toBe("/onboarding");
    expect(router.state.matches.some((m) => m.routeId === "/_authenticated/onboarding")).toBe(true);
  });

  it("does the same from a console deep link", async () => {
    mockUseCurrentUser.mockReturnValue({ data: { onboardingCompleted: false }, isLoading: false });
    const router = renderShell("/forge/p/simplebutton/releases?env=prod");

    await waitFor(() => expect(router.state.location.pathname).toBe("/onboarding"), { timeout: 15_000 });
    expect((router.state.location.search as { returnTo?: string }).returnTo).toBe(
      "/forge/p/simplebutton/releases?env=prod",
    );
  });

  it("lets an onboarded user straight through", async () => {
    mockUseCurrentUser.mockReturnValue({ data: { onboardingCompleted: true }, isLoading: false });
    renderShell("/apps");
    expect(await screen.findByTestId("apps-page", {}, { timeout: 15_000 })).toBeInTheDocument();
  });

  // A claim excuses only the claimed app's console (onboardingGate.test.tsx
  // shows that one passing); everything else on the forge site runs the wizard.
  it("runs the wizard on /apps for a claimant", async () => {
    mockUseCurrentUser.mockReturnValue({ data: { onboardingCompleted: false }, isLoading: false });
    markClaimLanding("auth-1", "simplebutton");
    const router = renderShell("/apps");
    await waitFor(() => expect(router.state.location.pathname).toBe("/onboarding"), { timeout: 15_000 });
  });

  it("runs the wizard on another app's console for a claimant", async () => {
    mockUseCurrentUser.mockReturnValue({ data: { onboardingCompleted: false }, isLoading: false });
    markClaimLanding("auth-1", "simplebutton");
    const router = renderShell("/forge/p/other-app/releases");
    await waitFor(() => expect(router.state.location.pathname).toBe("/onboarding"), { timeout: 15_000 });
  });

  it("leaves alone a user who took the wizard's forge-only skip", async () => {
    mockUseCurrentUser.mockReturnValue({ data: { onboardingCompleted: false }, isLoading: false });
    markForgeSetupSkipped("auth-1");
    renderShell("/apps");
    expect(await screen.findByTestId("apps-page", {}, { timeout: 15_000 })).toBeInTheDocument();
  });

  it("does not honour another user's skip", async () => {
    mockUseCurrentUser.mockReturnValue({ data: { onboardingCompleted: false }, isLoading: false });
    markForgeSetupSkipped("someone-else");
    const router = renderShell("/apps");
    await waitFor(() => expect(router.state.location.pathname).toBe("/onboarding"), { timeout: 15_000 });
  });
});
