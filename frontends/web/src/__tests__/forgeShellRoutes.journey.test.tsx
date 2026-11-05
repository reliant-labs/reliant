// Copyright (c) 2025 Reliant Labs

/**
 * The forge shell's route module and the journey engine: its boot points the
 * tour at this shell and at localStorage, the tree mounts `/forge/demo` and
 * `/demo` OUTSIDE the auth guard (the demo is public), and the root renders
 * <JourneyTour /> on every page, the public demo included.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";
import type { ReactNode } from "react";

// The root's other residents read stores and the network; they are not what this tests.
vi.mock("@/components/Orgs/ActiveOrgBinding", () => ({ ActiveOrgBinding: () => null }));
vi.mock("@/components/PageVisitTracker", () => ({ PageVisitTracker: () => null }));
vi.mock("@/components/Modals/ModalLayer", () => ({ ModalLayer: () => null }));
vi.mock("@/components/NewBuildPrompt", () => ({ NewBuildPrompt: () => null }));
vi.mock("@/lib/toast", () => ({ Toaster: () => null }));
vi.mock("@/components/Onboarding/JourneyTour", () => ({ JourneyTour: () => <div data-testid="journey-tour" /> }));
vi.mock("@/components/ForgeShell/ForgeLandingJourney", () => ({ ForgeLandingJourney: () => null }));
vi.mock("@/components/Forge/Demo/DemoConsole", () => ({ DemoConsole: () => <div data-testid="demo-console" /> }));
vi.mock("@/components/ForgeShell/ForgeAppsPage", () => ({ ForgeAppsPage: () => <div data-testid="apps-page" /> }));
vi.mock("@/components/ForgeShell/ForgeShellLayout", async () => {
  const { Outlet } = await import("@tanstack/react-router");
  return { ForgeShellLayout: () => <Outlet /> };
});

import { getTourShell, getTourStorage, setTourShell } from "@/components/Onboarding/tourShell";
import { FORGE_TOUR_STORAGE_KEY } from "@/components/ForgeShell/forgeTourBoot";
import { buildForgeShellTree } from "@/forgeShellRoutes";

/** A guard that never lets anyone through: whatever renders past it is public. */
function Locked(_: { children: ReactNode }) {
  return <div data-testid="guard-locked" />;
}

function renderShell(path: string) {
  const router = createRouter({
    routeTree: buildForgeShellTree({ Guard: Locked }),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

afterEach(() => {
  window.localStorage.clear();
});

describe("the forge shell's boot", () => {
  it("points the tour at the forge shell, with progress in this origin's localStorage", async () => {
    // Evaluating the route module is the boot (forgeShellRoutes.tsx, beside captureForgeEntry).
    expect(getTourShell()).toBe("forge");
    const storage = getTourStorage();
    expect(storage.kind).toBe("local");
    await storage.set("onboarding.tour.forge.v2.completed", "completed");
    expect(JSON.parse(window.localStorage.getItem(FORGE_TOUR_STORAGE_KEY) ?? "{}")).toEqual({
      "onboarding.tour.forge.v2.completed": "completed",
    });
    // The override, not the hostname, decides: jsdom is on localhost.
    expect(window.location.hostname).not.toBe("forge.reliantlabs.io");
    setTourShell(null);
    expect(getTourShell()).toBe("reliant");
    setTourShell("forge");
  });
});

describe("the forge shell's routes", () => {
  it("serves /forge/demo to anyone: the guard never runs", async () => {
    const router = renderShell("/forge/demo?tour=forge:fg-console");
    expect(await screen.findByTestId("demo-console")).toBeInTheDocument();
    expect(screen.queryByTestId("guard-locked")).not.toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/forge/demo");
    expect((router.state.location.search as { tour?: string }).tour).toBe("forge:fg-console");
  });

  it("sends /demo to /forge/demo, keeping the query", async () => {
    const router = renderShell("/demo?tour=forge&view=traces");
    expect(await screen.findByTestId("demo-console")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/forge/demo");
    expect(router.state.location.search).toMatchObject({ tour: "forge", view: "traces" });
    expect(screen.queryByTestId("guard-locked")).not.toBeInTheDocument();
  });

  it("is not shadowed by the guarded console: other /forge/ pages still meet the guard", async () => {
    renderShell("/forge/p/shop/environments");
    expect(await screen.findByTestId("guard-locked")).toBeInTheDocument();
    expect(screen.queryByTestId("demo-console")).not.toBeInTheDocument();
  });

  it("keeps the shell's own pages behind the guard", async () => {
    renderShell("/apps");
    expect(await screen.findByTestId("guard-locked")).toBeInTheDocument();
    expect(screen.queryByTestId("apps-page")).not.toBeInTheDocument();
  });

  it("mounts the journey tour at the root, so it runs on the public demo", async () => {
    renderShell("/forge/demo");
    expect(await screen.findByTestId("demo-console")).toBeInTheDocument();
    expect(screen.getByTestId("journey-tour")).toBeInTheDocument();
  });

  it("mounts the journey tour on guarded pages too", async () => {
    renderShell("/apps");
    expect(await screen.findByTestId("guard-locked")).toBeInTheDocument();
    expect(screen.getByTestId("journey-tour")).toBeInTheDocument();
  });
});
