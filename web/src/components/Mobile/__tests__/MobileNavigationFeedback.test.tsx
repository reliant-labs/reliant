/**
 * Navigation feedback on the mobile surface: chunk warm-up and the pending bar.
 *
 * The router is stubbed because what matters is the contract with it: which
 * routes get `loadRouteChunk`, and when the bar shows for `isLoading`. The
 * measurement that motivated this (the first tap into a chat waiting on a
 * ~122 KB gzipped chunk with nothing on screen changing) lives in the module
 * comment.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";

const router = vi.hoisted(() => ({
  isLoading: false,
  loadRouteChunk: vi.fn(() => Promise.resolve()),
  routesById: {} as Record<string, unknown>,
}));

vi.mock("@tanstack/react-router", () => ({
  useRouter: () => router,
  useMatch: ({ select }: { select: (m: { routeId: string }) => string }) =>
    select({ routeId: "/_authenticated/_mobile" }),
  useRouterState: ({ select }: { select: (s: { isLoading: boolean }) => boolean }) =>
    select({ isLoading: router.isLoading }),
}));

const { MobileNavigationFeedback, PROGRESS_DELAY_MS, descendantRoutes } = await import(
  "../MobileNavigationFeedback"
);

const chat = { id: "chat", children: [{ id: "chat-workflow" }] };
const list = { id: "list" };
const shell = { id: "/_authenticated/_mobile", children: [list, chat] };
const unrelated = { id: "/settings" };

beforeEach(() => {
  vi.useFakeTimers();
  router.isLoading = false;
  router.loadRouteChunk.mockClear();
  router.routesById = { [shell.id]: shell, [unrelated.id]: unrelated };
  Object.defineProperty(navigator, "onLine", { value: true, configurable: true });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("descendantRoutes", () => {
  it("walks every screen under a layout, nested ones included", () => {
    expect(descendantRoutes(shell).map((r) => r.id)).toEqual(["list", "chat", "chat-workflow"]);
  });

  it("is empty for a missing or childless route", () => {
    expect(descendantRoutes(undefined)).toEqual([]);
    expect(descendantRoutes(list)).toEqual([]);
  });
});

describe("MobileNavigationFeedback", () => {
  it("warms every mobile screen's chunk once idle, and nothing else", () => {
    render(<MobileNavigationFeedback />);
    expect(router.loadRouteChunk).not.toHaveBeenCalled();

    act(() => {
      vi.runAllTimers();
    });
    const warmed = router.loadRouteChunk.mock.calls.map(
      ([route]) => (route as unknown as { id: string }).id,
    );
    expect(warmed).toEqual(["list", "chat", "chat-workflow"]);
  });

  it("does not spend the warm-up while offline", () => {
    Object.defineProperty(navigator, "onLine", { value: false, configurable: true });
    render(<MobileNavigationFeedback />);
    act(() => {
      vi.runAllTimers();
    });
    expect(router.loadRouteChunk).not.toHaveBeenCalled();
  });

  it("shows the bar only for a navigation that is still pending after the delay", () => {
    router.isLoading = true;
    const { rerender } = render(<MobileNavigationFeedback />);
    // A warm navigation finishes inside the delay and never flashes the bar.
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(PROGRESS_DELAY_MS);
    });
    expect(screen.getByRole("progressbar")).toBeInTheDocument();

    router.isLoading = false;
    rerender(<MobileNavigationFeedback />);
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });
});
