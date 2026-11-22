// Copyright (c) 2025 Reliant Labs

/**
 * The forge host mounts the app's one route tree (routes.tsx, U6) as the
 * forge BRAND. What the brand changes is decided when the module evaluates,
 * so each brand gets a fresh import of it:
 *
 *   - the boot points the tour at the forge shell, with progress in this
 *     origin's localStorage, and takes the landing handoff for the first
 *     page_visited event;
 *   - the root carries `data-shell="forge"` and mounts JourneyTour (and the
 *     landing journey) in place of Reliant's OnboardingWizard, so the tour
 *     runs on the public demo, signed out;
 *   - `/forge/demo` and `/demo` are public on both brands; every app page is
 *     still behind the sign-in guard.
 */

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRouter, RouterProvider, type AnyRoute } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const firstVisitProps = vi.hoisted(() => ({ seen: [] as unknown[] }));

// The root's other residents read stores and the network; they are not what this tests.
vi.mock("@/components/Orgs/ActiveOrgBinding", () => ({ ActiveOrgBinding: () => null }));
vi.mock("@/components/PageVisitTracker", () => ({
  PageVisitTracker: (props: { firstVisitProps?: unknown }) => {
    firstVisitProps.seen.push(props.firstVisitProps);
    return null;
  },
}));
vi.mock("@/components/Shortcuts/ShortcutHost", () => ({ ShortcutHost: ({ children }: { children: unknown }) => children }));
vi.mock("@/components/Modals/ModalLayer", () => ({ ModalLayer: () => null }));
vi.mock("@/components/Projects/AddProjectDialog", () => ({ AddProjectDialogHost: () => null }));
vi.mock("@/components/Automations/dialog/AutomationDialogHost", () => ({ AutomationDialogHost: () => null }));
vi.mock("@/components/AnonSignInNudge", () => ({ AnonSignInNudge: () => null }));
vi.mock("@/lib/toast", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/lib/toast")>()), Toaster: () => null }));
vi.mock("@/components/NewBuildPrompt", () => ({ NewBuildPrompt: () => null }));
vi.mock("@/components/ClientMinimumWatch", () => ({ ClientMinimumWatch: () => null }));
vi.mock("@/components/Settings/GlobalUpdateHandler", () => ({ GlobalUpdateHandler: () => null }));
vi.mock("@/components/Onboarding", () => ({
  ContextualTipsLayer: () => null,
  OnboardingWizard: () => <div data-testid="onboarding-wizard" />,
}));
vi.mock("@/components/Layout/GitHubSyncBanner", () => ({ GitHubSyncStatus: () => null }));
vi.mock("@/components/Forge/QueuedDeployWatch", () => ({ QueuedDeployWatch: () => null }));
vi.mock("@/components/Onboarding/JourneyTour", () => ({ JourneyTour: () => <div data-testid="journey-tour" /> }));
vi.mock("@/components/ForgeShell/ForgeLandingJourney", () => ({ ForgeLandingJourney: () => <div data-testid="landing-journey" /> }));
vi.mock("@/components/Forge/Demo/DemoConsole", () => ({ DemoConsole: () => <div data-testid="demo-console" /> }));
vi.mock("@/components/AuthScreen", () => ({ AuthScreen: () => <div data-testid="auth-screen" /> }));
vi.mock("@/hooks/useAuthMode", () => ({ useAuthMode: () => ({ authMode: "supabase", loading: false }) }));

import { FORGE_TOUR_STORAGE_KEY } from "@/components/ForgeShell/forgeTourBoot";

/**
 * routes.tsx, evaluated afresh as `brand`'s page load would, with the module
 * instances it shares: a fresh registry, so the shell, the tour and the auth
 * store read here are the ones the tree was built against.
 */
async function loadTree(brand: "forge" | "reliant") {
  vi.resetModules();
  const shell = await import("@/lib/shell");
  shell.resetShellForTests(brand);
  const tour = await import("@/components/Onboarding/tourShell");
  const { useAuthStore } = await import("@/store/authStore");
  const { routeTree } = await import("@/routes");
  const signOut = () => useAuthStore.setState({ user: null, session: null, initialized: true, loading: false });
  return { routeTree: routeTree as AnyRoute, tour, shell, signOut };
}

type Loaded = Awaited<ReturnType<typeof loadTree>>;

function renderAt(routeTree: AnyRoute, path: string) {
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

afterAll(() => {
  window.localStorage.clear();
});

describe("the forge brand", () => {
  let app: Loaded;
  let tree: AnyRoute;
  const signOut = () => app.signOut();
  beforeAll(async () => {
    window.history.replaceState(null, "", "/?journey=forge&utm_campaign=c-1");
    app = await loadTree("forge");
    tree = app.routeTree;
    window.history.replaceState(null, "", "/");
  }, 60_000);
  afterAll(() => {
    app.shell.resetShellForTests();
    app.tour.setTourShell(null);
    app.tour.setTourStorage(app.tour.settingsTourStorage);
  });

  it("boots the tour as the forge shell's, with progress in this origin's localStorage", async () => {
    expect(app.tour.getTourShell()).toBe("forge");
    const storage = app.tour.getTourStorage();
    expect(storage.kind).toBe("local");
    await storage.set("onboarding.tour.forge.v2.completed", "completed");
    expect(JSON.parse(window.localStorage.getItem(FORGE_TOUR_STORAGE_KEY) ?? "{}")).toEqual({
      "onboarding.tour.forge.v2.completed": "completed",
    });
    // The override, not the hostname, decides: jsdom is on localhost.
    expect(window.location.hostname).not.toBe("forge.reliantlabs.io");
  });

  it("serves /forge/demo to a signed-out visitor, with the tour and the landing journey at the root", async () => {
    signOut();
    firstVisitProps.seen.length = 0;
    const router = renderAt(tree, "/forge/demo?tour=forge:fg-console");
    expect(await screen.findByTestId("demo-console", {}, { timeout: 15_000 })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/forge/demo");
    expect((router.state.location.search as { tour?: string }).tour).toBe("forge:fg-console");
    expect(screen.getByTestId("journey-tour")).toBeInTheDocument();
    expect(screen.getByTestId("landing-journey")).toBeInTheDocument();
    // Reliant's checklist and wizard are not the forge brand's.
    expect(screen.queryByTestId("onboarding-wizard")).not.toBeInTheDocument();
    expect(document.querySelector("[data-shell='forge']")).not.toBeNull();
    // The landing handoff, taken before the router existed.
    expect(firstVisitProps.seen.at(-1)).toEqual({ utm_campaign: "c-1", journey: "forge" });
    cleanup();
  });

  it("sends /demo to /forge/demo, keeping the query", async () => {
    signOut();
    const router = renderAt(tree, "/demo?tour=forge&view=traces");
    expect(await screen.findByTestId("demo-console", {}, { timeout: 15_000 })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/forge/demo");
    expect(router.state.location.search).toMatchObject({ tour: "forge", view: "traces" });
    cleanup();
  });

  it.each(["/apps", "/sites", "/forge/p/shop/environments"])("keeps %s behind the sign-in guard", async (path) => {
    signOut();
    const router = renderAt(tree, path);
    await waitFor(() => expect(router.state.location.pathname).toBe("/auth"), { timeout: 15_000 });
    expect(screen.queryByTestId("demo-console")).not.toBeInTheDocument();
    cleanup();
  }, 30_000);
});

describe("the Reliant brand", () => {
  let app: Loaded;
  beforeAll(async () => {
    app = await loadTree("reliant");
  }, 60_000);
  afterAll(() => app.shell.resetShellForTests());

  it("boots nothing of the forge brand's, and mounts the OnboardingWizard", async () => {
    const tree = app.routeTree;
    expect(app.tour.getTourShell()).toBe("reliant");
    expect(app.tour.getTourStorage().kind).toBe("settings");
    app.signOut();
    firstVisitProps.seen.length = 0;
    renderAt(tree, "/forge/demo");
    expect(await screen.findByTestId("demo-console", {}, { timeout: 15_000 })).toBeInTheDocument();
    expect(screen.getByTestId("onboarding-wizard")).toBeInTheDocument();
    expect(screen.queryByTestId("landing-journey")).not.toBeInTheDocument();
    expect(document.querySelector("[data-shell]")).toBeNull();
    expect(firstVisitProps.seen.at(-1)).toBeUndefined();
    cleanup();
  });
});
