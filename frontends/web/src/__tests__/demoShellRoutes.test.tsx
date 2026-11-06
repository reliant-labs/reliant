// Copyright (c) 2025 Reliant Labs

/**
 * The demo shell (demo.reliantlabs.io): its route module boots the tour into
 * memory-only storage, the tree is one page — every path lands on
 * `/forge/demo`, query kept — and it opens on the console: a toast offers the
 * forge tour, which never starts by itself.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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
// The boot installs the demo transport through a lazy import() whose fixture
// chain (sample metrics, traces, scenarios) is still loading when the file's
// tests are done, and vitest then fails the run with EnvironmentTeardownError
// ("Cannot load …metricsTypes.ts … after the environment was torn down"),
// intermittently. DemoConsole is mocked above, so nothing here reads that data:
// the transport is mocked too, and nothing loads after teardown.
vi.mock("@/components/Forge/Demo/demoData/demoTransport", () => ({ createDemoTransport: () => ({}) }));

import { getTourShell, getTourStorage } from "@/components/Onboarding/tourShell";
import { resetDemoSessionForTests } from "@/components/Forge/Demo/demoSession";
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
  resetDemoSessionForTests();
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

describe("explore first: the tour is offered, never started", () => {
  it("opens on the console with no tour, and a toast that offers it", async () => {
    const router = renderDemoShell("/");
    await screen.findByTestId("demo-console");
    expect(await screen.findByTestId("demo-tour-toast")).toBeInTheDocument();
    expect(searchOf(router).tour).toBeUndefined();
    expect(tracked).toContainEqual({ event: "demo_tour_offered", props: {} });
  });

  it("starts the forge journey from the toast, from the demo source", async () => {
    const router = renderDemoShell("/");
    fireEvent.click(await screen.findByTestId("demo-tour-toast-start"));
    await waitFor(() => expect(searchOf(router).tour).toBe("forge:fg-intro"));
    expect(tracked).toContainEqual(expect.objectContaining({ event: "journey_selected", props: expect.objectContaining({ source: "demo" }) }));
    expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
  });

  it("hides the toast over a deep link to a step, and leaves the link alone", async () => {
    const router = renderDemoShell("/forge/demo?view=traces&tour=forge:fg-trace");
    await screen.findByTestId("demo-console");
    expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
    expect(searchOf(router).tour).toBe("forge:fg-trace");
  });
});
