// Copyright (c) 2025 Reliant Labs

/**
 * Onboarding belongs to the agent side. The forge host's guard asks one thing
 * (ForgeAuthGuard: signed in), so a new or not-yet-onboarded account lands on
 * /apps, never in Reliant's wizard, and nothing on the forge host reads the
 * onboarding record. The Reliant host still sends the same user to /onboarding
 * (OnboardingGate), and the per-user flag is untouched, so "Open in Reliant"
 * onboards them there.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mockUseCurrentUser = vi.fn();
vi.mock("@/hooks/useOnboardingQueries", () => ({ useCurrentUser: () => mockUseCurrentUser() }));
vi.mock("@/store/authStore", () => {
  const state = {
    user: { id: "auth-1", email_confirmed_at: "2026-01-01T00:00:00Z" },
    loading: false,
    initialized: true,
    initialize: vi.fn(),
  };
  return { useAuthStore: (selector: (s: unknown) => unknown) => selector(state) };
});

// The root's other residents read stores and the network; they are not what this tests.
vi.mock("@/components/Orgs/ActiveOrgBinding", () => ({ ActiveOrgBinding: () => null }));
vi.mock("@/components/PageVisitTracker", () => ({ PageVisitTracker: () => null }));
vi.mock("@/components/Modals/ModalLayer", () => ({ ModalLayer: () => null }));
vi.mock("@/components/NewBuildPrompt", () => ({ NewBuildPrompt: () => null }));
vi.mock("@/lib/toast", () => ({ Toaster: () => null }));
vi.mock("@/components/Onboarding/JourneyTour", () => ({ JourneyTour: () => null }));
vi.mock("@/components/ForgeShell/ForgeLandingJourney", () => ({ ForgeLandingJourney: () => null }));
vi.mock("@/components/ForgeShell/ForgeAppsPage", () => ({ ForgeAppsPage: () => <div data-testid="apps-page" /> }));
vi.mock("@/components/ForgeShell/ForgeSitesPage", () => ({ ForgeSitesPage: () => <div data-testid="sites-page" /> }));
vi.mock("@/components/ForgeShell/ForgeShellLayout", async () => {
  const { Outlet } = await import("@tanstack/react-router");
  return { ForgeShellLayout: () => <Outlet /> };
});

import { forgeRouteTree } from "@/forgeShellRoutes";
import { OnboardingGate } from "@/components/OnboardingFlow/OnboardingGate";
import { onboardingSearchSchema } from "@/routeSchemas";

const notOnboarded = { data: { id: "u1", onboardingCompleted: false }, isLoading: false };

/** The forge host's production route tree, guard included (sign-in is mocked as done). */
function renderForgeHost(path: string) {
  const router = createRouter({
    routeTree: forgeRouteTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

/** The Reliant host's guard, as routes.tsx mounts it, around a page and the wizard. */
function renderReliantHost(path: string) {
  const root = createRootRoute({ component: Outlet });
  const layout = createRoute({
    getParentRoute: () => root,
    id: "_authenticated",
    component: () => (
      <OnboardingGate>
        <Outlet />
      </OnboardingGate>
    ),
  });
  const inbox = createRoute({ getParentRoute: () => layout, path: "/inbox", component: () => <div data-testid="page" /> });
  const wizard = createRoute({
    getParentRoute: () => layout,
    path: "/onboarding",
    validateSearch: onboardingSearchSchema,
    component: () => <div data-testid="wizard" />,
  });
  const router = createRouter({
    routeTree: root.addChildren([layout.addChildren([wizard, inbox])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  vi.clearAllMocks();
  mockUseCurrentUser.mockReturnValue(notOnboarded);
});

describe("the forge host", () => {
  it.each(["/", "/apps"])("lands a not-yet-onboarded user opening %s on /apps", async (path) => {
    const router = renderForgeHost(path);
    expect(await screen.findByTestId("apps-page", {}, { timeout: 15_000 })).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 100));
    expect(router.state.location.pathname).toBe("/apps");
    expect(screen.getByTestId("apps-page")).toBeInTheDocument();
  });

  // "Skip for now" on /auth signs in as a guest and navigates to `/` while the
  // guard's own bounce already sent the page there and on to /apps: a second
  // visit to `/` used to render nothing and never redirect (a blank page).
  it("sends every visit to / on to /apps, including a second one after landing", async () => {
    const router = renderForgeHost("/apps");
    expect(await screen.findByTestId("apps-page", {}, { timeout: 15_000 })).toBeInTheDocument();

    await router.navigate({ to: "/" as never });
    await waitFor(() => expect(router.state.location.pathname).toBe("/apps"), { timeout: 15_000 });
    expect(await screen.findByTestId("apps-page")).toBeInTheDocument();

    await router.navigate({ to: "/" as never });
    await waitFor(() => expect(router.state.location.pathname).toBe("/apps"), { timeout: 15_000 });
    expect(await screen.findByTestId("apps-page")).toBeInTheDocument();
  });

  it("keeps a not-yet-onboarded user on another guarded page, with no detour", async () => {
    const router = renderForgeHost("/sites");
    expect(await screen.findByTestId("sites-page", {}, { timeout: 15_000 })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/sites");
  });

  it("keeps a not-yet-onboarded user on a console deep link, with no detour", async () => {
    const router = renderForgeHost("/forge/p/simplebutton/releases?env=prod");
    await waitFor(() => expect(router.state.matches.some((m) => m.routeId.includes("/forge/p/"))).toBe(true), {
      timeout: 15_000,
    });
    expect(router.state.location.pathname).toBe("/forge/p/simplebutton/releases");
  });

  it("has no onboarding route: /onboarding falls through to /apps", async () => {
    const router = renderForgeHost("/onboarding?returnTo=%2Fapps");
    expect(await screen.findByTestId("apps-page", {}, { timeout: 15_000 })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/apps");
  });

  it("never reads the onboarding record: deploying needs no model, provider or finished onboarding", async () => {
    renderForgeHost("/apps");
    expect(await screen.findByTestId("apps-page", {}, { timeout: 15_000 })).toBeInTheDocument();
    expect(mockUseCurrentUser).not.toHaveBeenCalled();
  });
});

describe("the Reliant host", () => {
  it("still sends the same not-yet-onboarded user to /onboarding, remembering the page", async () => {
    const router = renderReliantHost("/inbox");
    await waitFor(() => expect(router.state.location.pathname).toBe("/onboarding"));
    expect((router.state.location.search as { returnTo?: string }).returnTo).toBe("/inbox");
    expect(await screen.findByTestId("wizard")).toBeInTheDocument();
  });
});
