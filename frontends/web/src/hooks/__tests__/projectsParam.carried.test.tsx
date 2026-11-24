/**
 * The chat list's filter of several projects (`?projects=a,b`) is in the URL
 * of every chat shell page, not only the home: each tab's selection is its
 * own URL, so a refresh, a shared link or another tab never loses or takes
 * it.
 *
 *   - moving between chats, to the new-chat view and back keeps it
 *     (retainSearchParams on the shell's layout, as routes.tsx declares it)
 *   - choosing several projects from a chat keeps the chat on screen and
 *     names them in its URL
 *   - choosing one project (or every project) drops it
 *   - the sidebar's bridge reads it on a chat's page too
 */
import { act, cleanup, renderHook } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, retainSearchParams, type AnyRouter } from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("@/hooks/project-queries", async (original) => ({
  ...(await original<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects: [], isLoading: false, error: null, refetch: () => {} }),
  getCachedProjects: () => [{ id: "a", name: "a", path: "/a" }],
  fetchProjects: async () => [{ id: "a", name: "a", path: "/a" }],
  touchProject: vi.fn(),
}));
vi.mock("@/hooks/sidebar-project-queries", () => ({
  useSidebarProjectPrefs: () => ({
    prefs: { pinned: new Set(), hidden: new Set(), showAll: false, filter: [] },
    writable: true,
    loadFailed: false,
    setFilter: () => {},
  }),
}));

import { navigateToChatUrl, navigateToNewChatUrl } from "@/lib/chatRoute";
import { chatViewSearchSchema, indexSearchSchema, newChatSearchSchema, PROJECTS_CARRIED } from "@/routeSchemas";
import { useChatStore } from "@/store/chatStore";
import { chooseProject, useProjectStore } from "@/store/projectStore";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";

import { resetChatProjectFilterForTests, useChatProjectFilter } from "../useChatProjectFilter";

let router: AnyRouter;

/** The chat shell's routes, under a layout configured as routes.tsx's `_app`. */
async function shellAt(url: string) {
  const root = createRootRoute();
  const app = createRoute({
    getParentRoute: () => root,
    id: "_app",
    validateSearch: indexSearchSchema,
    search: { middlewares: [retainSearchParams([...PROJECTS_CARRIED])] },
  });
  const home = createRoute({ getParentRoute: () => app, path: "/" });
  const project = createRoute({ getParentRoute: () => app, path: "project/$projectId" });
  const chat = createRoute({ getParentRoute: () => app, path: "c/$chatId", validateSearch: chatViewSearchSchema });
  const draft = createRoute({ getParentRoute: () => app, path: "new", validateSearch: newChatSearchSchema });
  router = createRouter({
    routeTree: root.addChildren([app.addChildren([home, project, chat, draft])]),
    history: createMemoryHistory({ initialEntries: [url] }),
  }) as unknown as AnyRouter;
  await router.load();
  (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = router;
}

const at = () => router.history.location;
const settle = () => act(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
});

beforeEach(() => {
  resetChatProjectFilterForTests();
  useWorkspaceStateStore.setState({ projectFilterId: null });
  useProjectStore.setState({ currentProject: { id: "a", name: "a", path: "/a" } as never });
  vi.spyOn(useChatStore.getState(), "loadChats").mockResolvedValue();
});

afterEach(() => {
  cleanup();
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe("?projects= rides on every chat shell page", () => {
  it("is kept from the home to a chat, to another chat, and to the new-chat view", async () => {
    await shellAt("/?projects=a,b");
    navigateToChatUrl("chat-1");
    await settle();
    expect(at().pathname).toBe("/c/chat-1");
    expect(at().search).toBe("?projects=a%2Cb");
    navigateToChatUrl("chat-2");
    await settle();
    expect(at().pathname).toBe("/c/chat-2");
    expect(at().search).toBe("?projects=a%2Cb");
    navigateToNewChatUrl({ project: "a" });
    await settle();
    expect(at().pathname).toBe("/new");
    expect(new URLSearchParams(at().search).get("projects")).toBe("a,b");
  });

  it("is dropped when one project is chosen", async () => {
    await shellAt("/c/chat-1?projects=a,b");
    await act(async () => {
      await chooseProject("a", { restoreWorkspace: false });
    });
    expect(at().pathname).toBe("/project/a");
    expect(at().search).toBe("");
  });

  it("choosing several from a chat keeps the chat and names them in its URL", async () => {
    await shellAt("/c/chat-1");
    const view = renderHook(() => useChatProjectFilter());
    act(() => view.result.current.set(["a", "b"]));
    await settle();
    expect(at().pathname).toBe("/c/chat-1");
    expect(new URLSearchParams(at().search).get("projects")).toBe("a,b");
    act(() => view.result.current.set([]));
    await settle();
    expect(at().pathname).toBe("/c/chat-1");
    expect(at().search).toBe("");
  });

  it("the sidebar's bridge reads it on a chat's page", () => {
    const view = renderHook(() => useChatProjectFilter({ sync: { pathname: "/c/chat-1", search: { projects: "a,b" } } }));
    expect(view.result.current).toMatchObject({ mode: "many", ids: ["a", "b"] });
  });
});
