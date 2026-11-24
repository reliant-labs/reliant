// Copyright (c) 2025 Reliant Labs

/**
 * The landing page's `?journey=` on the forge brand (landingJourney.ts,
 * ForgeLandingJourney): `forge` starts the forge journey — on the public demo
 * for a signed-out visitor, on arrival or after sign-in for an account — and
 * `workflows` never runs here but points at the Workflows area, in this app
 * (one shell). Driven through a real router and the real auth guard (AuthGuard, as
 * routes.tsx mounts it on both its signed-in layouts).
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Navigate, Outlet, RouterProvider } from "@tanstack/react-router";
import type { ComponentType } from "react";

const trackEvent = vi.hoisted(() => vi.fn());
vi.mock("@/lib/analytics", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/analytics")>()),
  trackEvent,
}));
vi.mock("@/store/authStore", async () => {
  const { create } = await import("zustand");
  type User = { id: string; email_confirmed_at?: string | null; is_anonymous?: boolean };
  return {
    useAuthStore: create<{
      user: User | null;
      loading: boolean;
      initialized: boolean;
      initialize: () => Promise<void>;
      signInAnonymously: () => Promise<void>;
    }>(() => ({
      user: null,
      loading: false,
      initialized: true,
      initialize: async () => undefined,
      signInAnonymously: async () => undefined,
    })),
  };
});
vi.mock("@/hooks/useAuthMode", () => ({ useAuthMode: () => ({ authMode: "supabase", loading: false }) }));
vi.mock("@/components/Forge/Demo/DemoConsole", () => ({ DemoConsole: () => <div data-testid="demo-console" /> }));
vi.mock("@/components/Layout/LoadingSpinner", () => ({ LoadingSpinner: () => <div data-testid="spinner" /> }));

// The app registers the workflows journey's content (routes.tsx does, on both hosts).
import "@/components/Onboarding/journeys/catalog";
import { createForgeDemoRedirectRoute, createForgeDemoRoute } from "@/components/Forge/Demo/demoRoute";
import { JourneyTour } from "@/components/Onboarding/JourneyTour";
import { resolveTour } from "@/components/Onboarding/journeys/registry";
import { parseTourParam } from "@/components/Onboarding/tourParam";
import { localStorageTourStorage, setTourShell, setTourStorage, settingsTourStorage } from "@/components/Onboarding/tourShell";
import { holdLandingJourney, readLandingJourney } from "@/lib/landingAttribution";
import { useAuthStore } from "@/store/authStore";
import { useTourStore } from "@/store/tourStore";
import { resetShellForTests } from "@/lib/shell";

import { AuthGuard } from "../../AuthGuard";
import { ForgeLandingJourney } from "../ForgeLandingJourney";
import {
  hasTourParam,
  landingAuthDecision,
  landingJourneyAction,
  landingJourneyHoldsSignIn,
  type LandingJourneyInputs,
} from "../landingJourney";

type AuthUser = NonNullable<ReturnType<typeof useAuthStore.getState>["user"]>;
const SIGNED_IN = { id: "u1", email_confirmed_at: "2026-11-01T00:00:00Z" } as AuthUser;

/**
 * routes.tsx's shape on the forge brand: public sign-in and demo, the chat
 * home behind `_app`'s guard and the other pages behind `_authenticated`'s,
 * and ForgeLandingJourney at the root. The demo here is the demo shell's page
 * (DemoConsole mocked), standing in for routes.tsx's handoff to the demo host
 * (demoHandoff.ts), so the router stays in this document.
 */
function renderShell(path: string, extra: ComponentType = () => null, { landing = true } = {}) {
  const Extra = extra;
  const root = createRootRoute({
    component: () => (
      <>
        <Outlet />
        {landing && <ForgeLandingJourney />}
        <Extra />
      </>
    ),
  });
  const guarded = createRoute({
    getParentRoute: () => root,
    id: "_authenticated",
    component: () => (
      <AuthGuard requireAuth requireEmailVerification returnHere>
        <Outlet />
      </AuthGuard>
    ),
  });
  const app = createRoute({
    getParentRoute: () => root,
    id: "_app",
    component: () => (
      <AuthGuard requireAuth requireEmailVerification>
        <Outlet />
      </AuthGuard>
    ),
  });
  const router = createRouter({
    routeTree: root.addChildren([
      createRoute({ getParentRoute: () => root, path: "/auth", component: () => <div data-testid="auth-screen" /> }),
      ...["/reset-password", "/verify-email", "/upgrade"].map((path) => createRoute({
        getParentRoute: () => root,
        path,
        component: () => <AuthGuard requireAuth><div data-testid="protected-page" /></AuthGuard>,
      })),
      createForgeDemoRoute(() => root),
      createForgeDemoRedirectRoute(() => root),
      app.addChildren([createRoute({ getParentRoute: () => app, path: "/", component: () => <Navigate to="/apps" replace /> })]),
      guarded.addChildren([
        createRoute({ getParentRoute: () => guarded, path: "/apps", component: () => <div data-testid="apps-page" /> }),
        createRoute({ getParentRoute: () => guarded, path: "/projects", component: () => <div data-testid="projects-page" /> }),
        createRoute({ getParentRoute: () => guarded, path: "/workflows/library", component: () => <div data-testid="workflows-page" /> }),
      ]),
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

/** What attributionBoot does with the landing link, before anything renders. */
function arriveWith(search: string) {
  holdLandingJourney(search);
}

const searchOf = (router: ReturnType<typeof renderShell>) => router.state.location.search as Record<string, unknown>;

beforeEach(() => {
  resetShellForTests("forge");
  setTourShell("forge");
  setTourStorage(localStorageTourStorage("forge.tour.v1"));
  useTourStore.setState({ isInitialized: true });
  useAuthStore.setState({ user: null, loading: false, initialized: true });
  trackEvent.mockClear();
});

afterEach(() => {
  resetShellForTests();
  setTourShell(null);
  setTourStorage(settingsTourStorage);
  window.localStorage.clear();
});

it.each(["/reset-password", "/verify-email", "/upgrade"])("does not hold sign-in outside the journey layouts: %s", async (path) => {
  arriveWith("?journey=forge");
  const router = renderShell(path);
  await waitFor(() => expect(router.state.location.pathname).toBe("/auth"));
  expect(await screen.findByTestId("auth-screen")).toBeInTheDocument();
  expect(screen.queryByTestId("protected-page")).not.toBeInTheDocument();
});

describe("landingAuthDecision", () => {
  it.each([
    [{ initialized: false, loading: false, user: null }, "wait"],
    [{ initialized: true, loading: true, user: null }, "wait"],
    [{ initialized: true, loading: false, user: null }, "sign-in"],
    [{ initialized: true, loading: false, user: { email_confirmed_at: null } }, "verify-email"],
    [{ initialized: true, loading: false, user: { email_confirmed_at: "2026-01-01" } }, "allow"],
    // A claim in flight holds an anonymous session: it is let in, as AuthGuard does.
    [{ initialized: true, loading: false, user: { is_anonymous: true } }, "allow"],
  ] as const)("%j → %s", (state, expected) => {
    expect(landingAuthDecision(state)).toBe(expected);
  });
});

describe("landingJourneyAction", () => {
  const base: LandingJourneyInputs = { held: "forge", tourOpen: false, forgeAvailable: true, onDemo: false, guarded: true, auth: "allow" };

  it.each<[Partial<LandingJourneyInputs>, string]>([
    [{ held: undefined }, "none"],
    [{ held: "workflows" }, "offer-workflows"],
    [{ held: "workflows", tourOpen: true }, "offer-workflows"],
    [{}, "start-forge"],
    [{ auth: "sign-in" }, "start-forge"],
    [{ auth: "wait" }, "wait"],
    [{ auth: "verify-email" }, "wait"],
    // Signing in, or a claim of their own: after sign-in.
    [{ guarded: false, auth: "sign-in" }, "wait"],
    [{ guarded: false, onDemo: true, auth: "sign-in" }, "start-forge"],
    [{ onDemo: true, guarded: false, auth: "wait" }, "start-forge"],
    [{ tourOpen: true }, "drop"],
    [{ forgeAvailable: false }, "drop"],
  ])("%j → %s", (inputs, expected) => {
    expect(landingJourneyAction({ ...base, ...inputs })).toBe(expected);
  });

  it("holds the guard's sign-in for exactly the visitor it sends to the demo", () => {
    expect(landingJourneyHoldsSignIn("/apps", "forge")).toBe(true);
    expect(landingJourneyHoldsSignIn("/apps?tour=forge%3Afg-trace", "forge")).toBe(false);
    expect(landingJourneyHoldsSignIn("/apps", "workflows")).toBe(false);
    expect(landingJourneyHoldsSignIn("/apps", undefined)).toBe(false);
    expect(hasTourParam("/x?a=1&tour=pick")).toBe(true);
    expect(hasTourParam("/x?a=1")).toBe(false);
  });
});

describe("?journey=forge on the forge brand", () => {
  it("takes a signed-out visitor straight to the forge journey on the public demo, not to sign-in", async () => {
    arriveWith("?journey=forge&rid=r1");
    const router = renderShell("/?journey=forge&rid=r1");
    await waitFor(() => expect(router.state.location.pathname).toBe("/forge/demo"));
    expect(searchOf(router)).toMatchObject({ tour: "forge:fg-intro", view: "overview" });
    expect(await screen.findByTestId("demo-console")).toBeInTheDocument();
    expect(screen.queryByTestId("auth-screen")).not.toBeInTheDocument();
    expect(router.history.location.pathname).not.toBe("/auth");
    expect(trackEvent).toHaveBeenCalledWith("journey_selected", expect.objectContaining({ journey: "forge", source: "landing" }));
    // Started once; the choice is spent.
    expect(readLandingJourney()).toBeUndefined();
  });

  it("starts it for a signed-in user on arrival", async () => {
    useAuthStore.setState({ user: SIGNED_IN });
    arriveWith("?journey=forge");
    const router = renderShell("/apps?journey=forge");
    await waitFor(() => expect(router.state.location.pathname).toBe("/forge/demo"));
    expect(searchOf(router).tour).toBe("forge:fg-intro");
    expect(trackEvent.mock.calls.filter(([name]) => name === "journey_selected")).toHaveLength(1);
  });

  it("waits through the sign-in page, then starts it once the visitor is signed in", async () => {
    arriveWith("?journey=forge");
    const router = renderShell("/auth?journey=forge");
    expect(await screen.findByTestId("auth-screen")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/auth");
    expect(readLandingJourney()).toBe("forge");

    // The sign-in screen signs them in and returns them to the shell.
    act(() => useAuthStore.setState({ user: SIGNED_IN }));
    await act(() => router.navigate({ to: "/apps" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/forge/demo"));
    expect(searchOf(router).tour).toBe("forge:fg-intro");
  });

  it("leaves a tour that is already open alone, and forgets the choice", async () => {
    arriveWith("?journey=forge");
    const router = renderShell("/forge/demo?tour=forge:fg-trace&view=traces");
    await waitFor(() => expect(readLandingJourney()).toBeUndefined());
    expect(searchOf(router).tour).toBe("forge:fg-trace");
    expect(trackEvent).not.toHaveBeenCalledWith("journey_selected", expect.anything());
  });

  it("holds sign-in on any guarded page, not only the home", async () => {
    arriveWith("?journey=forge");
    const router = renderShell("/apps?journey=forge");
    await waitFor(() => expect(router.state.location.pathname).toBe("/forge/demo"));
    expect(screen.queryByTestId("auth-screen")).not.toBeInTheDocument();
  });

  it("does nothing without a landing journey", async () => {
    const router = renderShell("/");
    // No journey to take: the guard asks for sign-in as always.
    await waitFor(() => expect(router.state.location.pathname).toBe("/auth"));
    expect(trackEvent).not.toHaveBeenCalledWith("journey_selected", expect.anything());
  });

  it("never holds sign-in on the Reliant brand: the journey is the forge host's", async () => {
    resetShellForTests("reliant");
    arriveWith("?journey=forge");
    // routes.tsx mounts ForgeLandingJourney on the forge brand only.
    const router = renderShell("/apps?journey=forge", undefined, { landing: false });
    await waitFor(() => expect(router.state.location.pathname).toBe("/auth"));
    // And the page is kept across sign-in (returnHere).
    expect(router.state.location.search).toMatchObject({ redirect: "/apps?journey=forge" });
  });
});

describe("?journey=workflows on the forge brand", () => {
  it("never runs the workflows journey here", () => {
    expect(resolveTour(parseTourParam("workflows"))).toBeNull();
    expect(resolveTour(parseTourParam("workflows:wf-new"))).toBeNull();
    expect(resolveTour(parseTourParam("wf-new"))).toBeNull();
  });

  it("points at the Workflows area instead, in this app, and stays put", async () => {
    useAuthStore.setState({ user: SIGNED_IN });
    arriveWith("?journey=workflows");
    const router = renderShell("/apps?journey=workflows");
    const offer = await screen.findByTestId("forge-workflows-offer");
    expect(screen.getByRole("link", { name: "Open Workflows" })).toHaveAttribute("href", "/workflows/library");
    expect(router.state.location.pathname).toBe("/apps");
    expect(searchOf(router).tour).toBeUndefined();

    fireEvent.click(screen.getByRole("button", { name: "Stay here" }));
    expect(offer).not.toBeInTheDocument();
    expect(readLandingJourney()).toBeUndefined();
  });

  it("opens the Workflows area in place, never another origin", async () => {
    useAuthStore.setState({ user: SIGNED_IN });
    arriveWith("?journey=workflows");
    const router = renderShell("/apps?journey=workflows");
    fireEvent.click(await screen.findByRole("link", { name: "Open Workflows" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/library"));
    expect(screen.queryByTestId("forge-workflows-offer")).not.toBeInTheDocument();
    expect(readLandingJourney()).toBeUndefined();
    expect(trackEvent).toHaveBeenCalledWith("tour_link_clicked", { journey: "workflows", target: "workflows" });
  });
});

describe("the forge journey's end card on the forge brand", () => {
  it("offers the workflows journey as the Workflows area, opened in place", async () => {
    // Signed in: the Workflows area is behind sign-in, as every app page is.
    useAuthStore.setState({ user: SIGNED_IN });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    const router = renderShell("/forge/demo?tour=forge:fg-done", JourneyTour);
    const tryWorkflows = await screen.findByRole("button", { name: "Try AI workflows" });
    // The forge shell's CLI is `forge`, in both of the end card's command lists.
    const lists = screen.getAllByTestId("tour-commands");
    expect(lists).toHaveLength(2);
    expect(lists[0]).toHaveTextContent("forge scaffold service inventory");
    expect(lists[1]).toHaveTextContent("forge obs status prod");
    for (const list of lists) expect(list).not.toHaveTextContent("reliant forge");
    fireEvent.click(tryWorkflows);
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/library"));
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });
});
