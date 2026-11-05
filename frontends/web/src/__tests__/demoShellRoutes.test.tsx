// Copyright (c) 2025 Reliant Labs

/**
 * The demo shell (demo.reliantlabs.io): its route module boots the tour into
 * memory-only storage, the tree is one page — every path lands on
 * `/forge/demo`, query kept — and the forge tour starts once per page load,
 * never over a deep link, never from storage.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";

const tracked = vi.hoisted(() => [] as { event: string; props?: Record<string, unknown> }[]);
const telemetry = vi.hoisted(() => ({ initTelemetry: vi.fn(async () => false) }));
vi.mock("@/lib/telemetry", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/lib/telemetry")>()), initTelemetry: telemetry.initTelemetry }));
vi.mock("@/lib/analytics", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/analytics")>()),
  trackEvent: (event: string, props?: Record<string, unknown>) => tracked.push({ event, props }),
}));
// The root's other residents read stores and the network; they are not what this tests.
vi.mock("@/components/PageVisitTracker", () => ({ PageVisitTracker: () => null }));
vi.mock("@/components/Modals/ModalLayer", () => ({ ModalLayer: () => null }));
vi.mock("@/components/NewBuildPrompt", () => ({ NewBuildPrompt: () => null }));
vi.mock("@/lib/toast", () => ({ Toaster: () => null }));
vi.mock("@/components/Onboarding/JourneyTour", () => ({ JourneyTour: () => <div data-testid="journey-tour" /> }));
vi.mock("@/components/Forge/Demo/DemoConsole", () => ({ DemoConsole: () => <div data-testid="demo-console" /> }));

import { getTourShell, getTourStorage } from "@/components/Onboarding/tourShell";
import { resetDemoAutoStartForTests } from "@/components/Forge/Demo/DemoAutoStart";
import { resetShellForTests } from "@/lib/shell";
import { buildDemoShellTree } from "@/demoShellRoutes";

function renderDemoShell(path: string) {
  const router = createRouter({ routeTree: buildDemoShellTree(), history: createMemoryHistory({ initialEntries: [path] }) });
  render(<RouterProvider router={router} />);
  return router;
}

const searchOf = (router: ReturnType<typeof renderDemoShell>) => router.state.location.search as Record<string, unknown>;

beforeEach(() => {
  resetShellForTests("demo");
  resetDemoAutoStartForTests();
  tracked.length = 0;
  window.localStorage.clear();
});

afterEach(() => {
  cleanup();
  resetShellForTests();
});

describe("the demo shell's boot", () => {
  it("runs the tour as the forge shell, keeps progress in memory only, and starts telemetry", async () => {
    // Evaluating the route module is the boot.
    expect(getTourShell()).toBe("forge");
    expect(telemetry.initTelemetry).toHaveBeenCalled();
    const storage = getTourStorage();
    expect(storage.kind).toBe("local");
    await storage.set("onboarding.tour.forge.v2.completed", "completed");
    expect(await storage.get("onboarding.tour.forge.v2.completed")).toBe("completed");
    // Nothing reached the browser's storage: the next visit starts fresh.
    expect(window.localStorage.length).toBe(0);
  });
});

describe("the demo shell's routes", () => {
  it.each([
    ["/", "/forge/demo"],
    ["/demo", "/forge/demo"],
    ["/auth", "/forge/demo"],
    ["/oauth/cli?port=1", "/forge/demo"],
    ["/claim", "/forge/demo"],
    ["/apps", "/forge/demo"],
    ["/forge/p/shop/env/prod", "/forge/demo"],
    ["/workflows", "/forge/demo"],
  ])("sends %s to the demo console", async (path, landed) => {
    const router = renderDemoShell(path);
    expect(await screen.findByTestId("demo-console")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe(landed);
    expect(screen.getByTestId("journey-tour")).toBeInTheDocument();
    expect(document.querySelector("[data-shell='demo']")).not.toBeNull();
  });

  it("keeps the query across the redirect (a landing link's rid and utm_*)", async () => {
    const router = renderDemoShell("/?rid=abcdefgh1234&utm_source=landing&tour=forge:fg-trace");
    await screen.findByTestId("demo-console");
    expect(router.state.location.pathname).toBe("/forge/demo");
    expect(router.state.location.searchStr).toContain("rid=abcdefgh1234");
    expect(router.state.location.searchStr).toContain("utm_source=landing");
  });
});

describe("the tour on every visit", () => {
  it("starts the forge journey on load, from the demo source", async () => {
    const router = renderDemoShell("/");
    await screen.findByTestId("demo-console");
    await waitFor(() => expect(searchOf(router).tour).toBe("forge:fg-console"));
    expect(tracked).toContainEqual({ event: "demo_tour_autostarted", props: { deep_link: false } });
    expect(tracked).toContainEqual(expect.objectContaining({ event: "journey_selected", props: expect.objectContaining({ source: "demo" }) }));
  });

  it("leaves a deep link alone", async () => {
    const router = renderDemoShell("/forge/demo?view=traces&tour=forge:fg-trace");
    await screen.findByTestId("demo-console");
    expect(tracked).toContainEqual({ event: "demo_tour_autostarted", props: { deep_link: true } });
    expect(searchOf(router).tour).toBe("forge:fg-trace");
  });

  it("starts once per page load: a remount after the tour ended does not restart it", async () => {
    const first = renderDemoShell("/");
    await waitFor(() => expect(searchOf(first).tour).toBe("forge:fg-console"));
    cleanup();
    // Same page load (module state kept), the tour finished (no ?tour=): it stays closed.
    const second = renderDemoShell("/forge/demo");
    await screen.findByTestId("demo-console");
    // render() flushes effects inside act, so the remount's DemoAutoStart has
    // already run by now; it records demo_tour_autostarted before it would
    // start the tour, so a single event proves it declined.
    expect(tracked.filter((t) => t.event === "demo_tour_autostarted")).toHaveLength(1);
    expect(searchOf(second).tour).toBeUndefined();
  });
});
