/**
 * syncProjectFilterUrl must not clobber search params.
 *
 * The URL carries the sidebar's project filter: filtering to a project drives
 * the router to `/project/$id`, and "All projects" back to `/`. That
 * navigation is about the PATHNAME only — every search param on the URL
 * belongs to someone else and has to survive it. Selecting the context
 * project (selectProject) never navigates at all.
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
import { describe, it, expect, beforeAll, beforeEach, afterEach, vi } from "vitest";
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  Outlet,
} from "@tanstack/react-router";
import { indexSearchSchema } from "../../routeSchemas";
import { mockTransport } from "../../lib/test-utils";

// projectStore pulls in the gRPC client graph via its sibling stores; none of
// it is exercised here (syncProjectUrl only touches the router), so the heavy
// leaves are stubbed to keep this a focused unit test.
//
// `touchProject` is stubbed: selectProject sends it fire-and-forget, and the
// unit-test transport refusing it would surface as a logged rejection that
// has nothing to do with the URL.
vi.mock("../../hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/project-queries")>()),
  touchProject: vi.fn(),
}));

// Selecting a project also loads its chats, presets and workflows in the
// background. Unanswered, each was a real RPC that settled after the test — and
// under load, late enough to time the test out. The chat list is answered at
// the transport (beforeEach).
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

  // A splat stands in for every Workflows-area page: the store only reads the
  // pathname, so one route that matches them all is enough.
  const workflowsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/workflows/$",
    component: () => <div>workflows</div>,
  });
  const inboxRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/inbox",
    component: () => <div>inbox</div>,
  });
  const forgeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/forge",
    component: () => <div>forge</div>,
  });
  const forgeEnvRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/forge/env/$env",
    component: () => <div>forge env</div>,
  });

  return createRouter({
    routeTree: rootRoute.addChildren([
      appLayout.addChildren([indexRoute, projectRoute]),
      settingsRoute,
      onboardingRoute,
      workflowsRoute,
      inboxRoute,
      forgeRoute,
      forgeEnvRoute,
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
  last_active: "",
  created_at: "",
  updated_at: "",
  is_forge: false,
});

/**
 * Selecting a project runs a long tail of side effects (worktrees, analytics).
 * We only care about the URL, so we invoke selection the way the store does
 * and let the rest reject harmlessly.
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

/** Filter the sidebar the way setProjectFilter reflects it into the URL. */
async function filterTo(id: string | null) {
  const { syncProjectFilterUrl } = await import("../projectStore");
  syncProjectFilterUrl(id);
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("syncProjectFilterUrl — search params survive the project filter", () => {
  // Each test imports a FRESH projectStore (vi.resetModules), but the first
  // import in a worker also transforms its whole module graph: ~4-5s alone,
  // past the test timeout under a loaded full run. Transform it once here,
  // on the hook's own budget, so every test's import is only an evaluation.
  beforeAll(async () => {
    await import("../projectStore");
  }, 60_000);

  beforeEach(async () => {
    vi.resetModules();
    // The transport seam of the module instance the re-imported store uses.
    const { setTransport } = await import("../../lib/connect");
    setTransport(mockTransport({ "ChatService.ListChats": { response: { chats: [] } } }));
  });

  afterEach(() => {
    delete (globalThis as any).__RELIANT_ROUTER;
    vi.restoreAllMocks();
  });

  it("preserves ?tour when moving / → /project/$id", async () => {
    const { router } = await withRouterAt(
      "/?tour=chat-and-sidebars",
    );

    await filterTo("p1");

    expect(router.state.location.pathname).toBe("/project/p1");
    // The whole point: the tour param is what starts the post-onboarding tour.
    expect((router.state.location.search as any).tour).toBe("chat-and-sidebars");
  });

  it("preserves ?tour when moving between two projects", async () => {
    const { router } = await withRouterAt(
      "/project/p1?tour=workspaces",
    );

    await filterTo("p2");

    expect(router.state.location.pathname).toBe("/project/p2");
    expect((router.state.location.search as any).tour).toBe("workspaces");
  });

  it("preserves ?tour when All projects moves /project/$id → /", async () => {
    const { router } = await withRouterAt("/project/p1?tour=workspaces");

    await filterTo(null);

    expect(router.state.location.pathname).toBe("/");
    expect((router.state.location.search as any).tour).toBe("workspaces");
  });

  it("never navigates when only the context project changes", async () => {
    const { router, useProjectStore } = await withRouterAt("/?tour=workspaces");

    await selectProjectIgnoringSideEffects(useProjectStore, "p1");

    expect(router.state.location.pathname).toBe("/");
  });

  it("leaves the URL alone entirely while the user is on /onboarding", async () => {
    // ensureProject creates and selects a project mid-flow; that must not
    // yank the user out of onboarding before the flow decides to leave.
    const { router } = await withRouterAt("/onboarding");

    await filterTo("p1");

    expect(router.state.location.pathname).toBe("/onboarding");
  });

  it("still does not navigate away from /settings", async () => {
    const { router } = await withRouterAt("/settings");

    await filterTo("p1");
    expect(router.state.location.pathname).toBe("/settings");
    await filterTo(null);

    expect(router.state.location.pathname).toBe("/settings");
  });

  // Only the chat shell's URL carries the filter: no filter change may
  // navigate the user out of the Workflows area (WORKFLOW_UI.md §1.3), the
  // Inbox or Forge into the chat view.
  it.each([
    "/workflows/runs/chat-1",
    "/workflows/runs",
    "/workflows/automations/trig-1",
    "/workflows/library",
    "/workflows/library/builtin%3A%2F%2Fagent",
    "/inbox",
    // Forge's header project switcher selects a project to change Forge's
    // scope; it must not navigate out of Forge into the chat view.
    "/forge",
    "/forge/env/prod",
  ])("leaves the user on %s when the filter changes", async (path) => {
    const { router } = await withRouterAt(path);

    await filterTo("p1");
    expect(router.state.location.pathname).toBe(path);
    await filterTo(null);

    expect(router.state.location.pathname).toBe(path);
  });
});
