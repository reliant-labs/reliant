/**
 * OnboardingWizard — the tour starts as soon as the wizard is initialized.
 *
 * THE BUG THIS CLOSES: the wizard used to suppress itself entirely while
 * NewChatView's blocking "What are you building?" dialog could be on screen.
 * That gate was spelled
 *
 *     newChatViewMounted && chatsLoaded && chatsCount === 0 && !hasPickedStarter
 *
 * and `chatsLoaded` waited on the useChatList query to resolve. So a
 * brand-new user — who by definition lands on `/` with zero chats and no
 * starter picked — got no tour until that query came back AND they answered
 * the starter question, which is the visible lag the user reported.
 *
 * Removing the blocking dialog (see NewChatView.starterPicker) removed the
 * only reason for the gate, so the gate is gone and the tour no longer waits
 * on the chat list at all. The scenario below is the exact state that used to
 * render nothing.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import React from "react";
import { render, screen, waitFor } from "@testing-library/react";
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { z } from "zod";

// ── Mocks ────────────────────────────────────────────────────────────────

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
    },
  ),
}));

// The wizard renders nothing without an authenticated user — onboarding is
// per-user state (see OnboardingWizard.authGate.test.tsx). This suite is about
// the starter-picker gate, so sign a user in and let that gate be the variable.
vi.mock("../../../store/authStore", () => {
  const authState = { user: { id: "user-1" } };
  return {
    useAuthStore: Object.assign(
      (selector?: any) => (selector ? selector(authState) : authState),
      {
        getState: () => authState,
        setState: vi.fn(),
        subscribe: vi.fn(() => () => undefined),
      },
    ),
  };
});

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
      },
    ),
  };
});

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
    getState: () => ({ setLeftSidebarExpandedGlobal: vi.fn() }),
  },
}));

// ── The first-run shape ──────────────────────────────────────────────────
// Zero chats, list resolved, no starter picked, sitting on `/`. Under the old
// gate this combination made the wizard render null.

vi.mock("../../../store/chatStore", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../store/chatStore")>();
  const mockState = {
    clearCurrentChat: vi.fn(),
    chats: new Map(),
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
      },
    ),
  };
});

vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: [], isSuccess: true }),
}));

vi.mock("../../../store/chatParamsStore", () => {
  const state = { tempNewChatWorkflow: null };
  return {
    useChatParamsStore: Object.assign(
      (selector?: any) => (selector ? selector(state) : state),
      {
        getState: () => state,
        setState: vi.fn(),
        subscribe: vi.fn(() => () => undefined),
      },
    ),
  };
});

vi.mock("../../../store/projectStore", () => {
  const projectState = { currentProject: { id: "project-1" } };
  return {
    useProjectStore: Object.assign(
      (selector: any) => (selector ? selector(projectState) : projectState),
      {
        getState: () => projectState,
        setState: vi.fn(),
        subscribe: vi.fn(() => () => undefined),
      },
    ),
  };
});

vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../OnboardingChecklist", () => ({ OnboardingChecklist: () => null }));
vi.mock("../steps", () => ({ CompletionStep: (_props: any) => null }));

// ── Test router ──────────────────────────────────────────────────────────

const searchSchema = z.object({
  tour: z.string().optional(),
  drill: z.string().optional(),
});

function makeRouter(initialEntries: string[], Wizard: React.ComponentType<any>) {
  const rootRoute = createRootRoute({
    component: () =>
      React.createElement(
        React.Fragment,
        null,
        React.createElement(Outlet),
        React.createElement(Wizard),
      ),
  });
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    validateSearch: searchSchema,
    component: () => React.createElement("div", { "data-testid": "index" }),
  });
  const projectRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/project/$projectId",
    validateSearch: searchSchema,
    component: () => React.createElement("div", { "data-testid": "project" }),
  });
  const tree = rootRoute.addChildren([indexRoute, projectRoute]);
  return createRouter({
    routeTree: tree as any,
    history: createMemoryHistory({ initialEntries }),
  });
}

async function loadWizard(): Promise<React.ComponentType<any>> {
  const mod = await import("../OnboardingWizard");
  return (mod as any).OnboardingWizard;
}

// ── Tests ────────────────────────────────────────────────────────────────

describe("OnboardingWizard — no starter-picker gate", () => {
  beforeEach(() => {
    tourStoreState.state = {
      ...tourStoreState.state,
      completedSteps: new Set(),
      skippedSteps: new Set(),
      hasCompletedOnboarding: false,
      isInitialized: true,
    };
  });

  // The reported symptom, as a test: a fresh user lands on `/` with the tour
  // param set and must see step 1 without first answering a starter question.
  it("shows the tour immediately for a project with zero chats", async () => {
    const Wizard = await loadWizard();
    const router = makeRouter(["/?tour=chat-and-sidebars"], Wizard);
    render(React.createElement(RouterProvider as any, { router }));

    await waitFor(() =>
      expect(screen.getByText(/1 \/ 7/)).toBeInTheDocument(),
    );
  });

  // Same for the project route, the other place NewChatView mounts.
  it("shows the tour on /project/$id with zero chats", async () => {
    const Wizard = await loadWizard();
    const router = makeRouter(["/project/p1?tour=chat-and-sidebars"], Wizard);
    render(React.createElement(RouterProvider as any, { router }));

    await waitFor(() =>
      expect(screen.getByText(/1 \/ 7/)).toBeInTheDocument(),
    );
  });
});
