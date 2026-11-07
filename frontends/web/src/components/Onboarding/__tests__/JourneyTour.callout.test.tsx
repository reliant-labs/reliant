/**
 * The compact controls (Journey.controls "callout", the forge journey): no
 * bottom bar and no Skip step. Each card has the step count, one Exit tour,
 * and Next only where there is no required action; a required action shows
 * its instruction and moves the tour on by itself once done. The callout sits
 * beside its target, never over it.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";

vi.mock("@/lib/analytics", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/lib/analytics")>()), trackEvent: vi.fn() }));

import { resetShellForTests } from "@/lib/shell";
import { useTourStore } from "@/store/tourStore";

import { JourneyTour } from "../JourneyTour";
import { DEMO_TRACE_ID } from "../journeys/forge";
import { retainTourParam } from "../tourParam";
import { memoryTourStorage, setTourShell, setTourStorage } from "../tourShell";
import { overlapArea } from "../calloutPlacement";

function Console() {
  return (
    <div>
      <div data-testid="obs-trace-row" data-trace-id={DEMO_TRACE_ID}>
        CreateOrder
      </div>
      <div data-testid="obs-overview">overview</div>
    </div>
  );
}

function renderTour(path: string) {
  const root = createRootRoute({
    component: () => (
      <>
        <Outlet />
        <JourneyTour />
      </>
    ),
    search: { middlewares: [retainTourParam as never] },
  });
  const demo = createRoute({ getParentRoute: () => root, path: "/forge/demo", component: Console, validateSearch: (s: Record<string, unknown>) => s });
  const router = createRouter({ routeTree: root.addChildren([demo]), history: createMemoryHistory({ initialEntries: [path] }) });
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  resetShellForTests("demo");
  setTourShell("forge");
  setTourStorage(memoryTourStorage());
  useTourStore.setState({ isInitialized: true });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  setTourShell(null);
  resetShellForTests();
});

describe("the forge journey's compact controls", () => {
  it("draws no bottom bar: the intro card carries the count, Exit tour and Next", async () => {
    renderTour("/forge/demo?view=overview&tour=forge:fg-intro");
    expect(await screen.findByTestId("tour-controls")).toBeInTheDocument();
    expect(screen.queryByTestId("onboarding-nav-bar")).toBeNull();
    expect(screen.queryByText("Skip step")).toBeNull();
    expect(screen.getByText("Step 1 of 9")).toBeInTheDocument();
    expect(screen.getByTestId("tour-exit")).toHaveTextContent("Exit tour");
    expect(screen.getByTestId("tour-next")).toHaveTextContent("Next");
  });

  it("a required action: its instruction, Next disabled, no Skip, and the tour moves on once it's done", async () => {
    const router = renderTour("/forge/demo?view=traces&tour=forge:fg-trace");
    expect(await screen.findByTestId("tour-action-hint")).toHaveTextContent("Open the CreateOrder trace at the top");
    expect(screen.getByTestId("tour-next")).toBeDisabled();
    expect(screen.getByTestId("tour-back")).toBeInTheDocument();
    expect(screen.queryByText("Skip step")).toBeNull();
    expect(screen.getByTestId("tour-exit")).toBeInTheDocument();
    await act(async () => {
      await router.navigate({ to: "/forge/demo", search: (prev: Record<string, unknown>) => ({ ...prev, trace: DEMO_TRACE_ID }) } as never);
    });
    await waitFor(() => expect((router.state.location.search as { tour?: string }).tour).toBe("forge:fg-hops"), { timeout: 3000 });
  });

  it("Exit tour closes it", async () => {
    const router = renderTour("/forge/demo?view=overview&tour=forge:fg-intro");
    fireEvent.click(await screen.findByTestId("tour-exit"));
    await waitFor(() => expect((router.state.location.search as { tour?: string }).tour).toBeUndefined());
  });

  it("puts the callout beside its target, never over it", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1440 });
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 900 });
    const rowRect = { top: 180, left: 260, width: 1160, height: 28, right: 1420, bottom: 208, x: 260, y: 180, toJSON: () => ({}) };
    const original = Element.prototype.getBoundingClientRect;
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      return this.getAttribute("data-testid") === "obs-trace-row" ? (rowRect as DOMRect) : original.call(this);
    });
    renderTour("/forge/demo?view=traces&tour=forge:fg-trace");
    const callout = await screen.findByTestId("tour-callout");
    // Placed below the full-width row (the card's default size, as jsdom measures nothing: 320 × 200).
    await waitFor(() => {
      expect(parseFloat(callout.style.top)).toBe(180 + 28 + 16);
      expect(overlapArea(rowRect, { top: parseFloat(callout.style.top), left: parseFloat(callout.style.left), width: 320, height: 200 })).toBe(0);
    });
    expect(callout.getAttribute("data-side")).toBe("bottom");
  });
});

describe("the callout controls are the engine's default, in every shell", () => {
  it.each([
    ["reliant", "reliant"],
    ["forge", "forge"],
    ["demo", "forge"],
  ] as const)("the %s shell: no bottom bar; Step X of Y, Back, Next (disabled until the action), Exit", async (shell, tourShell) => {
    resetShellForTests(shell);
    setTourShell(tourShell);
    renderTour("/forge/demo?view=traces&tour=forge:fg-trace");
    const controls = await screen.findByTestId("tour-controls");
    expect(screen.queryByTestId("onboarding-nav-bar")).toBeNull();
    expect(controls).toHaveTextContent(/Step \d+ of \d+/);
    expect(screen.getByTestId("tour-back")).toBeInTheDocument();
    expect(screen.getByTestId("tour-next")).toBeDisabled();
    expect(screen.getByTestId("tour-exit")).toHaveTextContent("Exit tour");
  });

  it("draws a bottom sheet on a phone in the forge shells, and defers in Reliant", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 390 });
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 844 });
    const matchMedia = window.matchMedia;
    window.matchMedia = ((query: string) => ({ matches: true, media: query, addEventListener() {}, removeEventListener() {} })) as never;
    try {
      renderTour("/forge/demo?view=traces&tour=forge:fg-trace");
      const callout = await screen.findByTestId("tour-callout");
      expect(callout).toHaveAttribute("data-side", "sheet");
      expect(callout.style.bottom).toBe("0px");
      cleanup();
      resetShellForTests("reliant");
      setTourShell("reliant");
      renderTour("/forge/demo?view=traces&tour=forge:fg-trace");
      expect(await screen.findByText("Open this on a computer")).toBeInTheDocument();
    } finally {
      window.matchMedia = matchMedia;
      Object.defineProperty(window, "innerWidth", { configurable: true, value: 1024 });
      Object.defineProperty(window, "innerHeight", { configurable: true, value: 768 });
    }
  });
});
