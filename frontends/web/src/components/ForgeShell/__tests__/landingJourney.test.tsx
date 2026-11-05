// Copyright (c) 2025 Reliant Labs

/**
 * The landing page's `?journey=` on the forge shell (landingJourney.ts,
 * ForgeLandingJourney): `forge` starts the forge journey — on the public demo
 * for a signed-out visitor, on arrival or after sign-in for an account — and
 * `workflows` never runs here but links to Reliant, at the runtime config's
 * origin. Driven through a real router and the real auth guard.
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
    useAuthStore: create<{ user: User | null; loading: boolean; initialized: boolean; initialize: () => Promise<void> }>(() => ({
      user: null,
      loading: false,
      initialized: true,
      initialize: async () => undefined,
    })),
  };
});
vi.mock("@/components/Forge/Demo/DemoConsole", () => ({ DemoConsole: () => <div data-testid="demo-console" /> }));
vi.mock("@/components/Layout/LoadingSpinner", () => ({ LoadingSpinner: () => <div data-testid="spinner" /> }));

import { createForgeDemoRedirectRoute, createForgeDemoRoute } from "@/components/Forge/Demo/demoRoute";
import { JourneyTour } from "@/components/Onboarding/JourneyTour";
import { resolveTour } from "@/components/Onboarding/journeys/registry";
import { parseTourParam } from "@/components/Onboarding/tourParam";
import { localStorageTourStorage, setTourShell, setTourStorage, settingsTourStorage } from "@/components/Onboarding/tourShell";
import { holdLandingJourney, readLandingJourney } from "@/lib/landingAttribution";
import { useAuthStore } from "@/store/authStore";
import { useTourStore } from "@/store/tourStore";
import { setRuntimeConfig } from "@/test/runtimeConfig";

import { ForgeAuthGuard } from "../ForgeAuthGuard";
import { ForgeLandingJourney } from "../ForgeLandingJourney";
import { hasTourParam, landingJourneyAction, landingJourneyHoldsSignIn, type LandingJourneyInputs } from "../landingJourney";

type AuthUser = NonNullable<ReturnType<typeof useAuthStore.getState>["user"]>;
const SIGNED_IN = { id: "u1", email_confirmed_at: "2026-11-01T00:00:00Z" } as AuthUser;

/** The forge shell's shape: public sign-in and demo, the shell's pages behind ForgeAuthGuard. */
function renderShell(path: string, extra: ComponentType = () => null) {
  const Extra = extra;
  const root = createRootRoute({
    component: () => (
      <>
        <Outlet />
        <ForgeLandingJourney />
        <Extra />
      </>
    ),
  });
  const guarded = createRoute({
    getParentRoute: () => root,
    id: "_authenticated",
    component: () => (
      <ForgeAuthGuard>
        <Outlet />
      </ForgeAuthGuard>
    ),
  });
  const router = createRouter({
    routeTree: root.addChildren([
      createRoute({ getParentRoute: () => root, path: "/", component: () => <Navigate to="/apps" replace /> }),
      createRoute({ getParentRoute: () => root, path: "/auth", component: () => <div data-testid="auth-screen" /> }),
      createForgeDemoRoute(() => root),
      createForgeDemoRedirectRoute(() => root),
      guarded.addChildren([createRoute({ getParentRoute: () => guarded, path: "/apps", component: () => <div data-testid="apps-page" /> })]),
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
  setTourShell("forge");
  setTourStorage(localStorageTourStorage("forge.tour.v1"));
  useTourStore.setState({ isInitialized: true });
  useAuthStore.setState({ user: null, loading: false, initialized: true });
  trackEvent.mockClear();
});

afterEach(() => {
  setTourShell(null);
  setTourStorage(settingsTourStorage);
  window.localStorage.clear();
});

describe("landingJourneyAction", () => {
  const base: LandingJourneyInputs = { held: "forge", tourOpen: false, forgeAvailable: true, onDemo: false, guarded: true, auth: "allow" };

  it.each<[Partial<LandingJourneyInputs>, string]>([
    [{ held: undefined }, "none"],
    [{ held: "workflows" }, "offer-reliant"],
    [{ held: "workflows", tourOpen: true }, "offer-reliant"],
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

describe("?journey=forge on the forge shell", () => {
  it("takes a signed-out visitor straight to the forge journey on the public demo, not to sign-in", async () => {
    arriveWith("?journey=forge&rid=r1");
    const router = renderShell("/?journey=forge&rid=r1");
    await waitFor(() => expect(router.state.location.pathname).toBe("/forge/demo"));
    expect(searchOf(router)).toMatchObject({ tour: "forge:fg-console", view: "overview" });
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
    expect(searchOf(router).tour).toBe("forge:fg-console");
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
    expect(searchOf(router).tour).toBe("forge:fg-console");
  });

  it("leaves a tour that is already open alone, and forgets the choice", async () => {
    arriveWith("?journey=forge");
    const router = renderShell("/forge/demo?tour=forge:fg-trace&view=traces");
    await waitFor(() => expect(readLandingJourney()).toBeUndefined());
    expect(searchOf(router).tour).toBe("forge:fg-trace");
    expect(trackEvent).not.toHaveBeenCalledWith("journey_selected", expect.anything());
  });

  it("does nothing without a landing journey", async () => {
    const router = renderShell("/");
    // No journey to take: the guard asks for sign-in as always.
    await waitFor(() => expect(router.state.location.pathname).toBe("/auth"));
    expect(trackEvent).not.toHaveBeenCalledWith("journey_selected", expect.anything());
  });
});

describe("?journey=workflows on the forge shell", () => {
  it("never runs the workflows journey here", () => {
    expect(resolveTour(parseTourParam("workflows"))).toBeNull();
    expect(resolveTour(parseTourParam("workflows:wf-new"))).toBeNull();
    expect(resolveTour(parseTourParam("wf-new"))).toBeNull();
  });

  it("offers Reliant instead, at the runtime config's origin, and stays put", async () => {
    setRuntimeConfig({ APP_URL: "https://app.staging.test/" });
    useAuthStore.setState({ user: SIGNED_IN });
    arriveWith("?journey=workflows");
    const router = renderShell("/apps?journey=workflows");
    const offer = await screen.findByTestId("forge-workflows-offer");
    expect(screen.getByRole("link", { name: "Open Reliant" })).toHaveAttribute("href", "https://app.staging.test/?journey=workflows");
    expect(router.state.location.pathname).toBe("/apps");
    expect(searchOf(router).tour).toBeUndefined();

    fireEvent.click(screen.getByRole("button", { name: "Stay here" }));
    expect(offer).not.toBeInTheDocument();
    expect(readLandingJourney()).toBeUndefined();
  });

  it("links to app.reliantlabs.io when the runtime config names no Reliant origin", async () => {
    arriveWith("?journey=workflows");
    renderShell("/forge/demo?journey=workflows");
    await screen.findByTestId("forge-workflows-offer");
    expect(screen.getByRole("link", { name: "Open Reliant" })).toHaveAttribute("href", "https://app.reliantlabs.io/?journey=workflows");
  });
});

describe("the forge journey's end card in the forge shell", () => {
  it("offers the workflows journey as a link to Reliant, from the runtime config", async () => {
    setRuntimeConfig({ APP_URL: "https://app.staging.test" });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    renderShell("/forge/demo?tour=forge:fg-done", JourneyTour);
    fireEvent.click(await screen.findByRole("button", { name: "Try AI workflows in Reliant" }));
    expect(open).toHaveBeenCalledWith("https://app.staging.test/?journey=workflows", "_blank", "noopener");
    // The forge shell's CLI is `forge`, in both of the end card's command lists.
    const lists = screen.getAllByTestId("tour-commands");
    expect(lists).toHaveLength(2);
    expect(lists[0]).toHaveTextContent("forge scaffold service inventory");
    expect(lists[1]).toHaveTextContent("forge obs status prod");
    for (const list of lists) expect(list).not.toHaveTextContent("reliant forge");
    open.mockRestore();
  });
});
