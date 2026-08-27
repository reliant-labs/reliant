/**
 * The onboarding wizard must not render to a signed-out user.
 *
 * THE BUG THIS CLOSES (2026-08-26, packaged desktop): "the activation
 * checklist is still showing on the login screen." A forced sign-out (see
 * api/transport.ts's 401 handler) dropped the user to /auth without
 * unmounting this globally-mounted wizard, so the Setup Guide floater stayed
 * painted over the login form.
 *
 * The wizard suppressed itself on /onboarding but had NO authentication gate.
 * Route is the wrong gate for this: onboarding state is per-USER, the sign-in
 * screen is reachable at more than one path, and the wizard is mounted on the
 * root route so it outlives any particular page.
 */
/* eslint-disable @typescript-eslint/no-explicit-any */
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

// ─── Auth store double ───────────────────────────────────────────────────────

const authState = vi.hoisted(() => ({
  current: { user: null as unknown } as { user: unknown },
}));

vi.mock("../../../store/authStore", () => ({
  useAuthStore: Object.assign(
    (selector?: any) =>
      selector ? selector(authState.current) : authState.current,
    {
      getState: () => authState.current,
      setState: vi.fn(),
      subscribe: vi.fn(() => () => undefined),
    },
  ),
}));

// ─── Store doubles (mirrors OnboardingWizard.routing.test.tsx) ───────────────

const tourStoreState = vi.hoisted(() => ({
  state: {
    hasCompletedOnboarding: true,
    completedSteps: new Set<string>(),
    skippedSteps: new Set<string>(),
    projectHasCode: true,
    isInitialized: true,
    isLoading: false,
    loadState: vi.fn(async () => undefined),
    completeStep: vi.fn(async () => undefined),
    skipStep: vi.fn(async () => undefined),
    saveTourState: vi.fn(async () => undefined),
    scheduleSave: vi.fn(),
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

// panelState "expanded" + nothing complete == the checklist WANTS to render.
// That is the whole point: only the auth gate should stop it.
vi.mock("../../../store/onboardingChecklistStore", () => {
  const checklistState = {
    isInitialized: true,
    panelState: "expanded" as const,
    completedItems: new Set<string>(),
    welcomeShown: false,
    loadState: vi.fn(async () => undefined),
    detectCompletedItems: vi.fn(),
    subscribeToStoreChanges: vi.fn(() => () => undefined),
    allRequiredComplete: () => false,
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

vi.mock("../../../store/chatStore", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../../store/chatStore")>();
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
      },
    ),
  };
});

vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: [{ id: "existing-chat" }], isSuccess: true }),
}));

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

// Render a recognizable stand-in so "did the checklist paint?" is a direct
// query rather than a heuristic over the real component's markup.
vi.mock("../OnboardingChecklist", () => ({
  OnboardingChecklist: () =>
    React.createElement("div", { "data-testid": "setup-guide" }, "Setup Guide"),
}));

vi.mock("../steps", () => ({ CompletionStep: (_props: any) => null }));

import { OnboardingWizard } from "../OnboardingWizard";

// ─── Test router ─────────────────────────────────────────────────────────────

const searchSchema = z.object({
  tour: z.string().optional(),
  drill: z.string().optional(),
});

function makeRouter(initialEntries: string[]) {
  const rootRoute = createRootRoute({
    component: () =>
      React.createElement(
        React.Fragment,
        null,
        React.createElement(Outlet),
        React.createElement(OnboardingWizard),
      ),
  });
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    validateSearch: searchSchema,
    component: () => React.createElement("div", { "data-testid": "index" }),
  });
  const authRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/auth",
    validateSearch: searchSchema,
    component: () => React.createElement("div", { "data-testid": "login" }),
  });

  return createRouter({
    routeTree: rootRoute.addChildren([indexRoute, authRoute]) as any,
    history: createMemoryHistory({ initialEntries }),
  });
}

describe("OnboardingWizard — authentication gate", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authState.current = { user: null };
  });

  it("renders no checklist on the login screen when signed out", async () => {
    // The exact reported state: forced sign-out landed the user on /auth
    // while the checklist store still says "expanded, nothing complete".
    const router = makeRouter(["/auth"]);
    render(React.createElement(RouterProvider as any, { router }));

    await waitFor(() =>
      expect(screen.getByTestId("login")).toBeInTheDocument(),
    );
    expect(screen.queryByTestId("setup-guide")).toBeNull();
  });

  it("renders no checklist anywhere while signed out", async () => {
    // Route is not the gate — signing out on the main page must hide it too.
    const router = makeRouter(["/"]);
    render(React.createElement(RouterProvider as any, { router }));

    await waitFor(() =>
      expect(screen.getByTestId("index")).toBeInTheDocument(),
    );
    expect(screen.queryByTestId("setup-guide")).toBeNull();
  });

  it("renders the checklist once a user is signed in", async () => {
    // The gate must not suppress the feature for its actual audience.
    authState.current = { user: { id: "user-1" } };
    const router = makeRouter(["/"]);
    render(React.createElement(RouterProvider as any, { router }));

    await waitFor(() =>
      expect(screen.getByTestId("setup-guide")).toBeInTheDocument(),
    );
  });

  it("does not run the guided tour for a signed-out user", async () => {
    // A stale ?tour= param must not spotlight chrome over the login form.
    const router = makeRouter(["/auth?tour=chat-and-sidebars"]);
    render(React.createElement(RouterProvider as any, { router }));

    await waitFor(() =>
      expect(screen.getByTestId("login")).toBeInTheDocument(),
    );
    expect(screen.queryByText(/1 \/ 7/)).toBeNull();
  });
});
