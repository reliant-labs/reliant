/**
 * Integration tests for OnboardingWizard's URL-driven render gating.
 *
 * Contract:
 *   - Wizard renders iff ?tour=<valid-step-id> is present in the URL.
 *   - With no tour param the wizard returns null (no spotlight, no modal).
 *   - When the current pathname doesn't match a spotlight step's expected
 *     page, the wizard renders an "Open <page>" modal with an action button
 *     that navigates to the correct page (carrying the tour param).
 *   - When the pathname matches, the spotlight branch is rendered.
 *
 * Several tests rely on the impl agent's refactor; they will fail until the
 * wizard reads tour state from the URL (instead of `useTourStore`).
 */
/* eslint-disable @typescript-eslint/no-explicit-any */
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
    resetTourProgress: vi.fn(async () => undefined),
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

vi.mock("../../../lib/analytics", () => ({
  trackEvent: vi.fn(),
}));

// The wizard imports OnboardingChecklist which pulls in heavy components
// (workflow builder, chat, etc.) that aren't relevant to the routing tests.
// Stub OnboardingChecklist so the import graph stays light.
vi.mock("../OnboardingChecklist", () => ({
  OnboardingChecklist: () => null,
}));

// CompletionStep transitively imports analytics + chat machinery — stub it.
vi.mock("../steps", () => ({
  CompletionStep: (_props: any) => null,
}));

// ─── Lazy wizard import ──────────────────────────────────────────────────────

async function loadWizard(): Promise<React.ComponentType<any> | null> {
  try {
    const mod = await import("../OnboardingWizard");
    return (mod as any).OnboardingWizard ?? null;
  } catch (err) {
    // Re-throw so the test failure surfaces the actual import error instead
    // of a generic "not importable" message.
    throw err;
  }
}

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
  });

  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    validateSearch: searchSchema,
    component: () => React.createElement("div", { "data-testid": "index" }),
  });
  const workflowHubRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/workflow",
    validateSearch: searchSchema,
    component: () =>
      React.createElement("div", { "data-testid": "workflow-hub-page" }),
  });
  const workflowBuilderRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/workflow/$workflowName",
    validateSearch: searchSchema,
    component: () =>
      React.createElement("div", { "data-testid": "workflow-builder-page" }),
  });

  const tree = rootRoute.addChildren([
    indexRoute,
    workflowHubRoute,
    workflowBuilderRoute,
  ]);
  return createRouter({
    routeTree: tree as any,
    history: createMemoryHistory({ initialEntries }),
  });
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("OnboardingWizard URL gating", () => {
  beforeEach(() => {
    tourStoreState.state = {
      ...tourStoreState.state,
      completedSteps: new Set(),
      skippedSteps: new Set(),
      hasCompletedOnboarding: false,
      isInitialized: true,
    };
  });

  it("renders nothing when no ?tour param is present", async () => {
    const Wizard = await loadWizard();
    if (!Wizard) {
      expect.fail("OnboardingWizard not importable");
      return;
    }
    const router = makeRouter(["/"], Wizard);
    render(React.createElement(RouterProvider as any, { router }));
    await waitFor(() =>
      expect(screen.getByTestId("index")).toBeInTheDocument()
    );
    // No tour modal, no spotlight title — heuristic: none of the known
    // step titles should appear.
    expect(screen.queryByText("Workflow Templates")).toBeNull();
    expect(screen.queryByText("Your Development Environment")).toBeNull();
  });

  it("renders the multi-spotlight step when at /?tour=chat-and-sidebars", async () => {
    const Wizard = await loadWizard();
    if (!Wizard) {
      expect.fail("OnboardingWizard not importable");
      return;
    }
    const router = makeRouter(["/?tour=chat-and-sidebars"], Wizard);
    render(React.createElement(RouterProvider as any, { router }));
    await waitFor(() => {
      // The chat-and-sidebars step renders the OnboardingNavBar with step 1/7.
      expect(screen.getByText(/1 \/ 7/)).toBeInTheDocument();
    });
  });

  it("renders an 'Open Workflows' modal when on / but tour=workflow-hub", async () => {
    const Wizard = await loadWizard();
    if (!Wizard) {
      expect.fail("OnboardingWizard not importable");
      return;
    }
    const router = makeRouter(["/?tour=workflow-hub"], Wizard);
    render(React.createElement(RouterProvider as any, { router }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /open workflows/i })).toBeInTheDocument()
    );
    expect(
      screen.getByText(/open workflows to continue/i)
    ).toBeInTheDocument();
  });

  it("clicking 'Open Workflows' navigates to /workflow with the tour param preserved", async () => {
    const Wizard = await loadWizard();
    if (!Wizard) {
      expect.fail("OnboardingWizard not importable");
      return;
    }
    const router = makeRouter(["/?tour=workflow-hub"], Wizard);
    render(React.createElement(RouterProvider as any, { router }));

    const button = await waitFor(() =>
      screen.getByRole("button", { name: /open workflows/i })
    );
    await userEvent.click(button);

    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/workflow");
    });
    // The wizard should preserve the tour param across the navigation so
    // the spotlight branch can take over on the right page.
    expect(router.state.location.search).toMatchObject({
      tour: "workflow-hub",
    });
  });

  it("renders the spotlight branch (not the open-page modal) at /workflow?tour=workflow-hub", async () => {
    const Wizard = await loadWizard();
    if (!Wizard) {
      expect.fail("OnboardingWizard not importable");
      return;
    }
    const router = makeRouter(["/workflow?tour=workflow-hub"], Wizard);
    render(React.createElement(RouterProvider as any, { router }));
    await waitFor(() =>
      expect(screen.getByTestId("workflow-hub-page")).toBeInTheDocument()
    );
    // On the right page, the open-page modal action button must NOT appear;
    // the spotlight tooltip (which contains the step title) should.
    expect(
      screen.queryByRole("button", { name: /open workflows/i })
    ).toBeNull();
  });
});
