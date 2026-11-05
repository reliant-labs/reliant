/**
 * The journey engine end to end, through the Reliant wizard and a real
 * (memory) router: what `?tour=` renders, the picker and its preselection,
 * a page-bound step's "Open <page>" card, an action-gated step (advanceOn),
 * Skip step / End tour, the end card (Finish, Try the other journey) and the
 * outro's onComplete hook, and the retired tour's ids.
 *
 * Runs against the shipped journeys:
 *   workflows: wf-new (signal + route, autoAdvance) → … → wf-machines (route)
 *              → wf-automations (page-bound) → wf-params
 *              → wf-outro-layout (Reliant-only outro, collapses panels)
 *   forge:     fg-console → … → fg-done (journeys/forge.tsx; its own test covers it)
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import React from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
// The wizard pulls in several stores and side-effect-heavy modules. Stub them
// out so we can exercise the URL-gating logic in isolation.

const tourStoreState = vi.hoisted(() => ({
  state: {
    hasCompletedOnboarding: false,
    completedSteps: new Set<string>(),
    skippedSteps: new Set<string>(),
    projectHasCode: true,
    isInitialized: true,
    isLoading: false,
    loadState: vi.fn(async () => undefined),
    completeStep: vi.fn(async () => undefined),
    skipStep: vi.fn(async () => undefined),
    saveTourState: vi.fn(async () => undefined),
    detectProjectCode: vi.fn(async () => undefined),
    markTourCompleted: vi.fn(async () => undefined),
    markRemainingSkipped: vi.fn(),
    resetTourProgress: vi.fn(async () => undefined),
    setLastJourney: vi.fn(async () => undefined),
    setResumeStep: vi.fn(async () => undefined),
    setJourneyContext: vi.fn(),
    markJourneyShown: vi.fn(async () => undefined),
    resumeStep: null as string | null,
    contexts: {},
    lastJourney: null,
    journeys: {
      workflows: { version: 2, current: null, completedOlder: false },
      forge: { version: 1, current: null, completedOlder: false },
    },
  },
}));

vi.mock("../../../store/tourStore", () => ({
  useTourStore: Object.assign(
    (selector?: any) =>
      selector ? selector(tourStoreState.state) : tourStoreState.state,
    {
      getState: () => tourStoreState.state,
      setState: (patch: any) => {
        tourStoreState.state = { ...tourStoreState.state, ...patch };
      },
      subscribe: vi.fn(() => () => undefined),
    }
  ),
}));

// The wizard renders nothing without an authenticated user — onboarding is
// per-user state, and a signed-out visitor must never see the tour or the
// checklist (see OnboardingWizard.authGate.test.tsx). These routing tests are
// about URL gating, so stand up a signed-in user and let the URL decide.
vi.mock("../../../store/authStore", () => {
  const authState = { user: { id: "user-1" } };
  return {
    useAuthStore: Object.assign(
      (selector?: any) => (selector ? selector(authState) : authState),
      {
        getState: () => authState,
        setState: vi.fn(),
        subscribe: vi.fn(() => () => undefined),
      }
    ),
  };
});

// Checklist store may be called WITH or WITHOUT a selector. Both forms must
// return an object containing the fields the wizard destructures.
vi.mock("../../../store/onboardingChecklistStore", () => {
  const checklistState = {
    isInitialized: true,
    panelState: "dismissed" as const,
    completedItems: new Set<string>(),
    welcomeShown: true,
    loadState: vi.fn(async () => undefined),
    detectCompletedItems: vi.fn(),
    subscribeToStoreChanges: vi.fn(() => () => undefined),
    markComplete: vi.fn(async () => undefined),
    markWelcomeShown: vi.fn(async () => undefined),
  };
  return {
    useOnboardingChecklistStore: Object.assign(
      (selector?: any) => (selector ? selector(checklistState) : checklistState),
      {
        getState: () => checklistState,
        setState: vi.fn(),
        subscribe: vi.fn(() => () => undefined),
      }
    ),
  };
});

// useTourNavigation pulls in apiKeySetupStore on completion paths.
vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: {
    getState: () => ({
      hasApiKey: true,
      ensureApiKeyOrShowModal: vi.fn(async () => undefined),
    }),
    setState: vi.fn(),
  },
}));

vi.mock("../../../store/workspaceStateStore", () => ({
  useWorkspaceStateStore: {
    getState: () => ({
      setLeftSidebarExpandedGlobal: vi.fn(),
    }),
  },
}));

vi.mock("../../../store/chatStore", async (importOriginal) => {
  // Use partial mock to preserve every named export the rest of the app
  // expects (initGlobalUpdatesStoreRef, etc.). We only override getState
  // for clearCurrentChat. `chats` and `hasLoaded` are read by the wizard's
  // starter-picker-modal gate; default to a populated `chats` so the gate
  // does NOT trigger (these routing tests are about tour navigation, not
  // the empty-state intent modal).
  const actual = await importOriginal<typeof import("../../../store/chatStore")>();
  const mockState = {
    clearCurrentChat: vi.fn(),
    chats: new Map([["existing-chat", {}]]),
    hasLoaded: true,
  };
  return {
    ...actual,
    useChatStore: Object.assign(
      (selector: any) => (selector ? selector(mockState) : mockState),
      {
        getState: () => mockState,
        setState: vi.fn(),
        subscribe: vi.fn(() => () => undefined),
      }
    ),
  };
});

// The wizard's starter-picker-modal gate reads the chat list from React Query
// (useChatList) and the current project. Provide a populated list + a project
// so the gate does NOT trigger (these routing tests are about tour navigation).
vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({
    data: [{ id: "existing-chat" }],
    isSuccess: true,
  }),
}));

vi.mock("../../../store/projectStore", () => {
  const projectState = { currentProject: { id: "project-1" } };
  return {
    useProjectStore: Object.assign(
      (selector: any) =>
        selector ? selector(projectState) : projectState,
      {
        getState: () => projectState,
        setState: vi.fn(),
        subscribe: vi.fn(() => () => undefined),
      }
    ),
  };
});

// The workflows journey's Automations step says whether the user turned Fix
// my PR on, from the triggers query.
vi.mock("../../../hooks/trigger-queries", () => ({
  useTriggers: () => ({ data: [] }),
}));

vi.mock("../../../lib/analytics", () => ({
  trackEvent: vi.fn(),
}));

// The wizard imports OnboardingChecklist which pulls in heavy components
// (workflow builder, chat, etc.) that aren't relevant to the routing tests.
// Stub OnboardingChecklist so the import graph stays light.
vi.mock("../OnboardingChecklist", () => ({
  OnboardingChecklist: () => null,
}));

// The workflows outro collapses the workspace panels on completion; observe
// it rather than drive the real panel stores.
vi.mock("../journeys/layoutActions", () => ({
  layoutActions: { collapseAll: vi.fn(), showSidebar: vi.fn(), hideSidebar: vi.fn() },
}));

// Imported statically, after the mocks (vitest hoists vi.mock above it): a
// dynamic import inside the first test made that test pay for loading the
// wizard's whole module graph, and on a loaded machine it hit the 10s test
// timeout.
import { OnboardingWizard, __resetJourneyReshow } from "../OnboardingWizard";
import { trackEvent } from "../../../lib/analytics";
import { holdLandingJourney } from "../../../lib/landingAttribution";
import { layoutActions } from "../journeys/layoutActions";
import { __resetJourneyAnalytics } from "../journeyAnalytics";
import { tourEvents } from "../tourEvents";
import { retainTourParam } from "../tourParam";

// ─── Test router ─────────────────────────────────────────────────────────────

const searchSchema = z.object({
  tour: z.string().optional(),
  drill: z.string().optional(),
});

function makeRouter(initialEntries: string[], Wizard: React.ComponentType<any>) {
  const rootRoute = createRootRoute({
    component: () =>
      React.createElement(React.Fragment, null,
        React.createElement(Outlet),
        React.createElement(Wizard)
      ),
    // As in routes.tsx: `?tour=` survives navigations that don't clear it, so
    // every tour exit asserted below must clear it explicitly.
    search: { middlewares: [retainTourParam as never] },
  });
  const route = (path: string, testId: string) =>
    createRoute({
      getParentRoute: () => rootRoute,
      path,
      validateSearch: searchSchema,
      component: () => React.createElement("div", { "data-testid": testId }),
    });
  const tree = rootRoute.addChildren([
    route("/", "index"),
    route("/workflows/library", "workflow-hub-page"),
    route("/workflow/$workflowName", "workflow-builder-page"),
    route("/settings/machines", "machines-page"),
    route("/apps", "apps-page"),
    // The forge journey's page (its steps open the demo console).
    route("/forge/demo", "forge-demo"),
    // Where finishing the tour lands the user (chatRouteAfterTour).
    route("/project/$projectId", "project-chat"),
  ]);
  return createRouter({
    routeTree: tree as any,
    history: createMemoryHistory({ initialEntries }),
  });
}

function renderAt(url: string) {
  const router = makeRouter([url], OnboardingWizard);
  render(React.createElement(RouterProvider as any, { router }));
  return router;
}

const tourParam = (router: ReturnType<typeof makeRouter>) =>
  (router.state.location.search as { tour?: string }).tour;

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("journey engine (OnboardingWizard)", () => {
  beforeEach(() => {
    vi.mocked(trackEvent).mockClear();
    vi.mocked(layoutActions.collapseAll).mockClear();
    tourStoreState.state.markTourCompleted.mockClear();
    tourStoreState.state.setResumeStep.mockClear();
    localStorage.clear();
    __resetJourneyReshow();
    __resetJourneyAnalytics();
    tourStoreState.state = {
      ...tourStoreState.state,
      completedSteps: new Set(),
      skippedSteps: new Set(),
      hasCompletedOnboarding: false,
      isInitialized: true,
      resumeStep: null,
    };
  });

  it("renders nothing when no ?tour param is present", async () => {
    renderAt("/");
    await screen.findByTestId("index");
    expect(screen.queryByTestId("onboarding-nav-bar")).toBeNull();
    expect(screen.queryByTestId("journey-picker")).toBeNull();
  });

  it("opens the retired single tour's ids at their successor journey", async () => {
    renderAt("/workflows/library?tour=chat-and-sidebars");
    expect(await screen.findByText(/1 \/ 10/)).toBeInTheDocument();
    expect(screen.getAllByText("Start a workflow").length).toBeGreaterThan(0);
  });

  describe("the picker", () => {
    it("shows both journeys and preselects the one the landing page carried", async () => {
      holdLandingJourney("?journey=forge");
      const router = renderAt("/?tour=pick");
      const forge = await screen.findByTestId("journey-card-forge");
      expect(forge).toHaveAttribute("aria-pressed", "true");
      expect(screen.getByTestId("journey-card-workflows")).toHaveAttribute("aria-pressed", "false");

      await userEvent.click(screen.getByRole("button", { name: /start tour/i }));
      await waitFor(() => expect(tourParam(router)).toBe("forge:fg-console"));
      expect(trackEvent).toHaveBeenCalledWith(
        "journey_selected",
        expect.objectContaining({ journey: "forge", source: "landing" }),
      );
      expect(tourStoreState.state.setLastJourney).toHaveBeenCalledWith("forge");
    });

    it("records a changed choice as the picker's", async () => {
      holdLandingJourney("?journey=forge");
      renderAt("/?tour=pick");
      await userEvent.click(await screen.findByTestId("journey-card-workflows"));
      await userEvent.click(screen.getByRole("button", { name: /start tour/i }));
      await waitFor(() =>
        expect(trackEvent).toHaveBeenCalledWith(
          "journey_selected",
          expect.objectContaining({ journey: "workflows", source: "picker" }),
        ),
      );
    });

    it("offers Resume when a tour was ended mid-way", async () => {
      tourStoreState.state = { ...tourStoreState.state, resumeStep: "workflows:wf-automations" };
      const router = renderAt("/?tour=pick:menu");
      await userEvent.click(await screen.findByTestId("journey-resume"));
      await waitFor(() => expect(tourParam(router)).toBe("workflows:wf-automations"));
      expect(router.state.location.pathname).toBe("/workflows/library");
    });

    it("Not now closes it and records the dismissal", async () => {
      const router = renderAt("/?tour=pick");
      await userEvent.click(await screen.findByRole("button", { name: /not now/i }));
      await waitFor(() => expect(tourParam(router)).toBeUndefined());
      expect(trackEvent).toHaveBeenCalledWith("journey_skipped", { journey: "none", step: "picker" });
      expect(tourStoreState.state.markTourCompleted).toHaveBeenCalledWith();
    });
  });

  describe("a page-bound step", () => {
    it("off its page, renders an Open card that goes there keeping the tour", async () => {
      const router = renderAt("/?tour=workflows:wf-automations");
      await userEvent.click(await screen.findByRole("button", { name: /open workflows/i }));
      await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/library"));
      expect(tourParam(router)).toBe("workflows:wf-automations");
    });

    it("on its page, renders the spotlight", async () => {
      renderAt("/workflows/library?tour=workflows:wf-automations");
      expect(await screen.findByText(/8 \/ 10/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /open workflows/i })).toBeNull();
    });
  });

  describe("an action-gated step (advanceOn)", () => {
    it("hides Next until the action, then moves on by itself (autoAdvance)", async () => {
      const router = renderAt("/workflows/library?tour=workflows:wf-new");
      await screen.findByText(/1 \/ 10/);
      expect(screen.queryByRole("button", { name: /^next/i })).toBeNull();
      expect(screen.getByRole("button", { name: /create it for me/i })).toBeInTheDocument();
      expect(screen.getByText(/click new workflow, then create workflow/i)).toBeInTheDocument();

      // The action: the guided draft exists, and the builder opened on it.
      tourEvents.emit("workflow:draftCreated", { slug: "fix-my-pr", draftId: "d1", start: "tour-fix-my-pr" });
      await router.navigate({
        to: "/workflow/$workflowName",
        params: { workflowName: "fix-my-pr" },
        search: { tour: "workflows:wf-new" },
      } as any);
      await waitFor(() => expect(tourParam(router)).toBe("workflows:wf-edge"), { timeout: 3000 });
      expect(trackEvent).toHaveBeenCalledWith(
        "journey_step_completed",
        expect.objectContaining({ journey: "workflows", step: "wf-new", via: "action", signal: "draft_created" }),
      );
    });

    it("already satisfied on entry: shows Next with a check, without moving on", async () => {
      renderAt("/settings/machines?tour=workflows:wf-machines");
      expect(await screen.findByRole("button", { name: /^next/i })).toBeInTheDocument();
      expect(screen.getByText("Done")).toBeInTheDocument();
    });

    it("Skip step moves on and records it as skipped", async () => {
      const router = renderAt("/workflow/fix-my-pr?tour=workflows:wf-loop");
      await userEvent.click(await screen.findByRole("button", { name: /skip step/i }));
      await waitFor(() => expect(tourParam(router)).toBe("workflows:wf-branch"));
      expect(tourStoreState.state.skipStep).toHaveBeenCalledWith("workflows:wf-loop");
      expect(trackEvent).toHaveBeenCalledWith(
        "journey_step_completed",
        expect.objectContaining({ step: "wf-loop", via: "skip" }),
      );
    });
  });

  describe("ending a journey", () => {
    const lastStepUrl = "/project/project-1?tour=workflows:wf-outro-layout";

    it("the last step offers Finish and Try the other journey, and no End tour", async () => {
      renderAt(lastStepUrl);
      expect(await screen.findByRole("button", { name: /finish/i })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /try the other journey/i })).toBeInTheDocument();
      expect(screen.getByText(/10 \/ 10/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /end tour/i })).toBeNull();
    });

    it("Finish runs the step's onComplete, records completion and lands on the project chat", async () => {
      const router = renderAt(lastStepUrl);
      await userEvent.click(await screen.findByRole("button", { name: /finish/i }));
      await waitFor(() => expect(router.state.location.pathname).toBe("/project/project-1"));
      expect(tourParam(router)).toBeUndefined();
      expect(layoutActions.collapseAll).toHaveBeenCalledTimes(1);
      expect(tourStoreState.state.markTourCompleted).toHaveBeenCalledWith("workflows", "completed");
      expect(trackEvent).toHaveBeenCalledWith("journey_completed", expect.objectContaining({ journey: "workflows" }));
      expect(trackEvent).toHaveBeenCalledWith(
        "onboarding_completed",
        expect.objectContaining({ journey: "workflows", totalSteps: 10 }),
      );
    });

    it("Try the other journey completes this one and starts the other", async () => {
      const router = renderAt(lastStepUrl);
      await userEvent.click(await screen.findByRole("button", { name: /try the other journey/i }));
      await waitFor(() => expect(tourParam(router)).toBe("forge:fg-console"));
      expect(tourStoreState.state.markTourCompleted).toHaveBeenCalledWith("workflows", "completed");
      expect(trackEvent).toHaveBeenCalledWith(
        "journey_selected",
        expect.objectContaining({ journey: "forge", source: "picker", from_journey: "workflows" }),
      );
    });

    it("a journey with no finish target leaves a builder page for the chat with ?tour= cleared", async () => {
      const router = renderAt("/workflow/fix-my-pr?tour=workflows:wf-loop");
      await userEvent.click(await screen.findByRole("button", { name: /end tour/i }));
      await waitFor(() => expect(router.state.location.pathname).toBe("/project/project-1"));
      expect(tourParam(router)).toBeUndefined();
    });

    it("End tour records the journey skipped and where it ended, for Resume", async () => {
      const router = renderAt("/workflows/library?tour=workflows:wf-automations");
      await userEvent.click(await screen.findByRole("button", { name: /end tour/i }));
      await waitFor(() => expect(router.state.location.pathname).toBe("/project/project-1"));
      expect(tourParam(router)).toBeUndefined();
      expect(tourStoreState.state.setResumeStep).toHaveBeenCalledWith("workflows:wf-automations");
      expect(tourStoreState.state.markTourCompleted).toHaveBeenCalledWith("workflows", "skipped");
      expect(trackEvent).toHaveBeenCalledWith("journey_skipped", expect.objectContaining({ journey: "workflows", step: "wf-automations" }));
    });
  });
});
