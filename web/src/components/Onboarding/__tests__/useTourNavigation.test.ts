/**
 * Tests for the URL-driven tour navigation hook.
 *
 * Contract (see refactor doc):
 *   - `?tour=<step-id>` is the source of truth for which tour step is active.
 *   - The hook exposes `currentStepId`, `isWizardActive`, and four URL-driving
 *     actions: `goToStep`, `exitTour`, `completeAndAdvance`, `goBack`, plus
 *     `skipAll` for ending the tour with no completion.
 *   - The hook never reads tour state from the store; it just calls the
 *     store's persistence methods (`completeStep`, `skipStep`, etc.) as a
 *     side effect of URL transitions.
 *
 * The impl agent has not landed yet, so several of these tests will fail
 * until the hook + route schemas are added. That's expected — these tests
 * are the contract.
 */
/* eslint-disable @typescript-eslint/no-explicit-any */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import React from "react";
import { render, act, waitFor } from "@testing-library/react";
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { z } from "zod";

// ─── Mocks ────────────────────────────────────────────────────────────────────
// We mock the tour store so the hook's persistence side-effects (completeStep,
// markTourCompleted, etc.) can be observed without touching real settings RPCs.

const tourStoreMocks = vi.hoisted(() => ({
  completeStep: vi.fn(async () => undefined),
  skipStep: vi.fn(async () => undefined),
  markTourCompleted: vi.fn(async () => undefined),
}));

vi.mock("../../../store/tourStore", () => {
  function getState() {
    return {
      completedSteps: new Set<string>(),
      skippedSteps: new Set<string>(),
      hasCompletedOnboarding: false,
      completeStep: tourStoreMocks.completeStep,
      skipStep: tourStoreMocks.skipStep,
      markTourCompleted: tourStoreMocks.markTourCompleted,
    };
  }
  const storeApi = Object.assign(
    (selector: any) => (selector ? selector(getState()) : getState()),
    {
      getState,
      setState: vi.fn(),
      subscribe: vi.fn(() => () => undefined),
    }
  );
  return { useTourStore: storeApi };
});

// chatStore is used by useTourNavigation's skipAll / completeAndAdvance
// to clear the active chat once the tour ends.
vi.mock("../../../store/chatStore", () => ({
  useChatStore: {
    getState: () => ({ clearCurrentChat: vi.fn() }),
  },
}));

// apiKeySetupStore is dynamically imported by useTourNavigation; ensure the
// dynamic import resolves without touching real RPCs.
vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: {
    getState: () => ({
      hasApiKey: true,
      ensureApiKeyOrShowModal: vi.fn(async () => undefined),
    }),
    setState: vi.fn(),
  },
}));

// ─── Test router ─────────────────────────────────────────────────────────────

const tourSearchSchema = z.object({
  tour: z.string().optional(),
  drill: z.string().optional(),
});

function buildRouter(initialEntries: string[], HookHarness: React.ComponentType) {
  const rootRoute = createRootRoute({
    component: () =>
      React.createElement(
        React.Fragment,
        null,
        React.createElement(Outlet),
        React.createElement(HookHarness)
      ),
  });

  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    validateSearch: tourSearchSchema,
    component: () => null,
  });
  const projectRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/project/$projectId",
    validateSearch: tourSearchSchema,
    component: () => null,
  });
  const workflowHubRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/workflow",
    validateSearch: tourSearchSchema,
    component: () => null,
  });
  const workflowNewRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/workflow/new",
    validateSearch: tourSearchSchema,
    component: () => null,
  });
  const workflowBuilderRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/workflow/$workflowName",
    validateSearch: tourSearchSchema,
    component: () => null,
  });

  const tree = rootRoute.addChildren([
    indexRoute,
    projectRoute,
    workflowHubRoute,
    workflowNewRoute,
    workflowBuilderRoute,
  ]);
  return createRouter({
    routeTree: tree as any,
    history: createMemoryHistory({ initialEntries }),
  });
}

// Render a hook in a router context. Returns a ref to the latest hook value
// and the router for asserting on location.
function renderHookWithRouter<T>(
  hookFn: () => T,
  initialEntries: string[]
): {
  result: { current: T | undefined };
  router: ReturnType<typeof buildRouter>;
} {
  const result: { current: T | undefined } = { current: undefined };

  function HookHarness() {
    result.current = hookFn();
    return null;
  }

  const router = buildRouter(initialEntries, HookHarness);
  render(React.createElement(RouterProvider as any, { router }));
  return { result, router };
}

// ─── Lazy hook import ────────────────────────────────────────────────────────

async function loadHook(): Promise<((..._args: any[]) => any) | null> {
  try {
    const mod = await import("../useTourNavigation");
    return (mod as any).useTourNavigation ?? null;
  } catch {
    return null;
  }
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("useTourNavigation", () => {
  beforeEach(() => {
    tourStoreMocks.completeStep.mockClear();
    tourStoreMocks.skipStep.mockClear();
    tourStoreMocks.markTourCompleted.mockClear();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("currentStepId / isWizardActive", () => {
    it("reads currentStepId from the ?tour search param", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/?tour=workflow-hub"]
      );
      await waitFor(() =>
        expect(result.current).toBeDefined()
      );
      expect(result.current.currentStepId).toBe("workflow-hub");
      expect(result.current.isWizardActive).toBe(true);
    });

    it("returns null currentStepId when no ?tour is present", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result } = renderHookWithRouter(() => useTourNavigation(), ["/"]);
      await waitFor(() => expect(result.current).toBeDefined());
      expect(result.current.currentStepId).toBeNull();
      expect(result.current.isWizardActive).toBe(false);
    });

    it("treats an unknown ?tour value as absent (currentStepId = null)", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/?tour=not-a-step"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      expect(result.current.currentStepId).toBeNull();
      expect(result.current.isWizardActive).toBe(false);
    });
  });

  describe("goToStep", () => {
    it("navigates to /workflow?tour=workflow-hub for the workflow-hub step", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.goToStep("workflow-hub");
      });
      expect(router.state.location.pathname).toBe("/workflow");
      expect(router.state.location.search).toMatchObject({
        tour: "workflow-hub",
      });
    });

    it("navigates to the encoded builder URL with drill=attempt for workflow-builder", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.goToStep("workflow-builder");
      });
      // pathname should resolve to the encoded builtin workflow name
      expect(router.state.location.pathname).toMatch(
        /^\/workflow\/builtin(:|%3A)/i
      );
      expect(router.state.location.search).toMatchObject({
        drill: "attempt",
        tour: "workflow-builder",
      });
    });

    it("navigates to the builder for workflow-builder-chat too", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.goToStep("workflow-builder-chat");
      });
      expect(router.state.location.pathname).toMatch(
        /^\/workflow\/builtin(:|%3A)/i
      );
      expect(router.state.location.search).toMatchObject({
        drill: "attempt",
        tour: "workflow-builder-chat",
      });
    });

    it("stays on the current pathname for chat-and-sidebars on /project/abc", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/project/abc"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.goToStep("chat-and-sidebars");
      });
      expect(router.state.location.pathname).toBe("/project/abc");
      expect(router.state.location.search).toMatchObject({
        tour: "chat-and-sidebars",
      });
    });

    it("leaves /workflow when transitioning to workspaces step", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/workflow?tour=workflow-hub"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.goToStep("workspaces");
      });
      expect(router.state.location.search).toMatchObject({
        tour: "workspaces",
      });
      // Allow either: hook stays on /workflow updating only search, OR
      // hook bounces to the chat route. Both are acceptable per the contract.
    });
  });

  describe("exitTour", () => {
    it("drops the ?tour param while preserving the pathname", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/workflow?tour=workflow-hub"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.exitTour();
      });
      expect(router.state.location.pathname).toBe("/workflow");
      expect(router.state.location.search.tour).toBeUndefined();
    });
  });

  describe("goBack", () => {
    it("steps the ?tour param backwards (workspaces → chat-and-sidebars)", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/?tour=workspaces"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.goBack();
      });
      expect(router.state.location.search).toMatchObject({
        tour: "chat-and-sidebars",
      });
    });

    it("exits the tour when called from the first step", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/?tour=chat-and-sidebars"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.goBack();
      });
      expect(router.state.location.search.tour).toBeUndefined();
    });
  });

  describe("completeAndAdvance", () => {
    it("marks the current step complete in the store", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/?tour=workflow-intro"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.completeAndAdvance();
      });
      expect(tourStoreMocks.completeStep).toHaveBeenCalledWith(
        "workflow-intro"
      );
    });

    it("advances from workflow-intro to workflow-hub at /workflow", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/?tour=workflow-intro"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.completeAndAdvance();
      });
      expect(router.state.location.pathname).toBe("/workflow");
      expect(router.state.location.search).toMatchObject({
        tour: "workflow-hub",
      });
    });

    it("on the last step (completion) calls markTourCompleted and drops the tour param", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/?tour=completion"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.completeAndAdvance();
      });
      expect(tourStoreMocks.markTourCompleted).toHaveBeenCalled();
      expect(router.state.location.search.tour).toBeUndefined();
    });
  });

  describe("skipAll", () => {
    it("marks remaining steps skipped and drops the tour param", async () => {
      const useTourNavigation = await loadHook();
      if (!useTourNavigation) {
        expect.fail("useTourNavigation hook not implemented yet");
        return;
      }
      const { result, router } = renderHookWithRouter(
        () => useTourNavigation(),
        ["/?tour=workspaces"]
      );
      await waitFor(() => expect(result.current).toBeDefined());
      await act(async () => {
        await result.current.skipAll();
      });
      expect(tourStoreMocks.skipStep.mock.calls.length).toBeGreaterThan(0);
      expect(router.state.location.search.tour).toBeUndefined();
    });
  });
});
