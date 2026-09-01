/**
 * syncProjectUrl must not clobber search params.
 *
 * The URL is the source of truth for which project is selected, so selecting
 * one drives the router to `/project/$id`. That navigation is about the
 * PATHNAME only — every search param on the URL belongs to someone else and
 * has to survive it.
 *
 * The regression this guards: tanstack-router REPLACES search when `search`
 * is omitted, so the original `navigate({ to, params })` erased the whole
 * query string. The casualty was `?tour=<step-id>`, which is the only trigger
 * for the post-onboarding tour (OnboardingWizard renders nothing without it).
 * Onboarding hands off to `/?tour=<first-step>`; workspace restore then
 * selected the just-created project, this sync moved / → /project/$id, and
 * the tour vanished with no error. It reproduced only when the restore landed
 * after the handoff — hence "the tour sometimes doesn't trigger".
 *
 * These tests drive the real router with the real route tree shape rather
 * than asserting on a mock, because the bug lives in router semantics: a
 * spy-based test would have happily passed against the broken call.
 */
/* eslint-disable @typescript-eslint/no-explicit-any */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import React from "react";
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  Outlet,
} from "@tanstack/react-router";
import { indexSearchSchema } from "../../routeSchemas";

// projectStore pulls in the gRPC client graph via its sibling stores; none of
// it is exercised here (syncProjectUrl only touches the router), so the heavy
// leaves are stubbed to keep this a focused unit test.
//
// `touch` resolves rather than being absent: selectProject calls it
// fire-and-forget, so a missing method surfaces as an unhandled rejection
// that fails the run even though every assertion passed.
vi.mock("../../api/project-grpc", () => ({
  projectGrpc: {
    touch: vi.fn(async () => undefined),
    get: vi.fn(async () => undefined),
    list: vi.fn(async () => []),
  },
}));

function makeRouter(initialEntry: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  // Mirrors routes.tsx: `_app` owns validateSearch, `/` and
  // `project/$projectId` are its children — so both accept `tour`.
  const appLayout = createRoute({
    getParentRoute: () => rootRoute,
    id: "_app",
    validateSearch: indexSearchSchema,
    component: () => <Outlet />,
  });
  const indexRoute = createRoute({
    getParentRoute: () => appLayout,
    path: "/",
    component: () => <div>index</div>,
  });
  const projectRoute = createRoute({
    getParentRoute: () => appLayout,
    path: "project/$projectId",
    component: () => <div>project</div>,
  });
  const settingsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/settings",
    component: () => <div>settings</div>,
  });
  const onboardingRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/onboarding",
    component: () => <div>onboarding</div>,
  });

  return createRouter({
    routeTree: rootRoute.addChildren([
      appLayout.addChildren([indexRoute, projectRoute]),
      settingsRoute,
      onboardingRoute,
    ]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  } as any);
}

/** Install a router where projectStore's lazy accessor will find it. */
async function withRouterAt(initialEntry: string) {
  const router = makeRouter(initialEntry);
  await router.load();
  (globalThis as any).__RELIANT_ROUTER = router;
  const { useProjectStore } = await import("../projectStore");
  return { router, useProjectStore };
}

const project = (id: string) => ({
  id,
  name: `project-${id}`,
  path: `/tmp/${id}`,
  is_git_repo: false,
  worktree_count: 0,
  last_active: "",
  created_at: "",
  updated_at: "",
  is_forge: false,
});

/**
 * Selecting a project runs a long tail of side effects (chats, worktrees,
 * workflows, analytics). We only care about the URL, so we invoke selection
 * the way the store does and let the rest reject harmlessly.
 */
async function selectProjectIgnoringSideEffects(
  useProjectStore: any,
  id: string,
) {
  await useProjectStore
    .getState()
    .selectProject(project(id), { skipClear: true, skipWorkspaceStateSave: true })
    .catch(() => undefined);
}

describe("syncProjectUrl — search params survive project selection", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  afterEach(() => {
    delete (globalThis as any).__RELIANT_ROUTER;
    vi.restoreAllMocks();
  });

  it("preserves ?tour when moving / → /project/$id", async () => {
    const { router, useProjectStore } = await withRouterAt(
      "/?tour=chat-and-sidebars",
    );

    await selectProjectIgnoringSideEffects(useProjectStore, "p1");

    expect(router.state.location.pathname).toBe("/project/p1");
    // The whole point: the tour param is what starts the post-onboarding tour.
    expect((router.state.location.search as any).tour).toBe("chat-and-sidebars");
  });

  it("preserves ?tour when moving between two projects", async () => {
    const { router, useProjectStore } = await withRouterAt(
      "/project/p1?tour=workspaces",
    );

    await selectProjectIgnoringSideEffects(useProjectStore, "p2");

    expect(router.state.location.pathname).toBe("/project/p2");
    expect((router.state.location.search as any).tour).toBe("workspaces");
  });

  it("leaves the URL alone entirely while the user is on /onboarding", async () => {
    // ensureProject creates and selects a project mid-flow; that must not
    // yank the user out of onboarding before the flow decides to leave.
    const { router, useProjectStore } = await withRouterAt("/onboarding");

    await selectProjectIgnoringSideEffects(useProjectStore, "p1");

    expect(router.state.location.pathname).toBe("/onboarding");
  });

  it("still does not navigate away from /settings", async () => {
    const { router, useProjectStore } = await withRouterAt("/settings");

    await selectProjectIgnoringSideEffects(useProjectStore, "p1");

    expect(router.state.location.pathname).toBe("/settings");
  });
});
