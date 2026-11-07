// Copyright (c) 2025 Reliant Labs

/**
 * The demo shell (demo.reliantlabs.io): its route module boots the tour into
 * memory-only storage; every path it has no page for lands on the sample app's
 * console (`/forge/p/shop`), query kept, whose pages are all mounted; and a
 * compact card offers the optional forge tour, which never starts by itself.
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
vi.mock("@/components/Forge/Demo/DemoConsole", () => ({ DemoConsole: () => <main data-testid="demo-console" /> }));
// The console's pages, one test id each; the layouts pass through.
vi.mock("@/forgeConsolePages", async () => {
  const { Outlet } = await import("@tanstack/react-router");
  const { useObsReadOnly } = await import("@/components/Forge/Observability/obsReadOnly");
  const page = (name: string) => () => {
    const { readOnly, reason } = useObsReadOnly();
    return <main data-testid={`console-${name}`} data-read-only={readOnly} data-read-only-reason={reason} />;
  };
  const names = ["overview", "workload", "releases", "release", "observabilityView", "dashboards", "dashboard", "storage", "environments", "environment", "secrets", "domains", "activity", "orgObservability", "landing", "legacyEnv", "legacyDomains", "legacyCommand"];
  return { forgeConsolePages: { layout: Outlet, console: Outlet, orgLayout: Outlet, ...Object.fromEntries(names.map((n) => [n, page(n)])) } };
});
vi.mock("@/demoBundle/DemoConsoleProviders", () => ({ DemoConsoleProviders: ({ children }: { children: React.ReactNode }) => children }));
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
  it.each(["/", "/auth", "/oauth/cli?port=1", "/claim", "/apps", "/forge/p/shop/env/prod", "/workflows"])(
    "sends %s to the sample app's console overview",
    async (path) => {
      const router = renderDemoShell(path);
      expect(await screen.findByTestId("console-overview")).toBeInTheDocument();
      expect(router.state.location.pathname).toBe("/forge/p/shop");
      expect(screen.getByTestId("journey-tour")).toBeInTheDocument();
      expect(document.querySelector("[data-shell='demo']")).not.toBeNull();
    },
  );

  it.each([
    ["/forge/p/shop/releases", "console-releases"],
    ["/forge/p/shop/environments", "console-environments"],
    ["/forge/p/shop/environments/prod", "console-environment"],
    ["/forge/p/shop/secrets", "console-secrets"],
    ["/forge/p/shop/domains", "console-domains"],
    ["/forge/p/shop/activity", "console-activity"],
    ["/forge/p/shop/storage", "console-storage"],
    ["/forge/p/shop/observability/traces", "console-observabilityView"],
    ["/forge/p/shop/observability/dashboards", "console-dashboards"],
  ])("mounts the console page at %s", async (path, testId) => {
    const router = renderDemoShell(path);
    const page = await screen.findByTestId(testId);
    expect(page).toHaveAttribute("data-read-only", "true");
    expect(page).toHaveAttribute("data-read-only-reason", "Read-only demo");
    expect(router.state.location.pathname).toBe(path);
  });

  it("keeps /forge/demo (the tour's console) and sends /demo there", async () => {
    const router = renderDemoShell("/demo?view=traces");
    expect(await screen.findByTestId("demo-console")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/forge/demo");
  });

  it("preserves a legacy logs tour URL without sending it to auth or a prerequisite", async () => {
    const router = renderDemoShell("/forge/demo?shell=demo&view=logs&tour=forge:fg-logs");
    await screen.findByTestId("demo-console");
    expect(router.state.location.pathname).toBe("/forge/demo");
    expect(searchOf(router)).toMatchObject({ shell: "demo", view: "logs", tour: "forge:fg-logs" });
    expect(screen.queryByTestId("demo-tip")).toBeNull();
  });

  it("keeps the query across the redirect (a landing link's rid and utm_*)", async () => {
    const router = renderDemoShell("/?rid=abcdefgh1234&utm_source=landing&tour=forge:fg-trace");
    await screen.findByTestId("console-overview");
    expect(router.state.location.pathname).toBe("/forge/p/shop");
    expect(router.state.location.searchStr).toContain("rid=abcdefgh1234");
    expect(router.state.location.searchStr).toContain("utm_source=landing");
  });
});

describe("explore first: the tour is offered, never started", () => {
  it("opens with one compact page card, not a competing toast or automatic tour", async () => {
    const router = renderDemoShell("/");
    await screen.findByTestId("console-overview");
    await waitFor(() => expect(screen.getByTestId("demo-tip")).toBeInTheDocument());
    expect(searchOf(router).tour).toBeUndefined();
    expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
  });

  it("starts the optional tour only from Take the tour", async () => {
    const router = renderDemoShell("/");
    await screen.findByTestId("console-overview");
    await waitFor(() => expect(screen.getByTestId("demo-page-tour-start")).toBeInTheDocument());
    fireEvent.click(screen.getByTestId("demo-page-tour-start"));
    await waitFor(() => expect(searchOf(router).tour).toBe("forge:fg-intro"));
    expect(tracked).toContainEqual({ event: "demo_page_tour_started", props: { tip: "overview" } });
    expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
  });

  it("hides the toast over a deep link to a step, and leaves the link alone", async () => {
    const router = renderDemoShell("/forge/demo?view=traces&tour=forge:fg-trace");
    await screen.findByTestId("demo-console");
    expect(screen.queryByTestId("demo-tour-toast")).toBeNull();
    expect(searchOf(router).tour).toBe("forge:fg-trace");
  });
});
