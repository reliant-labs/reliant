/**
 * useTourNavigation against FIXTURE journeys (the content modules are mocked),
 * so the engine's contract is pinned independently of whatever content ships:
 *
 *   - `?tour=` is the source of truth: journey:step, bare step ids, legacy ids.
 *   - goToStep follows a step's route — static, or built from the journey
 *     context — merging its search when already on the page.
 *   - an advanceOn that fires for a step the user already left is ignored.
 *   - a step's `skip.run` runs before moving on; if it throws, the user stays.
 *   - Finish lands on the journey's own `finish` target when it has one.
 *   - onComplete runs on forward moves only.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
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

const hooks = vi.hoisted(() => ({
  onComplete: vi.fn(),
  skipRun: vi.fn(async (_ctx?: unknown) => undefined as void),
}));

vi.mock("../journeys/workflows", () => ({
  workflowsJourney: {
    id: "workflows",
    version: 1,
    title: "Use AI workflows",
    description: "d",
    steps: [
      { id: "wf-a", title: "A", description: "a", onComplete: hooks.onComplete },
      {
        id: "wf-b",
        title: "B",
        description: "b",
        skip: { label: "Create it for me", run: hooks.skipRun },
        route: {
          path: "/workflow/",
          label: "Open the builder",
          open: (ctx: Record<string, unknown>) =>
            ctx.workflowSlug ? { to: "/workflow/$workflowName", params: { workflowName: String(ctx.workflowSlug) } } : null,
        },
      },
      { id: "wf-c", title: "C", description: "c" },
    ],
  },
}));

vi.mock("../journeys/forge", () => ({
  forgeJourney: {
    id: "forge",
    version: 1,
    title: "Explore a forge app",
    description: "d",
    finish: (shell: string) => (shell === "reliant" ? { to: "/apps" } : null),
    steps: [
      { id: "fg-a", title: "FA", description: "fa", route: { path: "/forge/demo", label: "Open", open: { to: "/forge/demo", search: { view: "overview" } } } },
      { id: "fg-b", title: "FB", description: "fb", route: { path: "/forge/demo", label: "Open", open: { to: "/forge/demo", search: { view: "traces" } } } },
    ],
  },
}));

vi.mock("../../../lib/settingsPersistence", () => ({
  safeGetSetting: vi.fn(async () => null),
  upsertStringSetting: vi.fn(async () => undefined),
  deleteSettingIfExists: vi.fn(async () => undefined),
}));
vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../store/onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: {
    getState: () => ({ markComplete: vi.fn(async () => undefined), markWelcomeShown: vi.fn(async () => undefined), completedItems: new Set() }),
    setState: vi.fn(),
    subscribe: vi.fn(() => () => undefined),
  },
}));
vi.mock("../../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ clearCurrentChat: vi.fn() }) },
}));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: { getState: () => ({ currentProject: { id: "project-1" } }) },
}));
vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: {
    getState: () => ({ hasApiKey: true, ensureApiKeyOrShowModal: vi.fn(async () => undefined) }),
    setState: vi.fn(),
  },
}));

// The workflows journey's content is registered by the Reliant shells (journeys/catalog.ts).
import "../journeys/catalog";
import { useTourNavigation, type TourNavigation } from "../useTourNavigation";
import { useTourStore, __resetTourSaveScheduler } from "../../../store/tourStore";
import { trackEvent } from "../../../lib/analytics";

const searchSchema = z.object({ tour: z.string().optional(), view: z.string().optional(), project: z.string().optional() });

function setup(url: string) {
  const result: { current?: TourNavigation } = {};
  function Harness() {
    result.current = useTourNavigation();
    return null;
  }
  const rootRoute = createRootRoute({
    component: () => React.createElement(React.Fragment, null, React.createElement(Outlet), React.createElement(Harness)),
  });
  const route = (path: string) =>
    createRoute({ getParentRoute: () => rootRoute, path, validateSearch: searchSchema, component: () => null });
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      route("/"),
      route("/apps"),
      route("/forge/demo"),
      route("/project/$projectId"),
      route("/workflow/$workflowName"),
    ]) as any,
    history: createMemoryHistory({ initialEntries: [url] }),
  });
  render(React.createElement(RouterProvider as any, { router }));
  const nav = () => result.current!;
  const tour = () => (router.state.location.search as { tour?: string }).tour;
  return { router, nav, tour };
}

describe("useTourNavigation", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    __resetTourSaveScheduler();
    useTourStore.setState({ contexts: {}, completedSteps: new Set(), skippedSteps: new Set() } as any);
  });

  describe("reading ?tour=", () => {
    it("resolves journey:step, bare ids and journeys", async () => {
      const a = setup("/?tour=workflows:wf-c");
      await waitFor(() => expect(a.nav().currentStepId).toBe("wf-c"));
      expect(a.nav().currentJourney).toBe("workflows");
      expect(a.nav().isWizardActive).toBe(true);
    });

    it("opens a bare step id in the journey that declares it", async () => {
      const { nav } = setup("/?tour=fg-b");
      await waitFor(() => expect(nav().currentJourney).toBe("forge"));
      expect(nav().currentStepId).toBe("fg-b");
    });

    it("opens a retired single-tour id at its successor journey's first step", async () => {
      const { nav } = setup("/?tour=workflow-hub");
      await waitFor(() => expect(nav().currentStepId).toBe("wf-a"));
    });

    it("reports the picker, and nothing for junk", async () => {
      const p = setup("/?tour=pick:menu");
      await waitFor(() => expect(p.nav().isPickerOpen).toBe(true));
      expect(p.nav().tour).toEqual({ kind: "picker", source: "menu" });
    });
  });

  describe("goToStep", () => {
    it("merges a route's search when already on its page", async () => {
      const { router, nav, tour } = setup("/forge/demo?view=overview&project=shop&tour=forge:fg-a");
      await waitFor(() => expect(nav().currentStepId).toBe("fg-a"));
      await act(async () => nav().goToStep("forge", "fg-b"));
      await waitFor(() => expect(tour()).toBe("forge:fg-b"));
      expect(router.state.location.search).toMatchObject({ view: "traces", project: "shop" });
    });

    it("builds the route from the journey context", async () => {
      useTourStore.getState().setJourneyContext("workflows", { workflowSlug: "fix-my-pr" });
      const { router, nav, tour } = setup("/?tour=workflows:wf-a");
      await waitFor(() => expect(nav().currentStepId).toBe("wf-a"));
      await act(async () => nav().advance("next"));
      await waitFor(() => expect(router.state.location.pathname).toBe("/workflow/fix-my-pr"));
      expect(tour()).toBe("workflows:wf-b");
      expect(hooks.onComplete).toHaveBeenCalledTimes(1);
    });

    it("without the context it needs, stays put and just sets the step", async () => {
      const { router, nav, tour } = setup("/?tour=workflows:wf-a");
      await waitFor(() => expect(nav().currentStepId).toBe("wf-a"));
      await act(async () => nav().goToStep("workflows", "wf-b"));
      await waitFor(() => expect(tour()).toBe("workflows:wf-b"));
      expect(router.state.location.pathname).toBe("/");
    });
  });

  describe("advance", () => {
    it("ignores an action for a step the user already left", async () => {
      const { nav, tour } = setup("/?tour=workflows:wf-c");
      await waitFor(() => expect(nav().currentStepId).toBe("wf-c"));
      await act(async () => nav().advance("action", { journey: "workflows", stepId: "wf-a" }));
      expect(tour()).toBe("workflows:wf-c");
      expect(hooks.onComplete).not.toHaveBeenCalled();
    });

    it("on the last step, an action only completes it (Finish stays the user's)", async () => {
      const { nav, tour } = setup("/?tour=workflows:wf-c");
      await waitFor(() => expect(nav().currentStepId).toBe("wf-c"));
      await act(async () => nav().advance("action", { journey: "workflows", stepId: "wf-c" }));
      expect(tour()).toBe("workflows:wf-c");
    });

    it("Back does not run onComplete", async () => {
      const { nav, tour } = setup("/?tour=workflows:wf-c");
      await waitFor(() => expect(nav().currentStepId).toBe("wf-c"));
      await act(async () => nav().goBack());
      await waitFor(() => expect(tour()).toBe("workflows:wf-b"));
      expect(hooks.onComplete).not.toHaveBeenCalled();
    });
  });

  describe("skipStep", () => {
    it("runs the step's skip handler, then moves on", async () => {
      hooks.skipRun.mockImplementationOnce(async (ctx: any) => ctx.setContext({ workflowSlug: "seeded" }));
      const { nav, tour } = setup("/?tour=workflows:wf-b");
      await waitFor(() => expect(nav().currentStepId).toBe("wf-b"));
      await act(async () => nav().skipStep());
      await waitFor(() => expect(tour()).toBe("workflows:wf-c"));
      expect(useTourStore.getState().contexts.workflows).toEqual({ workflowSlug: "seeded" });
      expect(trackEvent).toHaveBeenCalledWith("journey_step_completed", expect.objectContaining({ step: "wf-b", via: "skip" }));
    });

    it("stays on the step when the skip handler fails", async () => {
      hooks.skipRun.mockRejectedValueOnce(new Error("seed failed"));
      const { nav, tour } = setup("/?tour=workflows:wf-b");
      await waitFor(() => expect(nav().currentStepId).toBe("wf-b"));
      await act(async () => nav().skipStep());
      expect(tour()).toBe("workflows:wf-b");
    });
  });

  describe("finish", () => {
    it("lands on the journey's own finish target, clearing the tour", async () => {
      const { router, nav, tour } = setup("/forge/demo?tour=forge:fg-b");
      await waitFor(() => expect(nav().currentStepId).toBe("fg-b"));
      await act(async () => nav().finish());
      await waitFor(() => expect(router.state.location.pathname).toBe("/apps"));
      expect(tour()).toBeUndefined();
      expect(useTourStore.getState().journeys.forge.current).toBe("completed");
    });

    it("defaults to the project chat in Reliant", async () => {
      const { router, nav } = setup("/?tour=workflows:wf-c");
      await waitFor(() => expect(nav().currentStepId).toBe("wf-c"));
      await act(async () => nav().finish());
      await waitFor(() => expect(router.state.location.pathname).toBe("/project/project-1"));
    });
  });
});
